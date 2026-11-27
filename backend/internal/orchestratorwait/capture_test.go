package orchestratorwait

import (
	"context"
	"testing"
)

type fakeRecorder struct{ got []string }

func (f *fakeRecorder) RecordAnswer(_ context.Context, project, session, question, answer string) {
	f.got = append(f.got, project+"|"+session+"|"+question+"|"+answer)
}

// The owner's reply to a declared question is captured with the question, the
// project and the session, and the wait still closes as before.
func TestUserRepliedTextRecordsTheQuestionAndTheAnswer(t *testing.T) {
	w := newWorld()
	svc := w.service()
	rec := &fakeRecorder{}
	svc.SetAnswerRecorder(rec)
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Merge PR #42 now, or wait for the second review?"); err != nil {
		t.Fatal(err)
	}
	svc.UserRepliedText(ctx, "mer-orchestrator-1", "merge now")
	want := "mer|mer-orchestrator-1|Merge PR #42 now, or wait for the second review?|merge now"
	if len(rec.got) != 1 || rec.got[0] != want {
		t.Fatalf("recorded %v, want [%s]", rec.got, want)
	}
	if len(w.open()) != 0 {
		t.Fatalf("the wait must close: %d open", len(w.open()))
	}
}

// A message with no question open (the owner just writing to the orchestrator)
// is not a decision and is not captured.
func TestUserRepliedTextWithoutAnOpenQuestionRecordsNothing(t *testing.T) {
	w := newWorld()
	svc := w.service()
	rec := &fakeRecorder{}
	svc.SetAnswerRecorder(rec)
	svc.UserRepliedText(ctx, "mer-orchestrator-1", "hello there")
	if len(rec.got) != 0 {
		t.Fatalf("recorded %v, want nothing", rec.got)
	}
	// And a service with no recorder behaves as before.
	plain := w.service()
	plain.UserRepliedText(ctx, "mer-orchestrator-1", "hello")
}

// While Autopilot decides a question there is no owner answer to learn from.
func TestAnAutopilotDecidedQuestionIsNotCaptured(t *testing.T) {
	w := newWorld()
	svc := w.service()
	svc.SetAutopilot(&fakeAutopilot{on: true})
	rec := &fakeRecorder{}
	svc.SetAnswerRecorder(rec)
	if out, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil || out != OutcomeAutopilot {
		t.Fatalf("declare = %q, %v", out, err)
	}
	svc.UserRepliedText(ctx, "mer-orchestrator-1", "ja")
	if len(rec.got) != 0 {
		t.Fatalf("recorded %v, want nothing", rec.got)
	}
}
