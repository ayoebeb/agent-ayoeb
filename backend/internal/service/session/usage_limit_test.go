package session

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

type limitedStore struct {
	*fakeStore
	turns []domain.LimitStopTurn
}

func (s limitedStore) ListLatestFailedTurns(context.Context, []domain.SessionID) ([]domain.LimitStopTurn, error) {
	return s.turns, nil
}

// A session whose latest turn stopped on the usage limit is read as that: not
// idle, with the reset time and what AO will do, for every client of the API.
func TestSessionReadModelShowsUsageLimit(t *testing.T) {
	failed := time.Date(2026, 11, 16, 14, 20, 0, 0, time.UTC)
	now := failed.Add(time.Minute)
	turn := domain.LimitStopTurn{
		SessionID: "s", TurnID: "t1", CompletedAt: &failed, RequestedAt: failed.Add(-time.Hour),
		ErrorMessage: "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)",
	}
	svc := &Service{store: limitedStore{fakeStore: &fakeStore{}, turns: []domain.LimitStopTurn{turn}}, clock: func() time.Time { return now }}
	rec := domain.SessionRecord{ID: "s", Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: failed}, FirstSignalAt: failed}

	limits := svc.usageLimits(context.Background(), []domain.SessionID{"s"})
	got, err := svc.toSessionWithFacts(context.Background(), rec, nil, nil, false, limits["s"], domain.SessionCleanupRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusUsageLimit || got.DisplayStatus != contract.DisplayUsageLimit {
		t.Fatalf("status=%q display=%q, want usage_limit / Usage limit", got.Status, got.DisplayStatus)
	}
	if got.UsageLimit == nil || got.UsageLimit.Window != "5-hour" || got.UsageLimit.Resume != domain.UsageLimitResumeScheduled || got.UsageLimit.ResetsAt == nil {
		t.Fatalf("usageLimit = %+v", got.UsageLimit)
	}
	if want := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC); !got.UsageLimit.ResetsAt.Equal(want) {
		t.Fatalf("resetsAt = %v, want %v", got.UsageLimit.ResetsAt, want)
	}

	// Without a limit stop the same idle session is plain idle.
	plain, err := svc.toSessionWithFacts(context.Background(), rec, nil, nil, false, nil, domain.SessionCleanupRecord{})
	if err != nil || plain.Status != domain.StatusIdle || plain.UsageLimit != nil {
		t.Fatalf("plain idle: status=%q usageLimit=%+v err=%v", plain.Status, plain.UsageLimit, err)
	}

	// An archived session is just archived, and a running one just running.
	archived := rec
	archived.IsTerminated = true
	if s, _ := svc.toSessionWithFacts(context.Background(), archived, nil, nil, false, limits["s"], domain.SessionCleanupRecord{}); s.Status != domain.StatusTerminated || s.UsageLimit != nil {
		t.Fatalf("archived: status=%q usageLimit=%+v", s.Status, s.UsageLimit)
	}
	running := rec
	running.Activity.State = domain.ActivityActive
	if s, _ := svc.toSessionWithFacts(context.Background(), running, nil, nil, false, limits["s"], domain.SessionCleanupRecord{}); s.Status != domain.StatusWorking {
		t.Fatalf("running: status=%q", s.Status)
	}
}

// A store that cannot read failed turns, or a failed turn that is not a usage
// limit, leaves the board unlabelled instead of failing it.
func TestUsageLimitsIgnoresOtherFailures(t *testing.T) {
	failed := time.Date(2026, 11, 16, 14, 20, 0, 0, time.UTC)
	svc := &Service{store: limitedStore{fakeStore: &fakeStore{}, turns: []domain.LimitStopTurn{
		{SessionID: "s", TurnID: "t1", CompletedAt: &failed, ErrorMessage: "controller ended before the turn completed"},
	}}, clock: func() time.Time { return failed }}
	if got := svc.usageLimits(context.Background(), []domain.SessionID{"s"}); len(got) != 0 {
		t.Fatalf("a crash read as a usage limit: %+v", got)
	}
	if got := (&Service{store: &fakeStore{}}).usageLimits(context.Background(), []domain.SessionID{"s"}); got != nil {
		t.Fatalf("store without the reader: %+v", got)
	}
}
