package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const storeLimitMessage = "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)"

// The latest failed turn is what makes a session read as limit-stopped, and a
// newer turn of any kind ends it.
func TestListLatestFailedTurnsFollowsTheLatestTurn(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	appendBranchPrompt(t, s, session, conversation, "generation-root", "limited", "finish this task")
	if err := s.SettleTurnByID(ctx, "turn-limited", domain.TurnStateFailed, storeLimitMessage, testNow); err != nil {
		t.Fatal(err)
	}

	turns, err := s.ListLatestFailedTurns(ctx, []domain.SessionID{session.ID})
	if err != nil || len(turns) != 1 || turns[0].TurnID != "turn-limited" || turns[0].ErrorMessage != storeLimitMessage {
		t.Fatalf("turns = %+v, err = %v", turns, err)
	}
	limit := domain.BuildSessionUsageLimit(turns[0], testNow.Add(time.Minute))
	if limit == nil || limit.Window != "5-hour" || limit.ResetsAt == nil || limit.Resume != domain.UsageLimitResumeScheduled {
		t.Fatalf("limit = %+v", limit)
	}
	// 6pm Amsterdam on 9 August is 16:00 UTC.
	if want := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC); !limit.ResetsAt.Equal(want) {
		t.Fatalf("resetsAt = %v, want %v", limit.ResetsAt, want)
	}

	// The person sends something new: the failed turn is no longer the latest.
	appendBranchPrompt(t, s, session, conversation, "generation-root", "next", "carry on")
	turns, err = s.ListLatestFailedTurns(ctx, []domain.SessionID{session.ID})
	if err != nil || len(turns) != 0 {
		t.Fatalf("after a newer turn: turns = %+v, err = %v", turns, err)
	}
	if got, err := s.ListLatestFailedTurns(ctx, nil); err != nil || got != nil {
		t.Fatalf("no ids: %+v, %v", got, err)
	}
}

// A turn that failed for another reason is returned (the caller parses the
// message), but it is not a usage limit.
func TestListLatestFailedTurnsLeavesParsingToTheCaller(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	appendBranchPrompt(t, s, session, conversation, "generation-root", "crashed", "finish this task")
	if err := s.SettleTurnByID(ctx, "turn-crashed", domain.TurnStateFailed, "controller ended before the turn completed", testNow); err != nil {
		t.Fatal(err)
	}
	turns, err := s.ListLatestFailedTurns(ctx, []domain.SessionID{session.ID})
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns = %+v, err = %v", turns, err)
	}
	if domain.BuildSessionUsageLimit(turns[0], testNow) != nil {
		t.Fatal("a crash read as a usage limit")
	}
}

// The watcher's sweep sees a failed latest turn once, then only while its stop
// is still pending; the stop row carries what AO did about it.
func TestLimitStopCandidatesAndStopLifecycle(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	appendBranchPrompt(t, s, session, conversation, "generation-root", "limited", "finish this task")
	if err := s.SettleTurnByID(ctx, "turn-limited", domain.TurnStateFailed, storeLimitMessage, testNow); err != nil {
		t.Fatal(err)
	}

	candidates, err := s.ListLimitStopCandidates(ctx, testNow.Add(-time.Hour))
	if err != nil || len(candidates) != 1 || candidates[0].ResumeState != "" || candidates[0].SessionID != session.ID {
		t.Fatalf("candidates = %+v, err = %v", candidates, err)
	}
	if old, err := s.ListLimitStopCandidates(ctx, testNow.Add(time.Hour)); err != nil || len(old) != 0 {
		t.Fatalf("a turn older than the cutoff was listed: %+v, %v", old, err)
	}

	reset := time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)
	stop := domain.UsageLimitStop{
		TurnID: "turn-limited", SessionID: session.ID, ProjectID: "branches", AccountKey: "acct", WindowKey: "acct|w",
		WindowLabel: "5-hour", ResetsAt: &reset, DetectedAt: testNow,
	}
	if inserted, err := s.InsertUsageLimitStop(ctx, stop); err != nil || !inserted {
		t.Fatalf("insert: %v, %v", inserted, err)
	}
	if inserted, err := s.InsertUsageLimitStop(ctx, stop); err != nil || inserted {
		t.Fatalf("a second insert must be a no-op: %v, %v", inserted, err)
	}
	candidates, err = s.ListLimitStopCandidates(ctx, testNow.Add(-time.Hour))
	if err != nil || len(candidates) != 1 || candidates[0].ResumeState != domain.UsageLimitStopPending || candidates[0].StopResetsAt == nil || !candidates[0].StopResetsAt.Equal(reset) {
		t.Fatalf("pending candidate = %+v, err = %v", candidates, err)
	}
	pending, err := s.ListPendingUsageLimitStops(ctx)
	if err != nil || len(pending) != 1 || pending[0].AccountKey != "acct" {
		t.Fatalf("pending = %+v, err = %v", pending, err)
	}

	// Skipping it takes it out of the sweep and tells readers why.
	if changed, err := s.SkipUsageLimitStop(ctx, "turn-limited", domain.UsageLimitOffCancelled); err != nil || !changed {
		t.Fatalf("skip: %v, %v", changed, err)
	}
	if changed, err := s.SkipUsageLimitStop(ctx, "turn-limited", domain.UsageLimitOffRepeated); err != nil || changed {
		t.Fatalf("a settled stop must not be skipped again: %v, %v", changed, err)
	}
	if candidates, err = s.ListLimitStopCandidates(ctx, testNow.Add(-time.Hour)); err != nil || len(candidates) != 0 {
		t.Fatalf("skipped stop still a candidate: %+v, %v", candidates, err)
	}
	turns, err := s.ListLatestFailedTurns(ctx, []domain.SessionID{session.ID})
	if err != nil || len(turns) != 1 || turns[0].SkipReason != domain.UsageLimitOffCancelled {
		t.Fatalf("turns = %+v, err = %v", turns, err)
	}
	limit := domain.BuildSessionUsageLimit(turns[0], testNow)
	if limit == nil || limit.Resume != domain.UsageLimitResumeOff || limit.ResumeOffReason != domain.UsageLimitOffCancelled {
		t.Fatalf("limit = %+v", limit)
	}
}

