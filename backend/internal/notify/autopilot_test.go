package notify

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A parked Autopilot item is the user's to handle, so it needs no pull request,
// names the session and the item, and is stored once per item key.
func TestAutopilotParkedNotificationPayload(t *testing.T) {
	st := &fakeStore{}
	mgr := waitingManager(st, NewHub(), nil)
	err := mgr.Notify(context.Background(), Intent{
		Type: domain.NotificationAutopilotParked, SessionID: "mer-orchestrator-1", ProjectID: "mer",
		SessionDisplayName: "Orchestrator", Detail: "Buy more CI minutes: money: the free minutes ran out",
		SourceKey: "autopilot_parked:ape_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.rows) != 1 {
		t.Fatalf("rows = %+v", st.rows)
	}
	got := st.rows[0]
	if got.Type != domain.NotificationAutopilotParked || got.Title != "Orchestrator: Autopilot parked an item for you" ||
		got.Body != "Buy more CI minutes: money: the free minutes ran out" || got.PRURL != "" || got.SourceKey != "autopilot_parked:ape_1" {
		t.Fatalf("stored = %+v", got)
	}
	if !got.Type.NeedsResolution() {
		t.Fatal("a parked item stays open until the user handles it")
	}
}

// Autopilot answers the permission requests of the sessions it covers, so the
// "needs your input" row for them is not stored; every other session, and every
// other kind of notification, is untouched.
func TestSuppressDropsOnlyWhatAutopilotAnswers(t *testing.T) {
	st := &fakeStore{}
	var asked []ports.NotificationIntent
	mgr := waitingManager(st, NewHub(), func(d *Deps) {
		d.Suppress = func(_ context.Context, in ports.NotificationIntent) bool {
			asked = append(asked, in)
			return in.SessionID == "covered"
		}
	})
	ctx := context.Background()
	for _, in := range []Intent{
		{Type: domain.NotificationNeedsInput, SessionID: "covered", ProjectID: "mer"},
		{Type: domain.NotificationOrchestratorWaiting, SessionID: "covered", ProjectID: "mer", SourceKey: "orchestrator_waiting:a"},
		{Type: domain.NotificationAutopilotParked, SessionID: "covered", ProjectID: "mer", SourceKey: "autopilot_parked:a"},
		{Type: domain.NotificationReadyToMerge, SessionID: "covered", ProjectID: "mer", PRURL: "https://github.com/o/r/pull/1"},
	} {
		if err := mgr.Notify(ctx, in); err != nil {
			t.Fatalf("notify %s: %v", in.Type, err)
		}
	}
	if len(st.rows) != 2 || st.rows[0].Type != domain.NotificationAutopilotParked || st.rows[1].Type != domain.NotificationReadyToMerge {
		t.Fatalf("stored = %+v, want the parked item and the ready-to-merge row only", st.rows)
	}
	if len(asked) != 2 {
		t.Fatalf("asked about %d intents, want only needs_input and orchestrator_waiting", len(asked))
	}

	st2 := &fakeStore{}
	other := waitingManager(st2, NewHub(), func(d *Deps) {
		d.Suppress = func(context.Context, ports.NotificationIntent) bool { return false }
	})
	if err := other.Notify(ctx, Intent{Type: domain.NotificationNeedsInput, SessionID: "mer-1", ProjectID: "mer"}); err != nil || len(st2.rows) != 1 {
		t.Fatalf("an uncovered session must still notify: rows=%d err=%v", len(st2.rows), err)
	}
}
