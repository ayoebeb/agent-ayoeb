package autopilot

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// One command that merges two pull requests is allowed only if both are approved
// on their exact head with green checks: the approved one cannot vouch for the
// other, whichever order they come in.
func TestACompoundMergeNeedsEveryPullRequestToBeApprovedAndGreen(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	first, second := prFact(12, domain.CIPassing), prFact(13, domain.CIPassing)
	e.gates.facts[worker.ID] = []domain.PRFacts{first, second}
	// Only 13 has an approved AO review.
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{review(second.URL, domain.VerdictApproved, e.now)}

	for i, cmd := range []string{
		"gh pr merge 12 --squash && gh pr merge 13 --squash",
		"gh pr merge 13 --squash && gh pr merge 12 --squash",
		`bash -c "gh pr merge 12 && gh pr merge 13"`,
	} {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("c"+string(rune('0'+i)), "Bash", "execute", `{"command":"`+strings.ReplaceAll(cmd, `"`, `\"`)+`"}`))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
			t.Fatalf("%q decisions = %v, want reject: PR 12 has no approved review", cmd, got)
		}
	}

	// With both approved and green the same request goes through.
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{
		review(first.URL, domain.VerdictApproved, e.now),
		review(second.URL, domain.VerdictApproved, e.now),
	}
	before := len(e.approvals.decisions)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("c9", "Bash", "execute", `{"command":"gh pr merge 12 && gh pr merge 13"}`))
	if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "allow" {
		t.Fatalf("two approved merges decisions = %v, want allow", got)
	}

	// One of them failing CI refuses the whole request again.
	e.gates.facts[worker.ID] = []domain.PRFacts{first, prFact(13, domain.CIFailing)}
	before = len(e.approvals.decisions)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("c10", "Bash", "execute", `{"command":"gh pr merge 12 && gh pr merge 13"}`))
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
