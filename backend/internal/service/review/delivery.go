package review

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

// resultRetryWindow bounds how long AO keeps retrying a result its worker does
// not take.
const resultRetryWindow = 24 * time.Hour

// ResultDeliverer sends a recorded review result to its worker. It reports false
// when the worker cannot take the message now.
type ResultDeliverer interface {
	DeliverReviewResult(ctx context.Context, run domain.ReviewRun) (bool, error)
}

// WithResultDeliverer sends every review result to its worker, whether or not
// the review reached the pull request.
func WithResultDeliverer(d ResultDeliverer) Option {
	return func(s *Service) { s.deliverer = d }
}

// deliverResult sends one owed result to its worker and stamps it delivered. It
// returns why the worker did not get it, or "" when it did or none is owed.
func (s *Service) deliverResult(ctx context.Context, run *domain.ReviewRun) string {
	if s.deliverer == nil || run.Status != domain.ReviewRunComplete || !run.Verdict.Valid() || run.DeliveredAt != nil || !run.AutoInjectReview {
		return ""
	}
	sent, err := s.deliverer.DeliverReviewResult(ctx, *run)
	if err != nil {
		return err.Error()
	}
	if !sent {
		return "the worker session is not taking messages (it ended, or is waiting on the user)"
	}
	now := s.clock()
	if _, err := s.store.MarkReviewRunDelivered(ctx, run.ID, now); err != nil {
		return err.Error()
	}
	run.DeliveredAt = &now
	return ""
}

// retryResultDeliveries sends the results still owed to their workers and
// returns one report for each run that fails, once per run.
func (s *Service) retryResultDeliveries(ctx context.Context) ([]reviewcore.StalledReview, error) {
	if s.deliverer == nil {
		return nil, nil
	}
	runs, err := s.store.ListUndeliveredReviewRuns(ctx)
	if err != nil {
		return nil, err
	}
	var reports []reviewcore.StalledReview
	for _, run := range runs {
		if s.clock().Sub(run.CreatedAt) > resultRetryWindow {
			continue
		}
		reason := s.deliverResult(ctx, &run)
		if reason == "" || !s.claimUndeliveredReport(run.ID) {
			continue
		}
		reports = append(reports, reviewcore.StalledReview{
			WorkerID: run.SessionID,
			Note: fmt.Sprintf("AO review result NOT delivered to worker %s: %s on %s (run %s). Reason: %s. AO keeps retrying for %.0f hours; read it with `ao review ls %s --json`.",
				run.SessionID, run.Verdict, run.PRURL, run.ID, reason, resultRetryWindow.Hours(), run.SessionID),
		})
	}
	return reports, nil
}

func (s *Service) claimUndeliveredReport(runID string) bool {
	s.reportedMu.Lock()
	defer s.reportedMu.Unlock()
	if s.reportedUndelivered[runID] {
		return false
	}
	if s.reportedUndelivered == nil {
		s.reportedUndelivered = map[string]bool{}
	}
	s.reportedUndelivered[runID] = true
	return true
}
