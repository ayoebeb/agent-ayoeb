package autopilot

import (
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// codexApproval builds the detail the Codex adapter stores for an approval:
// method, command (unwrapped), rawCommand, cwd (see codexappserver.parseApproval).
func codexApproval(method, raw, cwd string) []byte {
	doc := map[string]any{"method": method}
	if raw != "" {
		doc["command"] = raw
		doc["rawCommand"] = raw
	}
	if cwd != "" {
		doc["cwd"] = cwd
	}
	out, _ := json.Marshal(doc)
	return out
}

// Real Codex requests carry no tool name or kind, only the JSON-RPC method and a
// top-level command. They must be judged like any other command, not allowed by
// default.
func TestEvaluateReadsCodexApprovalPayloads(t *testing.T) {
	const exec = "item/commandExecution/requestApproval"
	cases := []struct {
		name    string
		command string
		cwd     string
		allow   bool
		limit   domain.HardLimit
	}{
		{"go test", "go test ./...", testWorkspace, true, ""},
		{"sudo", "sudo apt install x", testWorkspace, false, domain.HardLimitAdmin},
		{"push to main", "git push origin main", testWorkspace, false, domain.HardLimitMainPush},
		{"delete outside", "rm -rf /tmp/other", testWorkspace, false, domain.HardLimitDataDelete},
		{"wrapped shell", `/bin/bash -lc "git push origin main"`, testWorkspace, false, domain.HardLimitMainPush},
		{"delete in the provider's cwd outside the workspace", "rm -rf data", "/tmp/other-project", false, domain.HardLimitDataDelete},
		{"relative delete in a workspace subfolder", "rm -rf dist", testWorkspace + "/web", true, ""},
		{"powershell", `"C:/Program Files/PowerShell/7/pwsh.exe" -Command "Remove-Item -Recurse C:/Users/x/Documents"`, testWorkspace, false, domain.HardLimitDataDelete},
	}
	for _, c := range cases {
		a := ParseApproval("Run "+c.command, codexApproval(exec, c.command, c.cwd), testWorkspace)
		if a.Command == "" || a.Kind != "execute" {
			t.Fatalf("%s: parsed %+v, want an execute request with its command", c.name, a)
		}
		if v := Evaluate(a); v.Allow != c.allow || v.Limit != c.limit {
			t.Errorf("%s: verdict allow=%v limit=%q (%s), want allow=%v limit=%q", c.name, v.Allow, v.Limit, v.Reason, c.allow, c.limit)
		}
	}

	// A merge is left to the PR facts, like any other agent's merge.
	merge := ParseApproval("Run gh pr merge 7", codexApproval(exec, "gh pr merge 7 --squash", testWorkspace), testWorkspace)
	if v := Evaluate(merge); !v.Merge || len(v.MergeTargets) != 1 || v.MergeTargets[0].Ref != "7" {
		t.Fatalf("codex merge = %+v", v)
	}

	// File changes name no files in the adapter's payload, so AO cannot tell they
	// stay inside the workspace: refused, never allowed by default.
	change := ParseApproval("Apply file changes", codexApproval("item/fileChange/requestApproval", "", ""), testWorkspace)
	if change.Kind != "edit" {
		t.Fatalf("file change parsed as %+v", change)
	}
	if v := Evaluate(change); v.Allow {
		t.Fatalf("a file change that names no files was allowed: %+v", v)
	}
	// Any other request whose effect cannot be determined is refused too.
	other := ParseApproval("Approval required", []byte(`{"method":"item/permissions/requestApproval"}`), testWorkspace)
	if v := Evaluate(other); v.Allow {
		t.Fatalf("an unidentifiable request was allowed: %+v", v)
	}
	// A command request whose command is missing is refused.
	empty := ParseApproval("Run", []byte(`{"method":"item/commandExecution/requestApproval"}`), testWorkspace)
	if v := Evaluate(empty); v.Allow {
		t.Fatalf("a command request with no command was allowed: %+v", v)
	}
}

// A deletion is judged where the shell really is, not where the session started.
func TestEvaluateFollowsDirectoryChangesBeforeADeletion(t *testing.T) {
	denied := []string{
		"cd /tmp/x && rm -rf data",
		"cd /tmp/other-project; rm -rf data",
		"cd .. && rm -rf data",
		"cd ../../.. && rm -rf build",
		"cd / && rm -rf tmp",
		"pushd /var/lib/app && rm -rf cache",
		"Set-Location C:/Users/x/Documents; Remove-Item -Recurse data",
		"(cd /tmp/x && rm -rf data)",
		"cd /tmp/x && cd sub && rm -rf data",
		`bash -c "cd /tmp/x && rm -rf data"`,
		`cd /tmp/x && bash -c "rm -rf data"`,
		"cd -; rm -rf data",
		"cd ~ && rm -rf old",
		`cd "$TARGET" && rm -rf data`,
		"cd $(mktemp -d) && rm -rf data",
		"popd && rm -rf data",
		"cd /tmp/x && find . -delete",
	}
	for _, cmd := range denied {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = allow %v limit %q (%s), want a data_delete refusal", cmd, v.Allow, v.Limit, v.Reason)
		}
	}
	// Deleting after any cd is parked, even when the parser can prove where it
	// lands: a delete is only ever allowed as one plain command (see canonicalDelete).
	parked := []string{
		"cd web && rm -rf dist",
		"cd " + testWorkspace + "/web && rm -rf node_modules",
		"cd /tmp/x && cd " + testWorkspace + " && rm -rf dist",
		"cd web && cd .. && rm -rf dist",
	}
	for _, cmd := range parked {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = allow %v limit %q, want it parked as data_delete", cmd, v.Allow, v.Limit)
		}
	}
	// A directory change on its own is ordinary work.
	if v := Evaluate(bash("cd /tmp/x && go test ./...")); !v.Allow {
		t.Errorf("a cd with no delete was denied: %+v", v)
	}
	// The provider's own cwd counts, nested shells included.
	in := Action{Tool: "Bash", Kind: "execute", Command: `sh -c "rm -rf data"`, Workspace: testWorkspace, Cwd: "/tmp/other-project"}
	if v := Evaluate(in); v.Allow || v.Limit != domain.HardLimitDataDelete {
		t.Errorf("a nested delete in the provider's cwd = %+v", v)
	}
}

