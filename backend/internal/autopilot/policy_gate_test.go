package autopilot

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The shapes of delete and merge AO can prove are allowed; everything else that
// deletes or merges is parked, never allowed.
func TestCanonicalDeletesAreAllowed(t *testing.T) {
	for _, cmd := range []string{
		"rm -rf dist",
		"rm -rf node_modules dist",
		"rm -f ./build/cache.txt",
		"rm -r --force out/tmp",
		"rmdir build/empty",
		"rm -rf " + testWorkspace + "/web/dist",
		"rm -rf 'dir with space/x'",
		"Remove-Item -Recurse -Force dist",
		"del /s /q build\\cache",
		"unlink notes.txt",
	} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) = %+v, want a plain delete inside the workspace allowed", cmd, v)
		}
	}
	// The directory the provider says the command runs in is used for relative targets.
	in := Action{Tool: "Bash", Kind: "execute", Command: "rm -rf dist", Workspace: testWorkspace, Cwd: testWorkspace + "/web"}
	if v := Evaluate(in); !v.Allow {
		t.Errorf("a delete in a workspace subfolder = %+v, want allow", v)
	}
}

func TestEveryOtherDeleteIsParked(t *testing.T) {
	for _, cmd := range []string{
		// directory changes, chaining and grouping
		"cd web && rm -rf dist",
		"cd web; rm -rf dist",
		"pushd web && rm -rf dist",
		"Set-Location web; Remove-Item -Recurse dist",
		"(rm -rf dist)",
		"{ rm -rf dist; }",
		"true && rm -rf dist",
		"rm -rf dist || true",
		"rm -rf dist && echo done",
		"rm -rf dist | cat",
		"rm -rf dist &",
		"rm -rf dist\nrm -rf out",
		// the conditional cd reported by the reviewer
		"false && cd " + testWorkspace + "; rm -rf data",
		"cd /tmp/x || cd " + testWorkspace + "; rm -rf data",
		"test -d web && cd web; rm -rf dist",
		"cd web || exit 1; rm -rf dist",
		// substitutions, variables, environment, wrappers, globs
		"rm -rf $(cat list.txt)",
		"rm -rf `cat list.txt`",
		"rm -rf $TARGET",
		"rm -rf ${TARGET}",
		"rm -rf ~/old",
		"rm -rf dist/*",
		"rm -rf dist?",
		"FOO=bar rm -rf dist",
		"env rm -rf dist",
		"nohup rm -rf dist",
		"command rm -rf dist",
		"xargs rm -rf",
		"find . -name '*.tmp' -delete",
		"find . -name '*.tmp' -exec rm {} ;",
		"git clean -fdx",
		"git rm -r old",
		"/bin/rm -rf dist",
		`bash -c "rm -rf dist"`,
		`sh -c 'rm -rf dist'`,
		"eval 'rm -rf dist'",
		"$RM -rf dist",
		"$(which rm) -rf dist",
		// quoting tricks around the word
		`r""m -rf dist`,
		`r\m -rf dist`,
		// targets that are not provably inside the workspace
		"rm -rf",
		"rm -rf .",
		"rm -rf " + testWorkspace,
		"rm -rf ..",
		"rm -rf ../sibling",
		"rm -rf /tmp/x",
		"rm -rf dist ../x",
		"rm --no-preserve-root -rf dist",
		"rm -rf -- dist",
		"rm -rf -Path /etc",
	} {
		if v := Evaluate(bash(cmd)); v.Allow || v.Merge || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = %+v, want it parked as data_delete", cmd, v)
		}
	}
	// A refusal the parser already made (here: administrator rights) stands.
	if v := Evaluate(bash("sudo rm -rf dist")); v.Allow || v.Limit != domain.HardLimitAdmin {
		t.Errorf("sudo rm = %+v, want the admin refusal", v)
	}
	// A cwd outside the workspace turns every relative target into an outside one.
	out := Action{Tool: "Bash", Kind: "execute", Command: "rm -rf dist", Workspace: testWorkspace, Cwd: "/tmp/other-project"}
	if v := Evaluate(out); v.Allow || v.Limit != domain.HardLimitDataDelete {
		t.Errorf("a delete whose cwd is outside the workspace = %+v, want parked", v)
	}
}

