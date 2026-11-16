package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// commandDetails reads one activity's stored detail back from a snapshot.
func commandDetails(t *testing.T, snapshotActivities []domain.ConversationActivity, providerItemID string) map[string]any {
	t.Helper()
	for _, item := range snapshotActivities {
		if item.ProviderItemID != providerItemID {
			continue
		}
		var detail map[string]any
		if len(item.Detail) > 0 {
			if err := json.Unmarshal(item.Detail, &detail); err != nil {
				t.Fatalf("decode detail %s: %v", item.Detail, err)
			}
		}
		return detail
	}
	t.Fatalf("no activity with provider item %q", providerItemID)
	return nil
}

// A provider announces a tool call before the approval it may wait on, so the row's
// created_at includes that wait. The store records when the command first began
// running and carries it through every later rewrite of the detail, so how long it
// took is measured from the moment it ran.
func TestUpsertActivityRecordsWhenACommandFirstBeganRunning(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	ctx := context.Background()
	announced := histClock
	approved := announced.Add(10 * time.Minute)
	heartbeat := approved.Add(30 * time.Second)
	finished := approved.Add(45 * time.Second)
	// A number well past what a float keeps exactly: rewriting the detail to add the
	// start must not reformat the rest of it.
	const detail = `{"command":"npm test","outputBytes":12345678901234567}`
	write := func(status domain.ActivityStatus, at time.Time) {
		t.Helper()
		if err := s.UpsertActivity(ctx, conversation, "", domain.ConversationActivity{
			ID: "act-1", Kind: domain.ActivityKindCommand, Status: status, Summary: "npm test",
			ProviderItemID: "exec-1", Detail: []byte(detail),
		}, at); err != nil {
			t.Fatalf("upsert %s: %v", status, err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		return commandDetails(t, snapshot.Activities, "exec-1")
	}

	write(domain.ActivityStatusPending, announced)
	if _, started := read()["startedAt"]; started {
		t.Fatal("a command that is only announced has not started: no startedAt yet")
	}

	write(domain.ActivityStatusRunning, approved)
	got, _ := read()["startedAt"].(string)
	if want := approved.UTC().Format(time.RFC3339Nano); got != want {
		t.Fatalf("startedAt = %q, want %q: when it first began running, not when it was announced", got, want)
	}

	// The provider re-announces a running tool; its detail carries no start.
	write(domain.ActivityStatusRunning, heartbeat)
	write(domain.ActivityStatusCompleted, finished)
	settled := read()
	if got, _ := settled["startedAt"].(string); got != approved.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("startedAt after the heartbeat and settle = %q, want the original start carried through", got)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	for _, item := range snapshot.Activities {
		if item.ProviderItemID == "exec-1" && !strings.Contains(string(item.Detail), `"outputBytes":12345678901234567`) {
			t.Fatalf("stored detail = %s: rewriting it to add the start must not reformat its other fields", item.Detail)
		}
	}
}

func TestUpsertActivityLeavesAnUnobservedCommandStartUnset(t *testing.T) {
	s, _, conversation := conversationFixture(t)
	ctx := context.Background()
	upsert := func(id, item string, kind domain.ActivityKind, status domain.ActivityStatus, at time.Time) {
		t.Helper()
		if err := s.UpsertActivity(ctx, conversation, "", domain.ConversationActivity{
			ID: id, Kind: kind, Status: status, Summary: id, ProviderItemID: item, Detail: []byte(`{"command":"ls"}`),
		}, at); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	// Pending straight to settled: it was never seen running, so there is no honest start.
	upsert("never-ran", "exec-a", domain.ActivityKindCommand, domain.ActivityStatusPending, histClock)
	upsert("never-ran", "exec-a", domain.ActivityKindCommand, domain.ActivityStatusCompleted, histClock.Add(time.Minute))
	// First seen already running: it began then.
	upsert("ran-at-once", "exec-b", domain.ActivityKindCommand, domain.ActivityStatusRunning, histClock.Add(2*time.Minute))
	// Only commands are timed.
	upsert("tool", "exec-c", domain.ActivityKindMCPTool, domain.ActivityStatusRunning, histClock.Add(3*time.Minute))

	snapshot, err := s.LoadConversationSnapshot(ctx, conversation)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if _, started := commandDetails(t, snapshot.Activities, "exec-a")["startedAt"]; started {
		t.Error("a command that went from pending straight to settled must have no startedAt")
	}
	if got, _ := commandDetails(t, snapshot.Activities, "exec-b")["startedAt"].(string); got != histClock.Add(2*time.Minute).UTC().Format(time.RFC3339Nano) {
		t.Errorf("a command first seen running: startedAt = %q, want that moment", got)
	}
	if _, started := commandDetails(t, snapshot.Activities, "exec-c")["startedAt"]; started {
		t.Error("only commands get a startedAt")
	}
}
