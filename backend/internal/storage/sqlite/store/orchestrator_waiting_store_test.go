package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func seedOrchestrator(t *testing.T, s *sqlite.Store, project string) domain.SessionRecord {
	t.Helper()
	seedProject(t, s, project)
	rec := sampleRecord(project)
	rec.Kind = domain.KindOrchestrator
	sess, err := s.CreateSession(context.Background(), rec)
	if err != nil {
		t.Fatalf("create orchestrator for %s: %v", project, err)
	}
	return sess
}

func waitingRecord(id string, sess domain.SessionRecord, key, question string, at time.Time) domain.NotificationRecord {
	return domain.NotificationRecord{
		ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, Type: domain.NotificationOrchestratorWaiting,
		Title: "Mercury: orchestrator is waiting for you", Body: question, Status: domain.NotificationUnread, CreatedAt: at, SourceKey: key,
	}
}

// The migration widened the type check and the settings default to on with a
// ten-minute reminder.
func TestOrchestratorWaiting_MigrationAndSettingsDefaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_1", sess, "orchestrator_waiting:a", "Ship it?", now)); err != nil || !inserted {
		t.Fatalf("an orchestrator_waiting row must be accepted: inserted=%v err=%v", inserted, err)
	}

	settings, err := s.GetAppSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.OrchestratorWaitingEnabled || settings.OrchestratorWaitingRenotifyMinutes != 10 {
		t.Fatalf("defaults = enabled %v, %d minutes; want on and 10", settings.OrchestratorWaitingEnabled, settings.OrchestratorWaitingRenotifyMinutes)
	}
	if err := s.SetOrchestratorWaiting(ctx, false, 0, now); err != nil {
		t.Fatal(err)
	}
	settings, err = s.GetAppSettings(ctx)
	if err != nil || settings.OrchestratorWaitingEnabled || settings.OrchestratorWaitingRenotifyMinutes != 0 {
		t.Fatalf("after set = %+v err=%v", settings, err)
	}
}

// The ordinary open-row dedupe also counts unread rows, so a wait the user
// answered but never opened would swallow the next question. Each declared wait
// carries a key of its own, which is what lets the next one through.
func TestOrchestratorWaiting_ResolvedUnreadRowDoesNotSwallowTheNextWait(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)

	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_1", sess, "orchestrator_waiting:a", "Ship it?", now)); err != nil || !inserted {
		t.Fatalf("first: inserted=%v err=%v", inserted, err)
	}
	resolved, err := s.ResolveSessionNotifications(ctx, sess.ID, domain.NotificationOrchestratorWaiting, now.Add(time.Minute))
	if err != nil || len(resolved) != 1 || !resolved[0].Resolved() {
		t.Fatalf("resolve = %+v err=%v", resolved, err)
	}
	// The row is resolved but still unread: the user never opened the panel.
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_2", sess, "orchestrator_waiting:b", "Ship it now?", now.Add(2*time.Minute))); err != nil || !inserted {
		t.Fatalf("next wait was swallowed: inserted=%v err=%v", inserted, err)
	}
	// The same declaration retried is still idempotent.
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_3", sess, "orchestrator_waiting:b", "Ship it now?", now.Add(3*time.Minute))); err != nil || inserted {
		t.Fatalf("replayed declaration inserted=%v err=%v, want false nil", inserted, err)
	}
}

func TestOrchestratorWaiting_ListsOpenWaitsAcrossProjectsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mer := seedOrchestrator(t, s, "mer")
	oth := seedOrchestrator(t, s, "oth")
	now := time.Now().UTC().Truncate(time.Second)
	for _, rec := range []domain.NotificationRecord{
		waitingRecord("ntf_1", mer, "orchestrator_waiting:a", "Ship it?", now),
		waitingRecord("ntf_2", oth, "orchestrator_waiting:b", "Which branch?", now.Add(time.Minute)),
	} {
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("create %s: inserted=%v err=%v", rec.ID, inserted, err)
		}
	}
	open, err := s.ListOpenOrchestratorWaiting(ctx)
	if err != nil || len(open) != 2 || open[0].ID != "ntf_2" || open[1].ID != "ntf_1" {
		t.Fatalf("open = %+v err=%v", open, err)
	}
	// The ordinary unresolved list now includes them too, for the notification panel.
	unresolved, err := s.ListNotifications(ctx, domain.NotificationListUnresolved, time.Time{}, "", 10)
	if err != nil || len(unresolved) != 2 {
		t.Fatalf("unresolved = %+v err=%v", unresolved, err)
	}
	if count, err := s.CountUnresolvedNotifications(ctx); err != nil || count != 2 {
		t.Fatalf("unresolved count = %d err=%v", count, err)
	}
}

