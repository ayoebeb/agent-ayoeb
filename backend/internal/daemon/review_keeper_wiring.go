package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/workerkeeper"
)

// reviewKeeper is what the review service offers for keeping reviewers running,
// the reviewer counterpart of the worker keeper.
type reviewKeeper interface {
	RecoverRunningReviews(ctx context.Context, stagger time.Duration) error
	SweepStalledReviews(ctx context.Context) ([]reviewcore.StalledReview, error)
}

type reviewReports interface {
	Create(context.Context, reportsvc.CreateInput) (domain.ReportRecord, error)
}

// runReviewKeeper relaunches the reviewers a restart took down once ready
// closes, then watches for silent ones until ctx ends. A reviewer the watchdog
// gives up on is reported once to the worker's orchestrator.
func runReviewKeeper(ctx context.Context, ready <-chan struct{}, every time.Duration, reviews reviewKeeper, reports reviewReports, log *slog.Logger) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	if err := reviews.RecoverRunningReviews(ctx, workerkeeper.DefaultStagger); err != nil && ctx.Err() == nil {
		log.Warn("review restart recovery failed", "err", err)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stalled, err := reviews.SweepStalledReviews(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("reviewer watchdog failed", "err", err)
		}
		for _, review := range stalled {
			if _, err := reports.Create(ctx, reportsvc.CreateInput{
				SessionID: review.WorkerID, State: domain.ReportCheckpoint, Note: review.Note, Immediate: true,
			}); err != nil {
				log.Warn("reviewer watchdog report failed", "session", review.WorkerID, "err", err)
			}
		}
	}
}
