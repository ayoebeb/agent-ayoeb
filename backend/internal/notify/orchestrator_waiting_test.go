package notify

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func waitingManager(st *fakeStore, hub *Hub, deps func(*Deps)) *Manager {
	d := Deps{
		Store: st, Publisher: hub,
		Clock: func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) },
		NewID: func() string { return "ntf_1" },
	}
	if deps != nil {
		deps(&d)
	}
	return New(d)
}

// The notification the user sees: the title names the project in the user's own
// words, and the body is the orchestrator's one-line question.
func TestOrchestratorWaitingPayload(t *testing.T) {
	st := &fakeStore{}
	mgr := waitingManager(st, NewHub(), nil)
	err := mgr.Notify(context.Background(), Intent{
		Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1", ProjectID: "mer",
		ProjectName: "Mercury", Detail: "  Merge PR 42 now,\nor wait?  ", SourceKey: "orchestrator_waiting:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.rows) != 1 {
		t.Fatalf("rows = %+v", st.rows)
	}
	got := st.rows[0]
	if got.Type != domain.NotificationOrchestratorWaiting || got.Title != "Mercury: orchestrator wacht op u" ||
		got.Body != "Merge PR 42 now,\nor wait?" || got.Status != domain.NotificationUnread ||
		got.SessionID != "mer-orchestrator-1" || got.ProjectID != "mer" || got.PRURL != "" || got.SourceKey != "orchestrator_waiting:abc" {
		t.Fatalf("stored = %+v", got)
	}
}

// A wait that comes from an approval has no declared question, so it says what
// is being waited on in general terms.
func TestOrchestratorWaitingFallbackBody(t *testing.T) {
	st := &fakeStore{}
	mgr := waitingManager(st, NewHub(), nil)
	if err := mgr.Notify(context.Background(), Intent{Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1", ProjectID: "mer", ProjectName: "Mercury"}); err != nil {
		t.Fatal(err)
	}
	if got := st.rows[0].Body; got != orchestratorWaitingFallbackBody {
		t.Fatalf("body = %q", got)
	}
}

// Lifecycle does not know project names; the manager looks them up, and falls
// back to the id and then a generic word rather than an empty title.
func TestOrchestratorWaitingProjectNameLookup(t *testing.T) {
	for _, tt := range []struct {
		name    string
		resolve func(context.Context, domain.ProjectID) string
		want    string
	}{
		{"resolved", func(context.Context, domain.ProjectID) string { return "Mercury" }, "Mercury: orchestrator wacht op u"},
		{"unknown name falls back to the id", func(context.Context, domain.ProjectID) string { return "" }, "mer: orchestrator wacht op u"},
		{"no resolver falls back to the id", nil, "mer: orchestrator wacht op u"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			mgr := waitingManager(st, NewHub(), func(d *Deps) { d.ProjectName = tt.resolve })
			if err := mgr.Notify(context.Background(), Intent{Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1", ProjectID: "mer"}); err != nil {
				t.Fatal(err)
			}
			if got := st.rows[0].Title; got != tt.want {
				t.Fatalf("title = %q, want %q", got, tt.want)
			}
		})
	}
	if got := OrchestratorWaitingTitle("", ""); got != "project: orchestrator wacht op u" {
		t.Fatalf("empty title = %q", got)
	}
}

// With the feature off the intent becomes the ordinary needs-input row, which is
// what an orchestrator produced before this notification existed.
func TestOrchestratorWaitingOffFallsBackToNeedsInput(t *testing.T) {
	st := &fakeStore{}
	mgr := waitingManager(st, NewHub(), func(d *Deps) { d.WaitingEnabled = func(context.Context) bool { return false } })
	err := mgr.Notify(context.Background(), Intent{
		Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1", ProjectID: "mer",
		SessionDisplayName: "Orchestrator", SourceKey: "orchestrator_waiting:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := st.rows[0]
	if got.Type != domain.NotificationNeedsInput || got.Title != "Orchestrator needs your input" || got.SourceKey != "" {
		t.Fatalf("stored = %+v", got)
	}
}

// A reminder is a published event about an existing row: it carries the row but
// stores nothing.
func TestRemindPublishesWithoutStoring(t *testing.T) {
	st := &fakeStore{}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	mgr := waitingManager(st, hub, nil)
	rec := domain.NotificationRecord{ID: "ntf_9", SessionID: "mer-orchestrator-1", ProjectID: "mer", Type: domain.NotificationOrchestratorWaiting}

	if err := mgr.Remind(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if len(st.rows) != 0 {
		t.Fatalf("a reminder stored rows: %+v", st.rows)
	}
	select {
	case got := <-ch:
		if got.Kind != domain.NotificationReminder || got.Record.ID != "ntf_9" {
			t.Fatalf("published = %+v", got)
		}
	default:
		t.Fatal("expected a reminder event")
	}
	var nilMgr *Manager
	if err := nilMgr.Remind(context.Background(), rec); err != nil {
		t.Fatalf("nil manager: %v", err)
	}
}

func TestOrchestratorWaitingResolves(t *testing.T) {
	if !domain.NotificationOrchestratorWaiting.NeedsResolution() || !domain.NotificationOrchestratorWaiting.Valid() {
		t.Fatal("orchestrator_waiting is a valid kind that stays open until resolved")
	}
	st := &fakeStore{}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	mgr := waitingManager(st, hub, nil)
	if err := mgr.Notify(context.Background(), Intent{Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1", ProjectID: "mer"}); err != nil {
		t.Fatal(err)
	}
	<-ch
	if err := mgr.Resolve(context.Background(), Resolution{Type: domain.NotificationOrchestratorWaiting, SessionID: "mer-orchestrator-1"}); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got.Kind != domain.NotificationResolved || got.Record.Resolved() == false {
		t.Fatalf("published = %+v", got)
	}
}
