package usagelimit

import (
	"context"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ListLatestFailedTurns is the session's latest failed turn with what became of
// its stop row, as the database reports it.
func (f *fakeStore) ListLatestFailedTurns(_ context.Context, ids []domain.SessionID) ([]domain.LimitStopTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.LimitStopTurn
	for _, turn := range f.turns {
		for _, id := range ids {
			if turn.SessionID != id {
				continue
			}
			if stop := f.stops[turn.TurnID]; stop != nil {
				turn.ResumeState, turn.SkipReason, turn.StopResetsAt = stop.ResumeState, stop.SkipReason, stop.ResetsAt
			}
			out = append(out, turn)
		}
	}
	return out, nil
}

type fakeHeadroom map[string]bool

func (h fakeHeadroom) HasHeadroom(_ context.Context, accountID string) bool {
	has, known := h[accountID]
	return !known || has
}

// A session that stopped on the old account's limit continues at once on the
// account it was moved to, with no wait for the reset.
func TestResumeAfterAccountMoveContinuesALimitStoppedSession(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep() // recorded as pending, resets at 17:00; it is only 14:21

	sentNow, err := h.svc.ResumeAfterAccountMove(context.Background(), "w1", "acct-2")
	if err != nil || !sentNow {
		t.Fatalf("resume = %v, %v, want the continue message sent", sentNow, err)
	}
	if len(h.sender.sent) != 1 || h.sender.sent[0].id != "w1" ||
		!strings.Contains(h.sender.sent[0].message, "another account") ||
		!strings.Contains(h.sender.sent[0].message, "Continue where you left off") {
		t.Fatalf("sent = %+v, want one continue message naming the new account", h.sender.sent)
	}
	if state := h.store.stops["turn-w1"].ResumeState; state != domain.UsageLimitStopResumed {
		t.Fatalf("stop state = %q, want resumed so the watcher does not continue it again", state)
	}
	// The reset passing later must not send a second message.
	h.now = resetAt.Add(10 * 60 * 1e9)
	h.sweep()
	if len(h.sender.sent) != 1 {
		t.Fatalf("sent %d messages, want exactly one", len(h.sender.sent))
	}
}

// Moving a session that is stopped but whose automatic resume is off still
// continues it: the move is an explicit action.
func TestResumeAfterAccountMoveContinuesASessionWhoseAutoResumeIsOff(t *testing.T) {
	h := newHarness(t)
	h.settings.enabled = false
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()

	sentNow, err := h.svc.ResumeAfterAccountMove(context.Background(), "w1", "acct-2")
	if err != nil || !sentNow || len(h.sender.sent) != 1 {
		t.Fatalf("resume = %v, %v, sent = %+v, want one message", sentNow, err, h.sender.sent)
	}
}

func TestResumeAfterAccountMoveLeavesOtherSessionsAlone(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()

	// A session that is not stopped on a limit gets nothing.
	if sentNow, err := h.svc.ResumeAfterAccountMove(context.Background(), "w-idle", "acct-2"); err != nil || sentNow {
		t.Fatalf("idle session: resume = %v, %v, want nothing sent", sentNow, err)
	}
	// Neither does one whose target account is itself at its limit.
	h.svc.headroom = fakeHeadroom{"acct-full": false}
	if sentNow, err := h.svc.ResumeAfterAccountMove(context.Background(), "w1", "acct-full"); err != nil || sentNow {
		t.Fatalf("full account: resume = %v, %v, want nothing sent", sentNow, err)
	}
	if len(h.sender.sent) != 0 {
		t.Fatalf("sent = %+v, want none", h.sender.sent)
	}
	// A second move of an already continued session does not repeat the message.
	h.svc.headroom = nil
	if sentNow, _ := h.svc.ResumeAfterAccountMove(context.Background(), "w1", "acct-2"); !sentNow {
		t.Fatal("first resume was not sent")
	}
	if sentNow, _ := h.svc.ResumeAfterAccountMove(context.Background(), "w1", "acct-3"); sentNow {
		t.Fatal("an already continued session was continued twice")
	}
}