// Turning the setting off parks the pending stops and turning it on reopens
// exactly those, never one skipped for another reason.
func TestUsageLimitStopsParkedAndReopenedWithTheSetting(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	for _, id := range []string{"a", "b"} {
		appendBranchPrompt(t, s, session, conversation, "generation-root", id, "task "+id)
		if err := s.SettleTurnByID(ctx, "turn-"+id, domain.TurnStateFailed, storeLimitMessage, testNow); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InsertUsageLimitStop(ctx, domain.UsageLimitStop{TurnID: "turn-" + id, SessionID: session.ID, DetectedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SkipUsageLimitStop(ctx, "turn-b", domain.UsageLimitOffCancelled); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingUsageLimitStopsDisabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.ListPendingUsageLimitStops(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending while disabled = %+v, %v", pending, err)
	}
	if err := s.SetPendingUsageLimitStopsDisabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingUsageLimitStops(ctx)
	if err != nil || len(pending) != 1 || pending[0].TurnID != "turn-a" {
		t.Fatalf("pending after enabling = %+v, %v (the cancelled one must stay cancelled)", pending, err)
	}
}

// A continued turn counts against the cap while it is queued or running, and
// the stagger reads the time of the last resume.
func TestResumeTracksTheContinuedTurn(t *testing.T) {
	ctx := context.Background()
	s, session, conversation := seededChatConversation(t)
	appendBranchPrompt(t, s, session, conversation, "generation-root", "limited", "finish this task")
	if err := s.SettleTurnByID(ctx, "turn-limited", domain.TurnStateFailed, storeLimitMessage, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertUsageLimitStop(ctx, domain.UsageLimitStop{TurnID: "turn-limited", SessionID: session.ID, DetectedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LastUsageLimitResumeAt(ctx); err != nil || ok {
		t.Fatalf("last resume before any: %v, %v", ok, err)
	}

	// The continue message starts a new turn after the stop was recorded.
	resumedAt := testNow.Add(time.Hour)
	created, err := s.AppendUserMessage(ctx, conversation.ID, session.ID, "generation-root", domain.ConversationMessage{
		ID: "message-continue", Origin: domain.MessageOriginAutomation, Text: "continue", ClientMessageID: "usage-limit-resume:turn-limited",
	}, "turn-continue", resumedAt)
	if err != nil || !created {
		t.Fatalf("append continue: %v, %v", created, err)
	}
	if err := s.MarkUsageLimitStopResumed(ctx, "turn-limited", resumedAt, resumedAt); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountRunningUsageLimitResumes(ctx); err != nil || n != 1 {
		t.Fatalf("running resumes = %d, %v, want 1 while the continued turn is queued", n, err)
	}
	if at, ok, err := s.LastUsageLimitResumeAt(ctx); err != nil || !ok || !at.Equal(resumedAt) {
		t.Fatalf("last resume = %v, %v, %v", at, ok, err)
	}
	if n, err := s.CountRecentUsageLimitResumes(ctx, session.ID, resumedAt.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("recent resumes = %d, %v", n, err)
	}
	if n, err := s.CountRecentUsageLimitResumes(ctx, session.ID, resumedAt.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("recent resumes after the window = %d, %v", n, err)
	}

	// The continued turn finishing frees its slot.
	if err := s.SettleTurnByID(ctx, "turn-continue", domain.TurnStateCompleted, "", resumedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountRunningUsageLimitResumes(ctx); err != nil || n != 0 {
		t.Fatalf("running resumes after it finished = %d, %v", n, err)
	}
}
