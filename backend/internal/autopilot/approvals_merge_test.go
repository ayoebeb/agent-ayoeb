package autopilot

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// One command that merges two pull requests is never allowed, even when both are
// approved and green: the approved one cannot vouch for the other, whichever
// order they come in. A single canonical merge of an approved, green PR is.
func TestACompoundMergeIsAlwaysParked(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	first, second := prFact(12, domain.CIPassing), prFact(13, domain.CIPassing)
	e.gates.facts[worker.ID] = []domain.PRFacts{first, second}
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{
		review(first.URL, domain.VerdictApproved, e.now),
		review(second.URL, domain.VerdictApproved, e.now),
	}

	for i, cmd := range []string{
		"gh pr merge 12 --squash && gh pr merge 13 --squash",
		"gh pr merge 13 --squash && gh pr merge 12 --squash",
		`bash -c "gh pr merge 12 && gh pr merge 13"`,
		"gh pr merge 12 && gh pr merge 13",
	} {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("c"+string(rune('0'+i)), "Bash", "execute", `{"command":"`+strings.ReplaceAll(cmd, `"`, `\"`)+`"}`))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
			t.Fatalf("%q decisions = %v, want reject: a compound merge is parked", cmd, got)
		}
	}

	// Each one on its own is judged on its own review and checks.
	for ref, want := range map[string]string{"12": "allow", "13": "allow"} {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("s"+ref, "Bash", "execute", `{"command":"gh pr merge `+ref+` --squash"}`))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != want {
			t.Fatalf("a single merge of %s decisions = %v, want %s", ref, got, want)
		}
	}
	e.gates.facts[worker.ID] = []domain.PRFacts{first, prFact(13, domain.CIFailing)}
	before := len(e.approvals.decisions)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("f13", "Bash", "execute", `{"command":"gh pr merge 13"}`))
	if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
		t.Fatalf("a merge on failing CI decisions = %v, want reject", got)
	}
}

// A Codex request, shaped like the adapter's stored detail, goes through the same
// service path: a forbidden command is declined, an ordinary one allowed.
func TestHandleApprovalAnswersCodexShapedRequests(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")

	e.svc.HandleApproval(e.ctx, orch.ID, rawApproval("x1", `{"method":"item/commandExecution/requestApproval","command":"sudo rm -rf /","rawCommand":"sudo rm -rf /","cwd":"/ws/orchestrator"}`))
	e.svc.HandleApproval(e.ctx, orch.ID, rawApproval("x2", `{"method":"item/commandExecution/requestApproval","command":"go test ./...","rawCommand":"go test ./...","cwd":"/ws/orchestrator"}`))
	e.svc.HandleApproval(e.ctx, orch.ID, rawApproval("x3", `{"method":"item/fileChange/requestApproval"}`))
	if got := strings.Join(e.approvals.decisions, ","); got != "reject,allow,reject" {
		t.Fatalf("codex decisions = %s, want reject,allow,reject", got)
	}
}

// rawApproval is an approval whose detail is stored as the provider adapter wrote it.
func rawApproval(id, detail string) ApprovalNotice {
	n := approval(id, "", "", "{}")
	n.Summary, n.Detail = "Approval required", []byte(detail)
	return n
}

// Repository and host identity of a merge: the number or URL the command names
// must be the tracked pull request's own.
func TestMergeIsDecidedOnRepositoryHostAndNumber(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	inA := domain.PRFacts{URL: "https://github.com/owner/repo-a/pull/12", Number: 12, CI: domain.CIPassing, HeadSHA: "abc123", SourceBranch: "ao/a"}
	e.gates.facts[worker.ID] = []domain.PRFacts{inA}
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{review(inA.URL, domain.VerdictApproved, e.now)}

	n := 0
	decide := func(cmd string) string {
		n++
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("r"+string(rune('a'+n)), "Bash", "execute", `{"command":"`+cmd+`"}`))
		got := e.approvals.decisions[before:]
		if len(got) != 1 {
			t.Fatalf("%s: decisions = %v", cmd, got)
		}
		return got[0]
	}
	for cmd, want := range map[string]string{
		// canonical forms of the tracked pull request
		"gh pr merge 12":          "allow",
		"gh pr merge 12 --squash": "allow",
		"gh pr merge 12 --match-head-commit abc1234 --rebase": "allow",
		"gh pr merge https://github.com/owner/repo-a/pull/12": "allow",
		// another repository, another host, a global flag or a repository override
		"gh pr merge https://github.com/owner/repo-b/pull/12":      "reject",
		"gh pr merge https://ghe.example.com/owner/repo-a/pull/12": "reject",
		"gh pr merge 12 --repo owner/repo-a":                       "reject",
		"gh pr merge 12 -R owner/repo-b":                           "reject",
		"gh -R owner/repo-b pr merge 12":                           "reject",
		"gh --repo owner/repo-b pr merge 12":                       "reject",
		"GH_REPO=owner/repo-b gh pr merge 12":                      "reject",
		"env GH_REPO=owner/repo-b gh pr merge 12":                  "reject",
		"GH_HOST=ghe.example.com gh pr merge 12":                   "reject",
		"gh pr merge 12 --admin":                                   "reject",
		"gh pr merge 12 --auto":                                    "reject",
		"gh pr merge 12 --delete-branch":                           "reject",
		"gh pr merge":                                              "reject",
		"gh pr merge my-branch":                                    "reject",
	} {
		if got := decide(cmd); got != want {
			t.Errorf("%q decided %s, want %s", cmd, got, want)
		}
	}

	// Two repositories tracked: a bare number could mean either, so only the full
	// URL of an approved pull request goes through.
	inB := domain.PRFacts{URL: "https://github.com/owner/repo-b/pull/12", Number: 12, CI: domain.CIPassing, HeadSHA: "def456", SourceBranch: "ao/b"}
	e.gates.facts[worker.ID] = []domain.PRFacts{inA, inB}
	for cmd, want := range map[string]string{
		"gh pr merge 12": "reject",
		"gh pr merge https://github.com/owner/repo-a/pull/12": "allow",
		"gh pr merge https://github.com/owner/repo-b/pull/12": "reject",
	} {
		if got := decide(cmd); got != want {
			t.Errorf("two repositories: %q decided %s, want %s", cmd, got, want)
		}
	}
}