func TestCanonicalAOMergeIsLeftToTheService(t *testing.T) {
	const url = "https://github.com/owner/repo/pull/12"
	for _, cmd := range []string{
		aoMerge(12, url, testSHA),
		"ao pr merge 12 --expected-head-sha " + testSHA + " --url " + url,
		aoMerge(12, "https://ghe.example.com/owner/repo/pull/12", testSHA),
		aoMerge(12, url, "0123456789ABCDEF0123456789ABCDEF01234567"),
	} {
		v := Evaluate(bash(cmd))
		if !v.Merge || v.Allow || len(v.MergeTargets) != 1 || v.MergeTargets[0].Number != "12" || v.MergeTargets[0].HeadSHA != strings.ToLower(v.MergeTargets[0].HeadSHA) {
			t.Errorf("Evaluate(%q) = %+v, want one merge of PR 12 left to the service", cmd, v)
		}
	}
}

// A bare number can name the wrong repository, and so can any flag or variable
// that picks one. AO's merge names the pull request by its full URL and checks the
// number against it, so none of these is accepted.
func TestEveryOtherAOMergeIsParked(t *testing.T) {
	const url = "https://github.com/owner/repo/pull/12"
	for _, cmd := range []string{
		"ao pr merge 12",
		"ao pr merge 12 --url " + url,
		"ao pr merge 12 --expected-head-sha " + testSHA,
		"ao pr merge 13 --url " + url + " --expected-head-sha " + testSHA,
		"ao pr merge 12 --url https://github.com/owner/repo/pull/12/files --expected-head-sha " + testSHA,
		"ao pr merge 12 --url http://github.com/owner/repo/pull/12 --expected-head-sha " + testSHA,
		"ao pr merge 12 --url https://github.com/owner/repo/issues/12 --expected-head-sha " + testSHA,
		"ao pr merge 12 --url " + url + " --expected-head-sha abc123",
		"ao pr merge 12 --url " + url + " --expected-head-sha nothexnothexnothexnothexnothexnothexno",
		"ao pr merge 12 --url " + url + " --url " + url + " --expected-head-sha " + testSHA,
		aoMerge(12, url, testSHA) + " --squash",
		aoMerge(12, url, testSHA) + " --repo owner/other",
		"ao --repo owner/other pr merge 12 --url " + url + " --expected-head-sha " + testSHA,
		"AO_REPO=other ao pr merge 12 --url " + url + " --expected-head-sha " + testSHA,
		"env " + aoMerge(12, url, testSHA),
		"/usr/local/bin/" + aoMerge(12, url, testSHA),
		"./" + aoMerge(12, url, testSHA),
		"AO pr merge 12 --url " + url + " --expected-head-sha " + testSHA,
		aoMerge(12, url, testSHA) + " && " + aoMerge(13, "https://github.com/owner/repo/pull/13", testSHA),
		aoMerge(12, url, testSHA) + "; rm -rf dist",
		"cd /tmp/x && " + aoMerge(12, url, testSHA),
		`bash -c "` + aoMerge(12, url, testSHA) + `"`,
		"ao pr merge $PR --url " + url + " --expected-head-sha " + testSHA,
		"ao pr merge 12 --url $URL --expected-head-sha " + testSHA,
		`a""o pr merge 12 --url ` + url + ` --expected-head-sha ` + testSHA,
	} {
		if v := Evaluate(bash(cmd)); v.Allow || v.Merge || v.Limit == "" {
			t.Errorf("Evaluate(%q) = %+v, want it parked with a hard limit and never left to the service", cmd, v)
		}
	}
}

