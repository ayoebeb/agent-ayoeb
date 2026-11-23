package autopilot

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const testWorkspace = "/home/dev/.ao/worktrees/proj/proj-1"

func bash(cmd string) Action {
	return Action{Tool: "Bash", Kind: "execute", Command: cmd, Workspace: testWorkspace}
}

// Everything an agent does to build, test and ship its own branch must go
// through without waiting: that is the whole point of the policy.
func TestEvaluateAllowsOrdinaryWorkInsideTheWorkspace(t *testing.T) {
	for _, cmd := range []string{
		"go test ./...",
		"npm run typecheck && npm test -- --run",
		"git status --short",
		"git add -A && git commit -m 'feat: add the importer cache'",
		"git push -u origin ao/proj-1/importer",
		"git push origin HEAD:ao/proj-1/importer",
		"git push --force-with-lease origin ao/proj-1/importer",
		"git checkout -b ao/proj-1/fix",
		"git merge origin/feature-x",
		"gh pr create --title 'Fix' --body 'Details'",
		"gh pr view 42 --json state",
		"gh pr checks 42",
		"gh api repos/o/r/pulls/42",
		"gh auth status",
		"rm -rf node_modules dist",
		"rm -rf ./build/cache",
		"rm -f " + testWorkspace + "/tmp/out.log",
		"cat package.json | head -20",
		"curl -s https://example.com/api/health",
		"cat ./.npmrc",
		"FOO=bar make build",
		"mkdir -p out && cp a.txt out/",
	} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) denied (%s: %s), want allow", cmd, v.Limit, v.Reason)
		}
	}
}

func TestEvaluateDeniesEveryHardLimit(t *testing.T) {
	cases := []struct {
		cmd  string
		want domain.HardLimit
	}{
		// admin / elevated rights
		{"sudo apt-get install jq", domain.HardLimitAdmin},
		{"runas /user:Administrator cmd", domain.HardLimitAdmin},
		{"net user bob secret /add", domain.HardLimitAdmin},
		{"reg add HKLM\\Software\\X /v a /d 1", domain.HardLimitAdmin},
		{"schtasks /create /tn x /tr y", domain.HardLimitAdmin},
		// pushing to main / skipping review or CI
		{"git push origin main", domain.HardLimitMainPush},
		{"git push origin HEAD:main", domain.HardLimitMainPush},
		{"git push -f origin ao/x:refs/heads/master", domain.HardLimitMainPush},
		{"git push --all", domain.HardLimitMainPush},
		{"git push --no-verify origin ao/x", domain.HardLimitCI},
		{"git commit --no-verify -m wip", domain.HardLimitCI},
		{"git commit -m 'tweak [skip ci]'", domain.HardLimitCI},
		{"HUSKY=0 git commit -m x", domain.HardLimitCI},
		{"gh workflow disable ci.yml", domain.HardLimitCI},
		{"gh run cancel 123", domain.HardLimitCI},
		{"gh pr merge 42 --admin", domain.HardLimitCI},
		{"gh api -X PUT repos/o/r/branches/main/protection -f a=b", domain.HardLimitCI},
		// deleting outside the workspace
		{"rm -rf /", domain.HardLimitDataDelete},
		{"rm -rf ~/Documents", domain.HardLimitDataDelete},
		{"rm -rf $HOME/projects", domain.HardLimitDataDelete},
		{"rm -rf ../other-project", domain.HardLimitDataDelete},
		{"rm -rf /etc/hosts", domain.HardLimitDataDelete},
		{"mkfs.ext4 /dev/sda1", domain.HardLimitDataDelete},
		{"find /var/data -name '*.db' -delete", domain.HardLimitDataDelete},
		{"gh repo delete o/r --yes", domain.HardLimitDataDelete},
		// messaging people outside AO
		{"gh issue comment 5 --body 'ping'", domain.HardLimitMessaging},
		{"gh pr comment 5 --body 'ping'", domain.HardLimitMessaging},
		{"gh pr review 5 --approve", domain.HardLimitMessaging},
		{"gh issue create --title x", domain.HardLimitMessaging},
		{"mail -s hi bob@example.com", domain.HardLimitMessaging},
		{"curl -X POST -d '{}' https://hooks.slack.com/services/T/B/x", domain.HardLimitMessaging},
		{"npm publish", domain.HardLimitMessaging},
		// credentials
		{"gh auth login", domain.HardLimitCredentials},
		{"gh auth token", domain.HardLimitCredentials},
		{"git config --global credential.helper store", domain.HardLimitCredentials},
		{"docker login registry.example.com", domain.HardLimitCredentials},
		{"aws configure", domain.HardLimitCredentials},
		{"npm login", domain.HardLimitCredentials},
		{"cat ~/.ssh/id_ed25519", domain.HardLimitCredentials},
		{"echo token > ~/.npmrc", domain.HardLimitCredentials},
		{"cat /Users/dev/.aws/credentials", domain.HardLimitCredentials},
		{"gh secret set TOKEN", domain.HardLimitCredentials},
		// other computers
		{"ssh build@10.0.0.7 'make'", domain.HardLimitOtherPCs},
		{"scp out.tgz build@10.0.0.7:/srv", domain.HardLimitOtherPCs},
		{"rsync -av out/ build@10.0.0.7:/srv/out", domain.HardLimitOtherPCs},
		// money
		{"stripe charges create --amount 5000", domain.HardLimitMoney},
		{"aws ec2 purchase-reserved-instances-offering --id x", domain.HardLimitMoney},
		{"gcloud billing accounts list", domain.HardLimitMoney},
		{"gh api /orgs/o/settings/billing/actions", domain.HardLimitMoney},
	}
	for _, tc := range cases {
		v := Evaluate(bash(tc.cmd))
		if v.Allow || v.Limit != tc.want {
			t.Errorf("Evaluate(%q) = allow=%v limit=%q, want deny %q", tc.cmd, v.Allow, v.Limit, tc.want)
		}
	}
}

