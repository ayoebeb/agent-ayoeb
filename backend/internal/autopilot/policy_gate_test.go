package autopilot

import (
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

func TestCanonicalMergesAreDecidedAgainstTheTrackedPR(t *testing.T) {
	for cmd, ref := range map[string]string{
		"gh pr merge 12":                                             "12",
		"gh pr merge 12 --squash":                                    "12",
		"gh pr merge 12 --merge":                                     "12",
		"gh pr merge 12 --rebase":                                    "12",
		"gh pr merge 12 --squash --match-head-commit abc1234":        "12",
		"gh pr merge 12 --rebase --match-head-commit=abc1234def":     "12",
		"gh pr merge https://github.com/owner/repo/pull/12":          "https://github.com/owner/repo/pull/12",
		"gh pr merge https://github.com/owner/repo/pull/12 --squash": "https://github.com/owner/repo/pull/12",
		"gh pr merge https://ghe.example.com/owner/repo/pull/12/":    "https://ghe.example.com/owner/repo/pull/12/",
	} {
		v := Evaluate(bash(cmd))
		if !v.Merge || v.Allow || len(v.MergeTargets) != 1 || v.MergeTargets[0].Ref != ref {
			t.Errorf("Evaluate(%q) = %+v, want one merge of %q left to the PR facts", cmd, v, ref)
		}
	}
}

// The four bypasses the review found, and the rest of their family: every one is
// parked, none is left to the PR facts.
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
	// A local git merge and a pull request that is only read are ordinary work.
	for _, cmd := range []string{"git merge origin/main", "gh pr view 12 --json state", "gh pr checks 12", "gh pr create --fill", "go test ./..."} {
		if v := Evaluate(bash(cmd)); !v.Allow {
			t.Errorf("Evaluate(%q) = %+v, want allow", cmd, v)
		}
	}
}
