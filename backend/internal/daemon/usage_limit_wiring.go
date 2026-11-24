package daemon

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
	reportsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/report"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/usagelimit"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// usageLimitReporter tells a worker's orchestrator that the worker stopped on
// the provider's usage limit, as a report delivered at once. Without it the
// orchestrator sees only an idle worker and can conclude it never got its task.
type usageLimitReporter struct{ reports *reportsvc.Service }

func (r usageLimitReporter) ReportUsageLimit(ctx context.Context, worker domain.SessionID, note string) error {
	_, err := r.reports.Create(ctx, reportsvc.CreateInput{
		SessionID: worker, State: domain.ReportCheckpoint, Note: note, Immediate: true,
	})
	return err
}

// usageLimitSender sends the "continue" message and translates a session that
// can no longer receive it, so the watcher stops trying instead of retrying.
type usageLimitSender struct{ sessions reportSemanticSession }

func (s usageLimitSender) SendSemantic(ctx context.Context, id domain.SessionID, message, key string) error {
	err := s.sessions.SendSemantic(ctx, id, message, key)
	if errors.Is(err, sessionmanager.ErrNotFound) || errors.Is(err, sessionmanager.ErrTerminated) {
		return errors.Join(usagelimit.ErrSessionGone, err)
	}
	return err
}

// usageLimitMover moves a limit-stopped session to another account through the
// session manager's durable handoff, waiting for any turn in progress.
type usageLimitMover struct {
	move func(context.Context, domain.SessionID, string, domain.SessionInterfaceTransitionPolicy) (domain.SessionInterfaceTransition, error)
}

func (m usageLimitMover) MoveToAccount(ctx context.Context, id domain.SessionID, accountID string) error {
	_, err := m.move(ctx, id, accountID, domain.SessionInterfaceTransitionDrain)
	return err
}

// usageLimitAccounts names the login a session ran on; a missing account
// service reads as the computer's own login.
type usageLimitAccounts struct {
	lookup         func(context.Context, domain.SessionID, domain.AgentHarness) (string, error)
	defaultAccount func(context.Context, domain.AgentHarness) (string, error)
}

func (a usageLimitAccounts) SessionAccountID(ctx context.Context, id domain.SessionID, harness domain.AgentHarness) (string, error) {
	if a.lookup == nil {
		return "", nil
	}
	return a.lookup(ctx, id, harness)
}

// reviewerLimitControl is what the review service offers the usage-limit watcher
// for a reviewer whose Chat stopped on the limit.
type reviewerLimitControl interface {
	PauseReviewerForUsageLimit(ctx context.Context, worker domain.SessionID, reviewID string) error
	ReleaseReviewerUsageLimitPause(ctx context.Context, worker domain.SessionID, reviewID, reason string) error
	ResumeReviewerAfterUsageLimit(ctx context.Context, worker domain.SessionID, reviewID string) error
	MoveReviewerToAccount(ctx context.Context, worker domain.SessionID, reviewID, accountID string) error
	SetReviewerAccountEnv(resolve func(ctx context.Context, worker domain.SessionID, accountID string) (map[string]string, error))
}

// usageLimitReviewers hands the watcher's reviewer actions to the review
// service, translating a reviewer that can no longer continue so the watcher
// stops trying.
type usageLimitReviewers struct{ reviews reviewerLimitControl }

func (r usageLimitReviewers) Pause(ctx context.Context, worker domain.SessionID, reviewID string) error {
	return r.reviews.PauseReviewerForUsageLimit(ctx, worker, reviewID)
}

func (r usageLimitReviewers) Release(ctx context.Context, worker domain.SessionID, reviewID, reason string) error {
	return r.reviews.ReleaseReviewerUsageLimitPause(ctx, worker, reviewID, reason)
}

func (r usageLimitReviewers) Resume(ctx context.Context, worker domain.SessionID, reviewID string) error {
	err := r.reviews.ResumeReviewerAfterUsageLimit(ctx, worker, reviewID)
	if errors.Is(err, reviewcore.ErrReviewerGone) {
		return errors.Join(usagelimit.ErrSessionGone, err)
	}
	return err
}

func (r usageLimitReviewers) Move(ctx context.Context, worker domain.SessionID, reviewID, accountID string) error {
	return r.reviews.MoveReviewerToAccount(ctx, worker, reviewID, accountID)
}

// ReviewerAccountID is the login a reviewer runs on: the provider's default.
func (a usageLimitAccounts) ReviewerAccountID(ctx context.Context, harness domain.AgentHarness) (string, error) {
	if a.defaultAccount == nil {
		return "", nil
	}
	return a.defaultAccount(ctx, harness)
}