// A forbidden step behind && ; | or a newline is still found, and a refusal
// anywhere in the line refuses the whole line.
func TestEvaluateReadsEveryStepOfAChain(t *testing.T) {
	for _, cmd := range []string{
		"go test ./... && git push origin main",
		"echo ok; sudo rm -rf build",
		"make build\ngh issue comment 1 --body hi",
		"cat notes.txt | ssh host 'cat > notes.txt'",
		"env GIT_TERMINAL_PROMPT=0 git push origin HEAD:main",
		"time nohup git push origin master",
	} {
		if v := Evaluate(bash(cmd)); v.Allow {
			t.Errorf("Evaluate(%q) allowed a forbidden step", cmd)
		}
	}
}

func TestEvaluateLeavesAMergeToThePRFacts(t *testing.T) {
	v := Evaluate(bash("gh pr merge 42 --squash"))
	if v.Allow || !v.Merge || len(v.MergeTargets) != 1 || v.MergeTargets[0].Ref != "42" {
		t.Fatalf("merge verdict = %+v, want a merge request for PR 42", v)
	}
	v = Evaluate(bash("gh pr merge https://github.com/o/r/pull/9 --auto"))
	if !v.Merge || len(v.MergeTargets) != 1 || v.MergeTargets[0].Ref != "https://github.com/o/r/pull/9" {
		t.Fatalf("merge by URL = %+v", v)
	}
	// A refusal in another step still wins over the merge.
	if v := Evaluate(bash("gh pr merge 42 && git push origin main")); v.Merge || v.Limit != domain.HardLimitMainPush {
		t.Fatalf("merge chained with a push to main = %+v", v)
	}
}

