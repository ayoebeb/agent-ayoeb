package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

func autopilotRun(id string, sess domain.SessionRecord, at time.Time) domain.AutopilotRun {
	return domain.AutopilotRun{
		ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, Task: "Make the importer faster",
		State: domain.AutopilotRunning, MaxIterations: 30, StartedAt: at, UpdatedAt: at,
	}
}

func TestAutopilot_OneOpenRunPerSessionAndHistoryAfterIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)

	first, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_1", sess, now))
	if err != nil || first.State != domain.AutopilotRunning || first.Task != "Make the importer faster" {
		t.Fatalf("create = %+v err=%v", first, err)
	}
	if _, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_2", sess, now.Add(time.Second))); !errors.Is(err, store.ErrAutopilotRunOpen) {
		t.Fatalf("second open run err = %v, want ErrAutopilotRunOpen", err)
	}

	// A parked run is still open: the user has not decided yet.
	first.State, first.EndReason, first.Iteration, first.Stagnant = domain.AutopilotParked, domain.AutopilotEndStuck, 7, 3
	first.LastNudgeAt = now.Add(time.Minute)
	first.UpdatedAt = now.Add(time.Minute)
	if _, err := s.UpdateAutopilotRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_2", sess, now.Add(2*time.Second))); !errors.Is(err, store.ErrAutopilotRunOpen) {
		t.Fatalf("a parked run must still block a new run, err = %v", err)
	}
	open, ok, err := s.GetOpenAutopilotRunForSession(ctx, sess.ID)
	if err != nil || !ok || open.ID != "ap_1" || open.Iteration != 7 || open.Stagnant != 3 || open.LastNudgeAt.IsZero() {
		t.Fatalf("open = %+v ok=%v err=%v", open, ok, err)
	}

	// Finishing it frees the session for a new run and keeps the history.
	open.State, open.EndReason, open.EndedAt = domain.AutopilotDone, domain.AutopilotEndGoalDone, now.Add(time.Hour)
	if _, err := s.UpdateAutopilotRun(ctx, open); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetOpenAutopilotRunForSession(ctx, sess.ID); ok {
		t.Fatal("a done run must not be open")
	}
	if _, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_2", sess, now.Add(2*time.Hour))); err != nil {
		t.Fatalf("new run after the first finished: %v", err)
	}
	latest, ok, err := s.GetLatestAutopilotRunForSession(ctx, sess.ID)
	if err != nil || !ok || latest.ID != "ap_2" {
		t.Fatalf("latest = %+v ok=%v err=%v, want ap_2", latest, ok, err)
	}
	openRuns, err := s.ListOpenAutopilotRuns(ctx)
	if err != nil || len(openRuns) != 1 || openRuns[0].ID != "ap_2" {
		t.Fatalf("open runs = %+v err=%v", openRuns, err)
	}
}

func TestAutopilot_ActiveRunsForProjectExcludeParkedRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mer := seedOrchestrator(t, s, "mer")
	oth := seedOrchestrator(t, s, "oth")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_mer", mer, now)); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_oth", oth, now))
	if err != nil {
		t.Fatal(err)
	}
	other.State = domain.AutopilotParked
	if _, err := s.UpdateAutopilotRun(ctx, other); err != nil {
		t.Fatal(err)
	}

	merRuns, err := s.ListActiveAutopilotRunsForProject(ctx, mer.ProjectID)
	if err != nil || len(merRuns) != 1 || merRuns[0].ID != "ap_mer" {
		t.Fatalf("mer = %+v err=%v", merRuns, err)
	}
	othRuns, err := s.ListActiveAutopilotRunsForProject(ctx, oth.ProjectID)
	if err != nil || len(othRuns) != 0 {
		t.Fatalf("a parked run answers nothing; oth = %+v err=%v", othRuns, err)
	}
}