func TestEveryOtherMergeIsParked(t *testing.T) {
	for _, cmd := range []string{
		// gh global flags before the subcommand
		"gh -R owner/repo-b pr merge 12",
		"gh --repo owner/repo-b pr merge 12",
		"gh --repo=owner/repo-b pr merge 12 --squash",
		"gh --hostname ghe.example.com pr merge 12",
		// repository or host chosen by the environment
		"GH_REPO=owner/repo-b gh pr merge 12",
		"GH_HOST=ghe.example.com gh pr merge 12",
		"GH_ENTERPRISE_TOKEN=x gh pr merge 12",
		"env GH_REPO=owner/repo-b gh pr merge 12",
		"export GH_REPO=owner/repo-b; gh pr merge 12",
		// repository chosen by a flag after the subcommand
		"gh pr merge 12 --repo owner/repo-b",
		"gh pr merge 12 -R owner/repo-b",
		"gh pr merge -R owner/repo-b 12",
		// a pull request URL on another host is still a URL, not a number
		"gh pr merge http://github.com/owner/repo/pull/12",
		"gh pr merge https://github.com/owner/repo/pull/12/files",
		"gh pr merge https://github.com/owner/repo/issues/12",
		// options outside the allowed four
		"gh pr merge 12 --admin",
		"gh pr merge 12 --auto",
		"gh pr merge 12 --delete-branch",
		"gh pr merge 12 --body hello",
		"gh pr merge 12 -s",
		"gh pr merge 12 --squash --match-head-commit nothex",
		// not a plain number or URL
		"gh pr merge",
		"gh pr merge my-branch",
		"gh pr merge #12",
		"gh pr merge $PR",
		"gh pr merge $(gh pr list --json number -q .[0].number)",
		// chaining, wrappers, nesting, other ways to merge
		"cd /tmp/x && gh pr merge 12",
		"gh pr merge 12 && gh pr merge 13",
		"(gh pr merge 12)",
		`bash -c "gh pr merge 12"`,
		"eval 'gh pr merge 12'",
		"nohup gh pr merge 12",
		"/usr/bin/gh pr merge 12",
		"GH pr merge 12",
		`g""h pr merge 12`,
		"gh api -X PUT repos/owner/repo/pulls/12/merge",
		"gh api repos/owner/repo/pulls/12/merge --method PUT",
		"gh pr merge 12 --squash; rm -rf dist",
	} {
		if v := Evaluate(bash(cmd)); v.Allow || v.Merge || v.Limit == "" {
			t.Errorf("Evaluate(%q) = %+v, want it parked with a hard limit and never left to the PR facts", cmd, v)
		}
	}
	// A merge into the default branch by push is parked too.
	for _, cmd := range []string{"git push origin main", "git push origin HEAD:main", "git push origin HEAD:refs/heads/main", "git push --force origin main"} {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitMainPush {
			t.Errorf("Evaluate(%q) = %+v, want it parked as main_push", cmd, v)
		}
	}
	// A local git merge and a pull request that is only read are ordinary work.
	for _, cmd := range []string{"git merge origin/main", "gh pr view 12 --json state", "gh pr checks 12", "gh pr create --fill", "go test ./..."} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) = %+v, want allow", cmd, v)
		}
	}
}