// Clearing the notification panel acknowledges the alert, not the question: the
// wait stays listed for the sidebar marker, and resolving it later is still
// reported so clients can drop the marker.
func TestOrchestratorWaiting_SurvivesClearingNotificationHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_1", sess, "orchestrator_waiting:a", "Ship it?", now)); err != nil || !inserted {
		t.Fatalf("create: inserted=%v err=%v", inserted, err)
	}
	if count, err := s.ClearAllNotifications(ctx); err != nil || count != 1 {
		t.Fatalf("clear all = %d err=%v", count, err)
	}
	if visible, err := s.ListNotifications(ctx, domain.NotificationListAll, time.Time{}, "", 10); err != nil || len(visible) != 0 {
		t.Fatalf("history after clear = %+v err=%v", visible, err)
	}
	open, err := s.ListOpenOrchestratorWaiting(ctx)
	if err != nil || len(open) != 1 || open[0].ID != "ntf_1" {
		t.Fatalf("the cleared wait must stay open: %+v err=%v", open, err)
	}

	resolved, err := s.ResolveSessionNotifications(ctx, sess.ID, domain.NotificationOrchestratorWaiting, now.Add(time.Minute))
	if err != nil || len(resolved) != 1 || resolved[0].ID != "ntf_1" {
		t.Fatalf("resolving a dismissed wait must still be reported: %+v err=%v", resolved, err)
	}
	if open, err := s.ListOpenOrchestratorWaiting(ctx); err != nil || len(open) != 0 {
		t.Fatalf("open after resolve = %+v err=%v", open, err)
	}
}

func TestOrchestratorWaiting_ReminderIsClaimedOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_1", sess, "orchestrator_waiting:a", "Ship it?", now)); err != nil || !inserted {
		t.Fatalf("create: inserted=%v err=%v", inserted, err)
	}

	if due, err := s.ListDueOrchestratorWaitingReminders(ctx, now.Add(-time.Second)); err != nil || len(due) != 0 {
		t.Fatalf("not yet due: %+v err=%v", due, err)
	}
	due, err := s.ListDueOrchestratorWaitingReminders(ctx, now)
	if err != nil || len(due) != 1 || due[0].ID != "ntf_1" || !due[0].RemindedAt.IsZero() {
		t.Fatalf("due = %+v err=%v", due, err)
	}

	claimed, ok, err := s.MarkNotificationReminded(ctx, "ntf_1", now.Add(time.Minute))
	if err != nil || !ok || claimed.RemindedAt.IsZero() {
		t.Fatalf("first claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := s.MarkNotificationReminded(ctx, "ntf_1", now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("a second claim must lose: ok=%v err=%v", ok, err)
	}
	if due, err := s.ListDueOrchestratorWaitingReminders(ctx, now.Add(time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("a reminded row is not due again: %+v err=%v", due, err)
	}

	// A resolved row cannot be reminded.
	if _, inserted, err := s.CreateNotification(ctx, waitingRecord("ntf_2", sess, "orchestrator_waiting:b", "Again?", now.Add(time.Hour))); err != nil || !inserted {
		t.Fatalf("second wait: inserted=%v err=%v", inserted, err)
	}
	if _, err := s.ResolveSessionNotifications(ctx, sess.ID, domain.NotificationOrchestratorWaiting, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.MarkNotificationReminded(ctx, "ntf_2", now.Add(3*time.Hour)); err != nil || ok {
		t.Fatalf("a resolved wait was reminded: ok=%v err=%v", ok, err)
	}
}

// An orchestrator that ended while the daemon was down never reached lifecycle,
// so startup reconciliation closes its wait.
func TestOrchestratorWaiting_ReconcileClosesWaitsOfEndedOrchestrators(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	live := seedOrchestrator(t, s, "mer")
	ended := seedOrchestrator(t, s, "oth")
	now := time.Now().UTC().Truncate(time.Second)
	for _, rec := range []domain.NotificationRecord{
		waitingRecord("ntf_live", live, "orchestrator_waiting:a", "Ship it?", now),
		waitingRecord("ntf_ended", ended, "orchestrator_waiting:b", "Which branch?", now),
	} {
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("create %s: inserted=%v err=%v", rec.ID, inserted, err)
		}
	}
	rec, ok, err := s.GetSession(ctx, ended.ID)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	rec.IsTerminated = true
	if err := s.UpdateSession(ctx, rec); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	resolved, err := s.ReconcileResolvedNotifications(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].ID != "ntf_ended" {
		t.Fatalf("resolved = %+v, want only the ended orchestrator's wait", resolved)
	}
	open, err := s.ListOpenOrchestratorWaiting(ctx)
	if err != nil || len(open) != 1 || open[0].ID != "ntf_live" {
		t.Fatalf("open = %+v err=%v", open, err)
	}
}