func TestAutopilot_EventsNewestFirstAndParkedItemsResolveOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.CreateAutopilotRun(ctx, autopilotRun("ap_1", sess, now)); err != nil {
		t.Fatal(err)
	}
	add := func(id string, kind domain.AutopilotEventKind, at time.Time, summary string) {
		t.Helper()
		if _, err := s.CreateAutopilotEvent(ctx, domain.AutopilotEvent{
			ID: id, RunID: "ap_1", SessionID: sess.ID, Kind: kind, Iteration: 1, Summary: summary, CreatedAt: at,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	add("ev_1", domain.AutopilotEventStarted, now, "Started")
	add("ev_2", domain.AutopilotEventParked, now.Add(time.Minute), "Upgrade the paid plan")
	add("ev_3", domain.AutopilotEventDecision, now.Add(2*time.Minute), "Chose the smaller refactor")

	events, err := s.ListAutopilotEventsForRun(ctx, "ap_1", 2)
	if err != nil || len(events) != 2 || events[0].ID != "ev_3" || events[1].ID != "ev_2" {
		t.Fatalf("events newest first, capped at 2 = %+v err=%v", events, err)
	}

	if n, err := s.CountOpenParkedAutopilotEventsForSession(ctx, sess.ID); err != nil || n != 1 {
		t.Fatalf("open parked = %d err=%v, want 1", n, err)
	}
	if ev, ok, err := s.ResolveAutopilotEvent(ctx, "ev_2", now.Add(time.Hour)); err != nil || !ok || ev.ResolvedAt.IsZero() {
		t.Fatalf("resolve = %+v ok=%v err=%v", ev, ok, err)
	}
	if _, ok, err := s.ResolveAutopilotEvent(ctx, "ev_2", now.Add(2*time.Hour)); err != nil || ok {
		t.Fatalf("a handled item must not resolve twice: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.ResolveAutopilotEvent(ctx, "ev_3", now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("only a parked item resolves: ok=%v err=%v", ok, err)
	}
	if n, _ := s.CountOpenParkedAutopilotEventsForSession(ctx, sess.ID); n != 0 {
		t.Fatalf("open parked after resolve = %d, want 0", n)
	}
}

func TestAutopilot_ParkedNotificationTypeIsStoredAndListedAsUnresolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := seedOrchestrator(t, s, "mer")
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.NotificationRecord{
		ID: "ntf_p1", SessionID: sess.ID, ProjectID: sess.ProjectID, Type: domain.NotificationAutopilotParked,
		Title: "Mercury: Autopilot parked an item for you", Body: "Upgrade the paid plan", Status: domain.NotificationUnread,
		CreatedAt: now, SourceKey: domain.AutopilotParkedSourceKeyPrefix + "ev_2",
	}
	if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
		t.Fatalf("an autopilot_parked row must be accepted: inserted=%v err=%v", inserted, err)
	}
	// The same parked item can never notify twice.
	rec.ID = "ntf_p2"
	if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || inserted {
		t.Fatalf("replayed item inserted=%v err=%v, want false nil", inserted, err)
	}
	unresolved, err := s.ListNotifications(ctx, domain.NotificationListUnresolved, time.Time{}, "", 10)
	if err != nil || len(unresolved) != 1 || unresolved[0].Type != domain.NotificationAutopilotParked {
		t.Fatalf("unresolved = %+v err=%v", unresolved, err)
	}
}

func TestAutopilot_MaxIterationsSettingDefaultsTo30(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if n, err := s.AutopilotMaxIterations(ctx); err != nil || n != domain.AutopilotDefaultMaxIterations {
		t.Fatalf("default = %d err=%v, want %d", n, err, domain.AutopilotDefaultMaxIterations)
	}
	if err := s.SetAutopilotMaxIterations(ctx, 12, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.AutopilotMaxIterations(ctx); err != nil || n != 12 {
		t.Fatalf("after set = %d err=%v, want 12", n, err)
	}
	if err := s.SetAutopilotMaxIterations(ctx, 0, time.Now().UTC()); err == nil {
		t.Fatal("the check constraint must refuse a cap below 1")
	}
}