// The program a delete runs under can be written many ways. Every spelling is
// recognised as a delete; only the bare name is ever auto-approved, and only in
// its plain form.
func TestDeleteExecutableVariantsAreGatedAndOnlyTheBareNameIsAllowed(t *testing.T) {
	parked := []string{
		"rm.exe -rf dist",
		"RM.EXE -rf dist",
		"Rm -rf dist/*",
		"/bin/rm -rf dist",
		"/usr/bin/rm -rf dist",
		"./rm -rf dist",
		"../tools/rm -rf dist",
		`C:\Windows\System32\rm.exe -rf dist`,
		`C:\Tools\rm.cmd -rf dist`,
		"del.exe /s /q build",
		"DEL.EXE build/x",
		"erase.exe build/x",
		"rmdir.exe build/x",
		"rd.exe /s /q build",
		"ri.exe -Recurse dist",
		"ri.ps1 -Recurse dist",
		"remove-item.exe dist",
		"Remove-Item.cmd -Recurse dist",
		"pwsh -c 'ri -Recurse dist'",
		"powershell.exe -Command ri -Recurse dist",
		"cmd.exe /c del build\\x",
		"cmd /c rd /s /q build",
		"env rm.exe -rf dist",
		"busybox rm -rf dist",
		"unlink.exe notes.txt",
		"shred.exe notes.txt",
		"find . -name x -delete",
	}
	for _, cmd := range parked {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit == "" {
			t.Errorf("Evaluate(%q) = %+v, want it parked with a hard limit", cmd, v)
		}
	}
	// The aliases are all deletes too, and allowed only as the bare plain form.
	for _, cmd := range []string{"rm -rf dist", "ri -Recurse dist", "rd /s /q build", "rmdir build/empty", "del /q build/x", "erase build/x", "unlink notes.txt", "Remove-Item -Recurse -Force dist", "RM -rf dist", "REMOVE-ITEM dist"} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) = %+v, want the plain delete allowed", cmd, v)
		}
	}
}

// PowerShell reads $ @ ` : ( ) { } ; | and commas as syntax inside an argument:
// subexpressions, splatting, drive and parameter-value colons, groups, lists of
// several paths. None of it can be checked as a literal path, so it is parked, and
// only -Recurse -Force -LiteralPath -Path are accepted as parameters.
func TestPowerShellSyntaxAndParametersAreParked(t *testing.T) {
	parked := []string{
		"Remove-Item -Recurse $env:TEMP",
		"Remove-Item -Recurse $HOME\\x",
		"Remove-Item @args",
		"Remove-Item @{Path='dist'}",
		"Remove-Item -Path:C:/Users/x/data",
		"Remove-Item -LiteralPath:dist",
		"Remove-Item -Recurse:$true dist",
		"Remove-Item -Force:$false dist",
		"Remove-Item -Path (Get-Content list.txt)",
		"Remove-Item (Join-Path dist x)",
		"Remove-Item -Path { dist }",
		"Remove-Item -Path dist; Remove-Item -Path out",
		"Remove-Item -Path dist | Out-Null",
		"Remove-Item dist, out",
		"Remove-Item dist,/etc/x",
		"Remove-Item -Path dist,out",
		"Remove-Item C:/Users/x/Documents",
		"Remove-Item -Path C:/work/dist",
		"Remove-Item -Path ..\\x",
		"Remove-Item -Path ../x",
		"Remove-Item -LiteralPath /etc/x",
		"Remove-Item -Path",
		"Remove-Item -Path -Recurse",
		"Remove-Item dist -Include *.log",
		"Remove-Item dist -Exclude keep",
		"Remove-Item dist -WhatIf",
		"Remove-Item dist -Confirm:$false",
		"Remove-Item dist -ErrorAction SilentlyContinue",
		"Remove-Item dist -Filter x",
		"Remove-Item dist -Credential x",
		"Remove-Item dist -Stream x",
		"Remove-Item dist -UseTransaction",
		"Remove-Item -Recurse -Force",
		"Remove-Item dist\\*",
		"Remove-Item 'dist'`n-Path ../x",
	}
	for _, cmd := range parked {
		if v := Evaluate(bash(cmd)); v.Allow || v.Limit != domain.HardLimitDataDelete {
			t.Errorf("Evaluate(%q) = %+v, want it parked as data_delete", cmd, v)
		}
	}
	for _, cmd := range []string{
		"Remove-Item -Recurse -Force dist",
		"Remove-Item -Path dist",
		"Remove-Item -LiteralPath dist -Recurse -Force",
		"Remove-Item -Path dist -Recurse",
		"remove-item -literalpath build/cache",
		"ri -Path dist -Force -Recurse",
		"Remove-Item dist out",
	} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) = %+v, want it allowed: literal paths inside the workspace", cmd, v)
		}
	}
}
