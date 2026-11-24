package review

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// PauseReviewerForUsageLimit pauses the running passes of a reviewer whose Chat
// stopped on the provider's usage limit.
func (s *Service) PauseReviewerForUsageLimit(ctx context.Context, worker domain.SessionID, reviewID string) error {
	n, err := s.engine.PauseForUsageLimit(ctx, worker, reviewID)
	if err == nil && n > 0 {
		s.emit(ctx, "ao.reviews.paused_usage_limit", worker, map[string]any{"paused_runs": n})
	}
	return err
}

// ReleaseReviewerUsageLimitPause ends the paused passes of a reviewer that will
// not be continued.
func (s *Service) ReleaseReviewerUsageLimitPause(ctx context.Context, worker domain.SessionID, reviewID, reason string) error {
	return s.engine.ReleaseUsageLimitPause(ctx, worker, reviewID, reason)
}

// ResumeReviewerAfterUsageLimit continues a reviewer's paused passes.
func (s *Service) ResumeReviewerAfterUsageLimit(ctx context.Context, worker domain.SessionID, reviewID string) error {
	return s.engine.ResumeAfterUsageLimit(ctx, worker, reviewID)
}

// MoveReviewerToAccount restarts a reviewer under another account and continues it.
func (s *Service) MoveReviewerToAccount(ctx context.Context, worker domain.SessionID, reviewID, accountID string) error {
	return s.engine.MoveReviewerToAccount(ctx, worker, reviewID, accountID)
}

// SetReviewerAccountEnv binds the resolver that launches a reviewer under an account.
func (s *Service) SetReviewerAccountEnv(resolve func(ctx context.Context, worker domain.SessionID, accountID string) (map[string]string, error)) {
	s.engine.SetAccountEnv(resolve)
}