func TestEvaluateFileTools(t *testing.T) {
	edit := func(p string) Action {
		return Action{Tool: "Edit", Kind: "edit", Paths: []string{p}, Workspace: testWorkspace}
	}
	if v := Evaluate(edit("src/app.ts")); !v.Allow {
		t.Errorf("a relative edit is inside the workspace: %+v", v)
	}
	if v := Evaluate(edit(testWorkspace + "/src/app.ts")); !v.Allow {
		t.Errorf("an absolute edit inside the workspace: %+v", v)
	}
	// Outside the workspace is refused but is not one of the hard limits, so it
	// is not parked for the user.
	if v := Evaluate(edit("/etc/hosts")); v.Allow || v.Limit != "" || v.Reason == "" {
		t.Errorf("a write outside the workspace = %+v, want a plain refusal", v)
	}
	if v := Evaluate(edit("../sibling/file.go")); v.Allow {
		t.Errorf("a write that climbs out of the workspace = %+v", v)
	}
	if v := Evaluate(edit("/home/dev/.ssh/config")); v.Limit != domain.HardLimitCredentials {
		t.Errorf("a write to a credential store = %+v", v)
	}
	del := Action{Tool: "Delete", Kind: "delete", Paths: []string{"/home/dev/Documents/a.txt"}, Workspace: testWorkspace}
	if v := Evaluate(del); v.Allow || v.Limit != domain.HardLimitDataDelete {
		t.Errorf("a delete outside the workspace = %+v", v)
	}
	read := Action{Tool: "Read", Kind: "read", Paths: []string{"/usr/include/stdio.h"}, Workspace: testWorkspace}
	if v := Evaluate(read); !v.Allow {
		t.Errorf("reading outside the workspace is not a hard limit: %+v", v)
	}
	read.Paths = []string{"/home/dev/.aws/credentials"}
	if v := Evaluate(read); v.Allow || v.Limit != domain.HardLimitCredentials {
		t.Errorf("reading a credential store = %+v", v)
	}
}

func TestEvaluateWindowsWorkspacePaths(t *testing.T) {
	ws := `C:\Users\Ayoeb\.ao\data\worktrees\agent-ayoeb\agent-ayoeb-34`
	in := Action{Tool: "Write", Kind: "edit", Paths: []string{`c:\users\ayoeb\.ao\data\worktrees\agent-ayoeb\agent-ayoeb-34\backend\x.go`}, Workspace: ws}
	if v := Evaluate(in); !v.Allow {
		t.Errorf("a Windows path differing only in case is inside: %+v", v)
	}
	out := Action{Tool: "Write", Kind: "edit", Paths: []string{`C:\Users\Ayoeb\Documents\notes.txt`}, Workspace: ws}
	if v := Evaluate(out); v.Allow {
		t.Errorf("a Windows path outside the workspace: %+v", v)
	}
	sibling := Action{Tool: "Write", Kind: "edit", Paths: []string{ws + `-copy\x.go`}, Workspace: ws}
	if v := Evaluate(sibling); v.Allow {
		t.Errorf("a sibling folder sharing the workspace prefix is not inside: %+v", v)
	}
}

func TestEvaluateMCPTools(t *testing.T) {
	for tool, want := range map[string]domain.HardLimit{
		"mcp__slack__send_message":    domain.HardLimitMessaging,
		"mcp__mail__reply":            domain.HardLimitMessaging,
		"mcp__stripe__create_charge":  domain.HardLimitMoney,
		"mcp__vault__read_password":   domain.HardLimitCredentials,
		"mcp__docs__create_invoice":   domain.HardLimitMoney,
		"mcp__ao__html_render":        "",
		"mcp__playwright__browser_go": "",
	} {
		v := Evaluate(Action{Tool: tool, Workspace: testWorkspace})
		if want == "" && !v.Allow {
			t.Errorf("%s denied (%s), want allow", tool, v.Limit)
		}
		if want != "" && (v.Allow || v.Limit != want) {
			t.Errorf("%s = allow=%v limit=%q, want deny %q", tool, v.Allow, v.Limit, want)
		}
	}
}