// A merge is allowed only as exactly one plain `gh pr merge`; any compound
// request that holds a merge is parked, so no later merge can vouch for an
// earlier one and none can hide in a nested script.
func TestEvaluateParksEveryCompoundMerge(t *testing.T) {
	for _, cmd := range []string{
		"gh pr merge 12 --squash && gh pr merge 13 --squash",
		"gh pr merge 12; gh pr merge 13",
		`bash -c "gh pr merge 12 && gh pr merge 13"`,
		`gh pr merge 12 && bash -c "gh pr merge 13"`,
		"gh pr merge 12 && echo $(gh pr merge 13)",
		"gh pr merge && gh pr merge 13",
		"gh pr merge 12 && git push origin main",
		"gh pr merge 12 || true",
		"gh pr merge 12 | cat",
	} {
		if v := Evaluate(bash(cmd)); v.Allow || v.Merge || v.Limit != domain.HardLimitMainPush {
			t.Errorf("Evaluate(%q) = %+v, want it parked as main_push", cmd, v)
		}
	}
}

// A command substitution runs in the directory its own command starts in, and a
// ( ) group is a subshell: what it does to the directory never reaches the parent.
func TestEvaluateKeepsSubstitutionsAndSubshellsInTheirOwnDirectory(t *testing.T) {
	denied := []string{
		`cd /tmp/other-project && echo "$(rm -rf data)"`,
		"cd /tmp/other-project && echo $(rm -rf data)",
		"cd /tmp/other-project && echo `rm -rf data`",
		"cd /tmp/other-project && cat <(rm -rf data)",
		"cd /tmp/other-project && echo $(echo $(rm -rf data))",
		"cd /tmp/other-project; (cd " + testWorkspace + " && true); rm -rf data",
		"cd /tmp/other-project; (cd " + testWorkspace + "); rm -rf data",
		"(cd /tmp/x && rm -rf data)",
		"(cd /tmp/x); (rm -rf /etc/y)",
		"echo $(cd /tmp/x && rm -rf data)",
		"cd /tmp/other-project && (echo hi; rm -rf data)",
		"for f in a b; do cd /tmp/x; rm -rf data; done",
		"if cd /tmp/x; then rm -rf data; fi",
	}
	for _, cmd := range denied {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = allow %v limit %q (%s), want a data_delete refusal", cmd, v.Allow, v.Limit, v.Reason)
		}
	}
	// These stay inside the workspace, but a delete next to a cd, a subshell or a
	// substitution is parked rather than argued about.
	parked := []string{
		"(cd /tmp/x && true); rm -rf dist",
		"(cd /tmp/x); rm -rf dist",
		"echo $(cd /tmp/x && pwd); rm -rf dist",
		"cd web && echo $(rm -rf dist)",
		"(cd web && rm -rf dist); rm -rf out",
		"echo $(git rev-parse HEAD) && rm -rf dist",
		"cd /tmp/x; (cd " + testWorkspace + " && rm -rf dist)",
	}
	for _, cmd := range parked {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = allow %v limit %q, want it parked as data_delete", cmd, v.Allow, v.Limit)
		}
	}
	for _, cmd := range []string{"(cd /tmp/x && true)", "echo $(cd /tmp/x && pwd)", "echo $(git rev-parse HEAD) && go test ./..."} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) denied (%s: %s), want allow: it deletes nothing", cmd, v.Limit, v.Reason)
		}
	}
}
