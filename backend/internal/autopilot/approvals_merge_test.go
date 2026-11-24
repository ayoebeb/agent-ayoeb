package autopilot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A compound request that holds a merge is parked, even when every pull request
// in it is approved and green; each merge must be its own command.
func TestACompoundMergeIsAlwaysParked(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	first, second := mergeFact(12, domain.CIPassing), mergeFact(13, domain.CIPassing)
	e.gates.facts[worker.ID] = []domain.PRFacts{first, second}
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{
		review(first.URL, domain.VerdictApproved, e.now),
		review(second.URL, domain.VerdictApproved, e.now),
	}
	one, two := mergeCmd(first), mergeCmd(second)

	for i, cmd := range []string{
		one + " && " + two,
		two + " && " + one,
		one + "; " + two,
		`bash -c "` + one + " && " + two + `"`,
	} {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("c"+string(rune('0'+i)), "Bash", "execute", jsonCommand(cmd)))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
			t.Fatalf("%q decisions = %v, want reject: a compound merge is parked", cmd, got)
		}
	}
	// Each one on its own is judged on its own review, checks and head.
	for _, cmd := range []string{one, two} {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("s", "Bash", "execute", jsonCommand(cmd)))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "allow" {
			t.Fatalf("%q decisions = %v, want allow", cmd, got)
		}
	}
	e.gates.facts[worker.ID] = []domain.PRFacts{first, mergeFact(13, domain.CIFailing)}
	before := len(e.approvals.decisions)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("f13", "Bash", "execute", jsonCommand(two)))
	if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
		t.Fatalf("a merge on failing CI decisions = %v, want reject", got)
	}
}

// mergeSHA is the head AO tracks for the pull requests the tests merge.
const mergeSHA = "0123456789abcdef0123456789abcdef01234567"

func mergeFact(n int, ci domain.CIState) domain.PRFacts {
	return domain.PRFacts{
		URL: fmt.Sprintf("https://github.com/owner/repo-a/pull/%d", n), Number: n, CI: ci,
		HeadSHA: mergeSHA, SourceBranch: fmt.Sprintf("ao/x-%d", n),
	}
}

func mergeCmd(f domain.PRFacts) string { return aoMerge(f.Number, f.URL, f.HeadSHA) }

func jsonCommand(cmd string) string {
	out, _ := json.Marshal(map[string]string{"command": cmd})
	return string(out)
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

// The merge command names its pull request by full URL, which fixes the host and
// the repository, and the head commit the caller reviewed. Anything else is
// parked, and a number alone never decides which repository is meant.
func TestAOMergeIsDecidedOnURLHeadReviewAndChecks(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	inA := mergeFact(12, domain.CIPassing)
	e.gates.facts[worker.ID] = []domain.PRFacts{inA}
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{review(inA.URL, domain.VerdictApproved, e.now)}

	n := 0
	decide := func(cmd string) string {
		n++
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("r"+string(rune('a'+n)), "Bash", "execute", jsonCommand(cmd)))
		got := e.approvals.decisions[before:]
		if len(got) != 1 {
			t.Fatalf("%s: decisions = %v", cmd, got)
		}
		return got[0]
	}
	otherSHA := "fedcba9876543210fedcba9876543210fedcba98"
	for cmd, want := range map[string]string{
		// the tracked pull request, at its tracked head
		aoMerge(12, inA.URL, mergeSHA):                  "allow",
		aoMerge(12, inA.URL, strings.ToUpper(mergeSHA)): "allow",
		// the same number in another repository or on another host
		aoMerge(12, "https://github.com/owner/repo-b/pull/12", mergeSHA):      "reject",
		aoMerge(12, "https://ghe.example.com/owner/repo-a/pull/12", mergeSHA): "reject",
		aoMerge(12, "https://github.com/OTHER/repo-a/pull/12", mergeSHA):      "reject",
		// a head that is not the one AO tracks
		aoMerge(12, inA.URL, otherSHA): "reject",
		// a pull request this run does not track
		aoMerge(99, "https://github.com/owner/repo-a/pull/99", mergeSHA): "reject",
		// a bare number, gh in every shape, other flags
		"ao pr merge 12":                                        "reject",
		"gh pr merge 12":                                        "reject",
		"gh pr merge " + inA.URL:                                "reject",
		"gh pr merge 12 --repo owner/repo-a":                    "reject",
		"gh -R owner/repo-b pr merge 12":                        "reject",
		"GH_REPO=owner/repo-b gh pr merge 12":                   "reject",
		aoMerge(12, inA.URL, mergeSHA) + " --repo owner/repo-b": "reject",
	} {
		if got := decide(cmd); got != want {
			t.Errorf("%q decided %s, want %s", cmd, got, want)
		}
	}

	// Two repositories tracked with the same number: only the URL says which, and
	// each is judged on its own review.
	inB := domain.PRFacts{URL: "https://github.com/owner/repo-b/pull/12", Number: 12, CI: domain.CIPassing, HeadSHA: otherSHA, SourceBranch: "ao/b"}
	e.gates.facts[worker.ID] = []domain.PRFacts{inA, inB}
	for cmd, want := range map[string]string{
		"ao pr merge 12":               "reject",
		aoMerge(12, inA.URL, mergeSHA): "allow",
		aoMerge(12, inB.URL, otherSHA): "reject",
		aoMerge(12, inB.URL, mergeSHA): "reject",
	} {
		if got := decide(cmd); got != want {
			t.Errorf("two repositories: %q decided %s, want %s", cmd, got, want)
		}
	}
}
