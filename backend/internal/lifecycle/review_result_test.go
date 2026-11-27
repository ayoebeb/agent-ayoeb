package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeliverReviewResultSendsOnceWhenTheWorkerCanTakeIt(t *testing.T) {
	m, st, msg := newManager()
	blocked := working("mer-1")
	blocked.Activity.State = domain.ActivityBlocked
	st.sessions["mer-1"] = blocked
	run := domain.ReviewRun{
		ID: "run-1", SessionID: "mer-1", PRURL: "pr1", TargetSHA: "sha1",
		Verdict: domain.VerdictChangesRequested, Body: "- `a.go:3`: nil check\x1b[2J",
	}

	sent, err := m.DeliverReviewResult(context.Background(), run)
	if err != nil || sent || len(msg.msgs) != 0 {
		t.Fatalf("blocked worker: sent %v, err %v, msgs %v", sent, err, msg.msgs)
	}

	st.sessions["mer-1"] = working("mer-1")
	for range 2 {
		if sent, err = m.DeliverReviewResult(context.Background(), run); err != nil || !sent {
			t.Fatalf("sent %v, err %v", sent, err)
		}
	}
	if len(msg.msgs) != 1 {
		t.Fatalf("messages = %v, want exactly one", msg.msgs)
	}
	for _, want := range []string{"changes requested", "pr1", "sha1", "NOT on the pull request", "`a.go:3`: nil check"} {
		if !strings.Contains(msg.msgs[0], want) {
			t.Errorf("message lacks %q: %s", want, msg.msgs[0])
		}
	}
	if strings.Contains(msg.msgs[0], "\x1b") {
		t.Errorf("message keeps terminal control characters: %q", msg.msgs[0])
	}
}
