package orchestratorwait

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeAutopilot struct {
	on        bool
	err       error
	questions []string
}

func (f *fakeAutopilot) OnWaitDeclared(_ context.Context, id domain.SessionID, question string) (bool, error) {
	f.questions = append(f.questions, string(id)+": "+question)
	return f.on, f.err
}

// While Autopilot runs for the orchestrator the user hears nothing: the wait is
// handed to Autopilot, which has the orchestrator decide it.
func TestDeclareWhileAutopilotRunsNotifiesNobody(t *testing.T) {
	w := newWorld()
	ap := &fakeAutopilot{on: true}
	svc := New(Deps{Store: w, Notifier: w, Settings: w, Autopilot: ap, Clock: func() time.Time { return w.now }})

	outcome, err := svc.Declare(ctx, "mer-orchestrator-1", "Merge PR #42 now, or wait for the second review?")
	if err != nil || outcome != OutcomeAutopilot {
		t.Fatalf("outcome = %q, %v, want %q", outcome, err, OutcomeAutopilot)
	}
	if len(w.rows) != 0 {
		t.Fatalf("rows = %+v: no notification may be stored while Autopilot is on", w.rows)
	}
	if len(ap.questions) != 1 || ap.questions[0] != "mer-orchestrator-1: Merge PR #42 now, or wait for the second review?" {
		t.Fatalf("autopilot was told %v", ap.questions)
	}
}

// With Autopilot off (or not running for this orchestrator) nothing changes:
// the user is notified exactly as before.
func TestDeclareWithoutAutopilotNotifiesAsBefore(t *testing.T) {
	w := newWorld()
	ap := &fakeAutopilot{on: false}
	svc := New(Deps{Store: w, Notifier: w, Settings: w, Autopilot: ap, Clock: func() time.Time { return w.now }})

	outcome, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?")
	if err != nil || outcome != OutcomeNotified {
		t.Fatalf("outcome = %q, %v, want %q", outcome, err, OutcomeNotified)
	}
	if len(w.open()) != 1 {
		t.Fatalf("open waits = %d, want 1", len(w.open()))
	}
}

// Autopilot is only asked about a real orchestrator wait: a worker or a missing
// question is refused before it is consulted.
func TestDeclareRefusesWorkersAndEmptyQuestionsBeforeAutopilotSeesThem(t *testing.T) {
	w := newWorld()
	ap := &fakeAutopilot{on: true}
	svc := New(Deps{Store: w, Notifier: w, Settings: w, Autopilot: ap, Clock: func() time.Time { return w.now }})
	if _, err := svc.Declare(ctx, "mer-1", "Ship it?"); apiCode(err) != "NOT_AN_ORCHESTRATOR" {
		t.Fatalf("worker err = %v", err)
	}
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "   "); apiCode(err) != "WAITING_QUESTION_REQUIRED" {
		t.Fatalf("empty err = %v", err)
	}
	if len(ap.questions) != 0 {
		t.Fatalf("autopilot was consulted for %v", ap.questions)
	}
}

// If Autopilot cannot record the wait the call fails loudly rather than falling
// through to a notification the user asked not to get, or to silence.
func TestDeclareSurfacesAnAutopilotFailure(t *testing.T) {
	w := newWorld()
	ap := &fakeAutopilot{err: errors.New("database is locked")}
	svc := New(Deps{Store: w, Notifier: w, Settings: w, Autopilot: ap, Clock: func() time.Time { return w.now }})
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err == nil {
		t.Fatal("want the Autopilot failure to surface")
	}
	if len(w.rows) != 0 {
		t.Fatalf("rows = %+v, want none", w.rows)
	}
}
