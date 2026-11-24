package review

import (
	stdctx "context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrReviewerGone reports that a paused reviewer can no longer continue: its
// worker was terminated or its reviewer was archived or removed. The paused
// passes have been released.
var ErrReviewerGone = errors.New("reviewer can no longer continue its review")

const (
	usageLimitPausedNote   = "paused: the reviewer stopped on the provider usage limit; the review continues by itself after the limit resets"
	usageLimitReleasedNote = "the reviewer stopped on the provider usage limit and could not continue; run `ao review trigger --rerun` to review again"
)

// PauseForUsageLimit pauses the running passes of a reviewer whose Chat turn
// stopped on the provider's usage limit, so they no longer read as running while
// nothing is working on them. It returns how many passes it paused.
func (e *Engine) PauseForUsageLimit(ctx stdctx.Context, workerID domain.SessionID, reviewID string) (int, error) {
	if workerID == "" || reviewID == "" {
		return 0, fmt.Errorf("%w: worker session id and review id are required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	paused, err := e.store.PauseRunningReviewRunsByReview(ctx, reviewID, usageLimitPausedNote)
	return len(paused), err
}

// ReleaseUsageLimitPause ends the paused passes of a reviewer as failed, for a
// reviewer that will not be continued, so they stop holding the review slot.
func (e *Engine) ReleaseUsageLimitPause(ctx stdctx.Context, workerID domain.SessionID, reviewID, reason string) error {
	if workerID == "" || reviewID == "" {
		return fmt.Errorf("%w: worker session id and review id are required", ErrInvalid)
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	return e.releasePausedLocked(ctx, reviewID, reason)
}

func (e *Engine) releasePausedLocked(ctx stdctx.Context, reviewID, reason string) error {
	note := usageLimitReleasedNote
	if reason != "" {
		note = usageLimitReleasedNote + " (" + reason + ")"
	}
	_, err := e.store.FailPausedReviewRunsByReview(ctx, reviewID, note)
	return err
}

// ResumeAfterUsageLimit continues a reviewer whose passes were paused on the
// usage limit: it brings the reviewer back if its Chat is gone, asks it to
// review the paused commits again, and marks the passes running. It does nothing
// when no pass is paused. A reviewer that cannot continue at all releases its
// passes and returns ErrReviewerGone; any other error leaves them paused so the
// caller can retry.
func (e *Engine) ResumeAfterUsageLimit(ctx stdctx.Context, workerID domain.SessionID, reviewID string) error {
	if workerID == "" || reviewID == "" {
		return fmt.Errorf("%w: worker session id and review id are required", ErrInvalid)
	}
	if err := e.waitForChatRecovery(ctx); err != nil {
		return err
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	return e.resumeReviewerLocked(ctx, workerID, reviewID)
}

func (e *Engine) reviewByID(ctx stdctx.Context, workerID domain.SessionID, reviewID string) (domain.Review, bool, error) {
	reviews, err := e.store.ListReviewsBySession(ctx, workerID)
	if err != nil {
		return domain.Review{}, false, err
	}
	for _, review := range reviews {
		if review.ID == reviewID {
			return review, !review.IsArchived, nil
		}
	}
	return domain.Review{}, false, nil
}

func (e *Engine) resumeReviewerLocked(ctx stdctx.Context, workerID domain.SessionID, reviewID string) error {
	paused, err := e.store.ListPausedReviewRunsByReview(ctx, reviewID)
	if err != nil {
		return err
	}
	if len(paused) == 0 {
		return nil
	}
	worker, ok, err := e.sessions.GetSession(ctx, workerID)
	if err != nil {
		return err
	}
	review, live, err := e.reviewByID(ctx, workerID, reviewID)
	if err != nil {
		return err
	}
	if !ok || worker.IsTerminated || worker.Metadata.WorkspacePath == "" || !live {
		if err := e.releasePausedLocked(ctx, reviewID, "the worker or the reviewer is gone"); err != nil {
			return err
		}
		return ErrReviewerGone
	}
	runs, err := e.store.ListReviewRunsBySession(ctx, workerID)
	if err != nil {
		return err
	}
	restored, err := e.restorePersistedChatReviewerLocked(ctx, worker, review, reviewRunsForReview(runs, review.ID))
	if err != nil {
		return err
	}
	if restored.Restored {
		// The restore recorded a new handle and launch id.
		if review, _, err = e.reviewByID(ctx, workerID, reviewID); err != nil {
			return err
		}
	}
	handleID := restored.ReviewerHandleID
	if handleID == "" {
		handleID = review.ReviewerHandleID
	}
	config, err := e.reviewerConfigFor(ctx, worker, review.Harness)
	if err != nil {
		return err
	}
	spec := reviewLaunchSpec(worker, review.Harness, config, paused[0], reviewQueue(paused), 0, review.AgentSessionID, review.ReviewerLaunchID)
	// A fresh batch id: the Chat message of the original batch carries the old one,
	// and a repeated client message id would be dropped as a duplicate.
	spec.BatchID = e.newID()
	spec.InterfaceMode = review.InterfaceMode
	if err := e.launcher.Notify(ctx, handleID, spec); err != nil {
		return fmt.Errorf("continue reviewer after usage limit: %w", err)
	}
	_, err = e.store.ResumePausedReviewRunsByReview(ctx, reviewID)
	return err
}

// reviewerConfigFor is the model and effort a reviewer of harness runs with: the
// worker's resolved reviewer config when it is the same harness, otherwise the
// project's model for that harness, the way a trigger resolves it.
func (e *Engine) reviewerConfigFor(ctx stdctx.Context, worker domain.SessionRecord, harness domain.ReviewerHarness) (domain.AgentConfig, error) {
	selected, config, err := e.reviewerSelection(ctx, worker)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	if selected == harness {
		return config, nil
	}
	model, err := e.sameHarnessProjectModel(ctx, worker, harness)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	return domain.AgentConfig{Model: model}, nil
}

// MoveReviewerToAccount restarts a limit-paused reviewer under another account
// and continues its passes there. The account applies to this reviewer until the
// daemon restarts. Without an account resolver (no accounts configured) it is an
// ErrInvalid.
func (e *Engine) MoveReviewerToAccount(ctx stdctx.Context, workerID domain.SessionID, reviewID, accountID string) error {
	if workerID == "" || reviewID == "" || accountID == "" {
		return fmt.Errorf("%w: worker session id, review id and account id are required", ErrInvalid)
	}
	e.envMu.Lock()
	resolve := e.accountEnv
	e.envMu.Unlock()
	if resolve == nil {
		return fmt.Errorf("%w: moving a reviewer to another account is unavailable", ErrInvalid)
	}
	if err := e.waitForChatRecovery(ctx); err != nil {
		return err
	}
	unlock := e.lockWorker(workerID)
	defer unlock()
	review, live, err := e.reviewByID(ctx, workerID, reviewID)
	if err != nil {
		return err
	}
	if !live {
		if err := e.releasePausedLocked(ctx, reviewID, "the reviewer is gone"); err != nil {
			return err
		}
		return ErrReviewerGone
	}
	env, err := resolve(ctx, workerID, accountID)
	if err != nil {
		return err
	}
	e.envMu.Lock()
	e.reviewerEnv[reviewID] = env
	e.envMu.Unlock()
	// The running Chat keeps the old login; stop it so the restore starts it anew.
	if err := e.stopReviewChatWork(ctx, review); err != nil {
		return err
	}
	return e.resumeReviewerLocked(ctx, workerID, reviewID)
}

func (e *Engine) accountEnvFor(reviewID string) map[string]string {
	e.envMu.Lock()
	defer e.envMu.Unlock()
	return e.reviewerEnv[reviewID]
}

// envLauncher gives every launch of a reviewer that was moved to another account
// that account's environment.
type envLauncher struct {
	Launcher
	eng *Engine
}

func (l envLauncher) withEnv(spec LaunchSpec) LaunchSpec {
	if spec.AccountEnv == nil {
		spec.AccountEnv = l.eng.accountEnvFor(spec.ReviewSessionID)
	}
	return spec
}

func (l envLauncher) Spawn(ctx stdctx.Context, spec LaunchSpec) (LaunchResult, error) {
	return l.Launcher.Spawn(ctx, l.withEnv(spec))
}

func (l envLauncher) RestoreTerminal(ctx stdctx.Context, spec LaunchSpec) (LaunchResult, error) {
	return l.Launcher.RestoreTerminal(ctx, l.withEnv(spec))
}

func (l envLauncher) Notify(ctx stdctx.Context, handleID string, spec LaunchSpec) error {
	return l.Launcher.Notify(ctx, handleID, l.withEnv(spec))
}

// SetAccountEnv sets how a reviewer is launched under another account. It is
// bound after the account service exists, which is after the engine is built.
func (e *Engine) SetAccountEnv(resolve func(ctx stdctx.Context, worker domain.SessionID, accountID string) (map[string]string, error)) {
	e.envMu.Lock()
	defer e.envMu.Unlock()
	e.accountEnv = resolve
}
