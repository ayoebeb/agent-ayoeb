package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func orchestratorRecord(now time.Time, state domain.ActivityState) domain.SessionRecord {
	return domain.SessionRecord{
		ID: "mer-orchestrator-1", ProjectID: "mer", Kind: domain.KindOrchestrator, DisplayName: "Orchestrator",
		Activity:      domain.Activity{State: state, LastActivityAt: now.Add(-time.Minute)},
		FirstSignalAt: now.Add(-time.Minute),
	}
}

func resolutionTypes(list []ports.NotificationResolution) []domain.NotificationType {
	out := make([]domain.NotificationType, 0, len(list))
	for _, res := range list {
		out = append(out, res.Type)
	}
	return out
}

// A pending approval or question on an orchestrator is the user's main contact
// waiting: it raises the distinct notification, with a key of its own so a wait
// the user never opened cannot swallow the next one.
func TestOrchestratorWaiting_NeedsInputTransitionEmitsDistinctNotification(t *testing.T) {
	for _, state := range []domain.ActivityState{domain.ActivityWaitingInput, domain.ActivityBlocked} {
		t.Run(string(state), func(t *testing.T) {
			st := newFakeStore()
			sink := &fakeNotificationSink{}
			m := New(st, nil, WithNotificationSink(sink))
			now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
			m.clock = func() time.Time { return now }
			st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityActive)

			if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: state}); err != nil {
				t.Fatal(err)
			}
			if len(sink.intents) != 1 {
				t.Fatalf("intents = %d, want 1: %+v", len(sink.intents), sink.intents)
			}
			intent := sink.intents[0]
			if intent.Type != domain.NotificationOrchestratorWaiting || intent.SessionID != "mer-orchestrator-1" || intent.ProjectID != "mer" {
				t.Fatalf("intent = %+v", intent)
			}
			if want := domain.OrchestratorWaitingSourceKey("mer-orchestrator-1", intent.CreatedAt); intent.SourceKey != want {
				t.Fatalf("source key = %q, want %q", intent.SourceKey, want)
			}
		})
	}
}

// Workers keep exactly the behaviour they had.
func TestOrchestratorWaiting_WorkerNeedsInputIsUnchanged(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(-time.Minute)}, FirstSignalAt: now.Add(-time.Minute),
	}
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityBlocked}); err != nil {
		t.Fatal(err)
	}
	if len(sink.intents) != 1 || sink.intents[0].Type != domain.NotificationNeedsInput || sink.intents[0].SourceKey != "" {
		t.Fatalf("worker intents = %+v, want one plain needs_input", sink.intents)
	}
	// Leaving the pause resolves only the needs-input row, as before.
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive}); err != nil {
		t.Fatal(err)
	}
	if got := resolutionTypes(sink.resolutions); len(got) != 1 || got[0] != domain.NotificationNeedsInput {
		t.Fatalf("worker resolutions = %v", got)
	}
}

// Answering the approval is the user responding, so the wait closes. Both kinds
// close because the kind depends on a setting that may have changed meanwhile.
func TestOrchestratorWaiting_AnsweringTheApprovalResolvesTheWait(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityBlocked)

	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive}); err != nil {
		t.Fatal(err)
	}
	got := resolutionTypes(sink.resolutions)
	if len(got) != 2 || got[0] != domain.NotificationNeedsInput || got[1] != domain.NotificationOrchestratorWaiting {
		t.Fatalf("resolutions = %v, want needs_input then orchestrator_waiting", got)
	}
}

// A question the orchestrator declared lives while the session is idle, so the
// activity never entered the needs-input family. A prompt the human submits is
// the reply that closes it.
func TestOrchestratorWaiting_HumanPromptResolvesADeclaredQuestion(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityIdle)

	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
		ConversationCheckpointOrigin: domain.ConversationCheckpointOriginHuman,
	}); err != nil {
		t.Fatal(err)
	}
	got := resolutionTypes(sink.resolutions)
	if len(got) != 1 || got[0] != domain.NotificationOrchestratorWaiting {
		t.Fatalf("resolutions = %v, want orchestrator_waiting", got)
	}
}

// A prompt whose origin is unknown is treated as the human's: a wait cleared too
// early is declared again when the orchestrator's next turn ends, while one
// that is never cleared would nag forever.
func TestOrchestratorWaiting_UnknownPromptOriginCountsAsTheHuman(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityIdle)

	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolutionTypes(sink.resolutions); len(got) != 1 || got[0] != domain.NotificationOrchestratorWaiting {
		t.Fatalf("resolutions = %v", got)
	}
}

// A worker's message reaches the orchestrator as a coordination prompt. It is
// not the user, so the question stays open.
func TestOrchestratorWaiting_CoordinationPromptKeepsTheQuestionOpen(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityIdle)

	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit",
		ConversationCheckpointOrigin: domain.ConversationCheckpointOriginCoordination,
	}); err != nil {
		t.Fatal(err)
	}
	if len(sink.resolutions) != 0 {
		t.Fatalf("a worker's message closed the user's question: %+v", sink.resolutions)
	}
}

// A worker's prompt never resolves orchestrator rows: the rule is the
// orchestrator's alone.
func TestOrchestratorWaiting_WorkerPromptResolvesNothing(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-1"] = domain.SessionRecord{
		ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Minute)}, FirstSignalAt: now.Add(-time.Minute),
	}
	if err := m.ApplyActivitySignal(ctx, "mer-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "user-prompt-submit"}); err != nil {
		t.Fatal(err)
	}
	if len(sink.resolutions) != 0 {
		t.Fatalf("resolutions = %+v, want none", sink.resolutions)
	}
}

// An orchestrator that ends has nobody to wait for, even when it was waiting on
// a question it declared rather than on an approval.
func TestOrchestratorWaiting_TerminationResolvesADeclaredQuestion(t *testing.T) {
	st := newFakeStore()
	sink := &fakeNotificationSink{}
	m := New(st, nil, WithNotificationSink(sink))
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityIdle)

	if err := m.MarkTerminated(ctx, "mer-orchestrator-1"); err != nil {
		t.Fatal(err)
	}
	if got := resolutionTypes(sink.resolutions); len(got) != 1 || got[0] != domain.NotificationOrchestratorWaiting {
		t.Fatalf("resolutions = %v", got)
	}
}
