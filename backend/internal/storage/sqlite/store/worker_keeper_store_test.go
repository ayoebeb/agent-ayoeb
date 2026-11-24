package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestAgentStopMarkKeepsTheNewestStop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, ok, err := s.AgentStopMark(ctx, rec.ID); err != nil || ok {
		t.Fatalf("mark before any stop: ok=%v err=%v", ok, err)
	}
	first := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkAgentStop(ctx, rec.ID, first, "exit-agent"); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Hour)
	if err := s.MarkAgentStop(ctx, rec.ID, second, "update"); err != nil {
		t.Fatal(err)
	}
	at, ok, err := s.AgentStopMark(ctx, rec.ID)
	if err != nil || !ok || !at.Equal(second) {
		t.Fatalf("mark = %v ok=%v err=%v, want %v", at, ok, err, second)
	}
}

func TestListWatchedWorkerIDsReturnsLiveExitedAndIdleWorkers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	create := func(mutate func(*domain.SessionRecord)) domain.SessionID {
		rec := sampleRecord("mer")
		mutate(&rec)
		out, err := s.CreateSession(ctx, rec)
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		return out.ID
	}
	exited := create(func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityExited })
	idle := create(func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityIdle })
	create(func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityActive })
	create(func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityExited; r.IsTerminated = true })
	create(func(r *domain.SessionRecord) {
		r.Kind = domain.KindOrchestrator
		r.Activity.State = domain.ActivityExited
	})

	got, err := s.ListWatchedWorkerIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("watched = %v, want the exited and the idle worker only", got)
	}
	seen := map[domain.SessionID]bool{got[0]: true, got[1]: true}
	if !seen[exited] || !seen[idle] {
		t.Fatalf("watched = %v, want %s and %s", got, exited, idle)
	}
}