func TestParseApprovalReadsTheProviderPayload(t *testing.T) {
	detail := []byte(`{"protocol":"acp","toolKind":"execute","subjectKind":"command","providerToolName":"Bash","input":{"command":"git push origin main","description":"push"}}`)
	a := ParseApproval("Run git push", detail, testWorkspace)
	if a.Tool != "Bash" || a.Kind != "execute" || a.Command != "git push origin main" || a.Workspace != testWorkspace {
		t.Fatalf("parsed = %+v", a)
	}
	if v := Evaluate(a); v.Limit != domain.HardLimitMainPush {
		t.Fatalf("verdict = %+v", v)
	}

	edit := ParseApproval("Edit src/a.ts", []byte(`{"toolKind":"edit","providerToolName":"Edit","input":{"file_path":"src/a.ts","old_string":"a","new_string":"b"}}`), testWorkspace)
	if len(edit.Paths) != 1 || edit.Paths[0] != "src/a.ts" {
		t.Fatalf("edit parsed = %+v", edit)
	}

	// A payload AO cannot read says nothing about what it does, so it is refused
	// (without naming a hard limit) instead of being waved through.
	garbage := ParseApproval("Something", []byte("not json"), testWorkspace)
	if v := Evaluate(garbage); v.Allow || v.Limit != "" {
		t.Fatalf("garbage payload = %+v, want a plain refusal", v)
	}
	// A shell tool whose payload has no command falls back to its label.
	labeled := ParseApproval("Run `sudo reboot`", []byte(`{"toolKind":"execute"}`), testWorkspace)
	if v := Evaluate(labeled); v.Allow || v.Limit != domain.HardLimitAdmin {
		t.Fatalf("label fallback = %+v (command %q)", v, labeled.Command)
	}
}

func TestClassifyTextFlagsChoicesThatCrossALimit(t *testing.T) {
	flagged := map[string]domain.HardLimit{
		"Upgrade the paid plan so the build has more minutes":    domain.HardLimitMoney,
		"Upgrade the paid CI plan":                               domain.HardLimitMoney,
		"Buy more CI minutes so the queue clears":                domain.HardLimitMoney,
		"Skip the failing CI checks to merge faster":             domain.HardLimitCI,
		"Disable the CI checks until the release is out":         domain.HardLimitCI,
		"Force push to rewrite history":                          domain.HardLimitMainPush,
		"Run as administrator to install the driver":             domain.HardLimitAdmin,
		"Email the customer that the fix is live":                domain.HardLimitMessaging,
		"Rotate the API key and paste the new token in the repo": domain.HardLimitCredentials,
	}
	for text, want := range flagged {
		if got, ok := ClassifyText(text); !ok || got != want {
			t.Errorf("ClassifyText(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	for _, text := range []string{
		"Take the smaller refactor and keep the public API",
		"Do not upgrade the paid plan; use the free tier",
		"Keep CI as it is and fix the failing test instead of skipping it",
		"Ask the orchestrator to review the exact head",
		"Use the existing importer cache",
		"Tell the user in the final report what was left",
		"Upgrade the dependency to the latest version",
		"Keep the failing check and fix the code that breaks it",
	} {
		if got, ok := ClassifyText(text); ok {
			t.Errorf("ClassifyText(%q) flagged %q, want none", text, got)
		}
	}
}

// A forbidden step must not hide inside a command that runs another command.
func TestEvaluateReadsCommandsInsideCommands(t *testing.T) {
	cases := []struct {
		cmd   string
		limit domain.HardLimit
	}{
		{`sh -c 'git push origin main'`, domain.HardLimitMainPush},
		{`bash -lc "go test ./... && git push origin main"`, domain.HardLimitMainPush},
		{`eval "git push origin main"`, domain.HardLimitMainPush},
		{"echo $(rm -rf /etc)", domain.HardLimitDataDelete},
		{"echo `sudo ls`", domain.HardLimitAdmin},
		{`powershell -Command "Remove-Item -Recurse C:\Users\x\Documents"`, domain.HardLimitDataDelete},
		{`cmd /c del C:\Windows\x`, domain.HardLimitDataDelete},
		{`bash -c 'bash -c "sudo ls"'`, domain.HardLimitAdmin},
	}
	for _, c := range cases {
		if v := Evaluate(bash(c.cmd)); v.Allow || v.Limit != c.limit {
			t.Errorf("Evaluate(%q) = allow %v limit %q, want deny %q", c.cmd, v.Allow, v.Limit, c.limit)
		}
	}
	// An encoded script cannot be read, so it is refused without naming a limit.
	if v := Evaluate(bash("powershell -EncodedCommand AAAA")); v.Allow {
		t.Errorf("an undecodable encoded command was allowed")
	}
	for _, cmd := range []string{`bash -c "go build ./..."`, "echo $(git rev-parse HEAD)", `sh -c 'npm test'`} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) denied (%s: %s), want allow", cmd, v.Limit, v.Reason)
		}
	}
}
