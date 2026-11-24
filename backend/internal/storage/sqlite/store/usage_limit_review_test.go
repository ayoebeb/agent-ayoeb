package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A reviewer's Chat turn that failed on the limit is a stop candidate of its
// own, recorded under the worker the review is of. Its passes pause and come
// back, and the continued turn is found through the reviewer's conversation.
func TestReviewerLimitStopIsListedPausedAndResumedThroughTheReviewConversation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "review-limit")
	worker, err := s.CreateSession(ctx, sampleRecord("review-limit"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{
		ID: "review-limit-1", SessionID: worker.ID, ProjectID: worker.ProjectID,
		Harness: domain.ReviewerClaudeCode, InterfaceMode: domain.ReviewerInterfaceChat, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	conversation, err := s.CreateReviewConversation(ctx, "review-limit-conversation", review.ID, worker.ProjectID, worker.ID, now)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if claimed, err := s.ClaimReviewChatController(ctx, review.ID, "provider-1", "review-generation", now); err != nil || !claimed {
		t.Fatalf("claim reviewer controller: claimed=%v err=%v", claimed, err)
	}
	if created, err := s.AppendReviewUserMessage(ctx, conversation.ID, worker.ID, review.ID, "review-generation", domain.ConversationMessage{
		ID: "review-message", Text: "Review the change", Origin: domain.MessageOriginDaemon,
	}, "review-turn", now); err != nil || !created {
		t.Fatalf("append review message: created=%v err=%v", created, err)
	}
	if err := s.SettleTurnByID(ctx, "review-turn", domain.TurnStateFailed, storeLimitMessage, now.Add(time.Minute)); err != nil {
		t.Fatalf("settle turn: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-limit-1", ReviewID: review.ID, SessionID: worker.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	candidates, err := s.ListLimitStopCandidates(ctx, now.Add(-time.Hour))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v, err = %v, want the reviewer's failed turn", candidates, err)
	}
	got := candidates[0]
	if got.ReviewID != review.ID || got.SessionID != worker.ID || got.ReviewHarness != domain.ReviewerClaudeCode || got.TurnID != "review-turn" {
		t.Fatalf("candidate = %+v, want it under the worker with the reviewer named", got)
	}

	paused, err := s.PauseRunningReviewRunsByReview(ctx, review.ID, "paused for the limit")
	if err != nil || len(paused) != 1 || paused[0].Status != domain.ReviewRunPaused {
		t.Fatalf("paused = %+v, err = %v, want the run paused", paused, err)
	}
	if running, err := s.ListRunningReviewRunsBySession(ctx, worker.ID); err != nil || len(running) != 1 {
		t.Fatalf("running = %+v, err = %v, want a paused run to still hold its slot", running, err)
	}

	inserted, err := s.InsertUsageLimitStop(ctx, domain.UsageLimitStop{
		TurnID: "review-turn", SessionID: worker.ID, ProjectID: worker.ProjectID, ReviewID: review.ID,
		WindowKey: "acct|reset", DetectedAt: now,
	})
	if err != nil || !inserted {
		t.Fatalf("insert stop: inserted=%v err=%v", inserted, err)
	}
	if again, err := s.ListLimitStopCandidates(ctx, now.Add(-time.Hour)); err != nil || len(again) != 1 || again[0].ResumeState != domain.UsageLimitStopPending || again[0].ReviewID != review.ID {
		t.Fatalf("candidates after recording = %+v, err = %v, want the pending reviewer stop", again, err)
	}

	// The continue message starts a new turn in the reviewer's conversation.
	resumeAt := now.Add(2 * time.Minute)
	if created, err := s.AppendReviewUserMessage(ctx, conversation.ID, worker.ID, review.ID, "review-generation", domain.ConversationMessage{
		ID: "review-message-2", Text: "Review again", Origin: domain.MessageOriginDaemon,
	}, "review-turn-2", resumeAt); err != nil || !created {
		t.Fatalf("append continue message: created=%v err=%v", created, err)
	}
	if n, err := s.ResumePausedReviewRunsByReview(ctx, review.ID); err != nil || n != 1 {
		t.Fatalf("resume paused = %d, %v, want 1", n, err)
	}
	if err := s.MarkUsageLimitStopResumed(ctx, "review-turn", resumeAt, resumeAt.Add(-time.Second)); err != nil {
		t.Fatalf("mark resumed: %v", err)
	}
	if n, err := s.CountRunningUsageLimitResumes(ctx); err != nil || n != 1 {
		t.Fatalf("running resumes = %d, %v, want the reviewer's continued turn counted", n, err)
	}
	if n, err := s.CountRecentUsageLimitResumes(ctx, worker.ID, review.ID, resumeAt.Add(-time.Minute)); err != nil || n != 1 {
		t.Fatalf("recent reviewer resumes = %d, %v, want 1", n, err)
	}
	if n, err := s.CountRecentUsageLimitResumes(ctx, worker.ID, "", resumeAt.Add(-time.Minute)); err != nil || n != 0 {
		t.Fatalf("recent session resumes = %d, %v, want the reviewer's resume kept apart", n, err)
	}
	if again, err := s.ListLimitStopCandidates(ctx, now.Add(-time.Hour)); err != nil || len(again) != 0 {
		t.Fatalf("candidates after the continue = %+v, err = %v, want none: the failed turn is no longer the latest", again, err)
	}
}

func TestFailPausedReviewRunsFreesTheSlot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedProject(t, s, "review-release")
	worker, err := s.CreateSession(ctx, sampleRecord("review-release"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{ID: "review-release-1", SessionID: worker.ID, ProjectID: worker.ProjectID, Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run-release-1", ReviewID: "review-release-1", SessionID: worker.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := s.PauseRunningReviewRunsByReview(ctx, "review-release-1", "paused"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if n, err := s.FailPausedReviewRunsByReview(ctx, "review-release-1", "gone"); err != nil || n != 1 {
		t.Fatalf("fail paused = %d, %v, want 1", n, err)
	}
	if running, err := s.ListRunningReviewRunsBySession(ctx, worker.ID); err != nil || len(running) != 0 {
		t.Fatalf("running = %+v, err = %v, want the slot freed", running, err)
	}
}
