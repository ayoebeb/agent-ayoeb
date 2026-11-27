package review

import (
	stdctx "context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A reviewer that Ayo restarted under, or that silently stopped working, leaves
// its passes "running" forever: nothing submits them and they hold the review
// slot of that head. RecoverRunningReviews handles the first case once after a
// daemon start, SweepStalledReviews the second on a timer.

// ReviewerStallAfter is how long a running pass may show no reviewer activity
// before the watchdog fails it.
const ReviewerStallAfter = 20 * time.Minute

const (
	resumedAfterRestartNote = "resumed after an Ayo restart"
	retriedStalledNote      = "retried automatically after the reviewer stopped"
	reviewerStoppedReason   = "reviewer stopped"
	restartFailedReason     = "reviewer could not be resumed after an Ayo restart"
	reviewerGoneDetail      = ": the worker or the reviewer is gone"
)

// reviewerProbe is what the restart recovery and the watchdog read about a
// reviewer beyond Launcher.Alive. The production launcher implements it.
type reviewerProbe interface {
	// Busy reports whether a Chat reviewer has a turn in progress.
	Busy(handleID string) bool
	// Output returns a terminal reviewer's recent screen.
	Output(ctx stdctx.Context, handleID string) (string, error)
}

// StalledReview is a reviewer the watchdog gave up on, so that the worker's
// orchestrator can be told once.
type StalledReview struct {
	WorkerID domain.SessionID
	// Note is the report to file for the worker.
	Note string
}

// reviewerWatch is what the watchdog remembers about one reviewer between sweeps.
type reviewerWatch struct {
	runs      string
	since     time.Time
	output    string
	hasOutput bool
	outputAt  time.Time
}

type reviewerRef struct {
	workerID domain.SessionID
	reviewID string
}

// reviewerPasses is one reviewer's running work, read under the worker lock.
type reviewerPasses struct {
	worker domain.SessionRecord
	review domain.Review
	// runs are the running passes, oldest first.
	runs []domain.ReviewRun
	// live is false when the worker or the reviewer can no longer take work.
	live bool
}

func (e *Engine) runningReviewers(ctx stdctx.Context) ([]reviewerRef, error) {
	runs, err := e.store.ListRunningReviewRuns(ctx)
	if err != nil {
		return nil, err
	}
	var refs []reviewerRef
	for _, run := range runs {
		ref := reviewerRef{workerID: run.SessionID, reviewID: run.ReviewID}
		if !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (e *Engine) reviewerPassesLocked(ctx stdctx.Context, ref reviewerRef) (reviewerPasses, error) {
	var p reviewerPasses
	runs, err := e.store.ListReviewRunsBySession(ctx, ref.workerID)
	if err != nil {
		return p, err
	}
	for _, run := range reviewRunsForReview(runs, ref.reviewID) {
		if run.Status == domain.ReviewRunRunning {
			p.runs = append(p.runs, run)
		}
	}
	if len(p.runs) == 0 {
		return p, nil
	}
	slices.SortStableFunc(p.runs, func(a, b domain.ReviewRun) int { return a.CreatedAt.Compare(b.CreatedAt) })
	worker, ok, err := e.sessions.GetSession(ctx, ref.workerID)
	if err != nil {
		return p, err
	}
	review, found, err := e.reviewByID(ctx, ref.workerID, ref.reviewID)
	if err != nil {
		return p, err
	}
	p.worker, p.review = worker, review
	p.live = ok && !worker.IsTerminated && worker.Metadata.WorkspacePath != "" && found
	return p, nil
}

// RecoverRunningReviews relaunches, once after a daemon start, every reviewer
// that still has running passes but is no longer working on them. A terminal
// reviewer whose process survived is left alone; a Chat reviewer needs a turn in
// progress, because a restored conversation does not continue by itself. Passes
// that cannot be relaunched fail, which frees their head for a new trigger.
// stagger spaces the relaunches. Call it after boot reconciliation settled which
// sessions are live.
func (e *Engine) RecoverRunningReviews(ctx stdctx.Context, stagger time.Duration) error {
	defer e.restartPending.Store(false)
	if err := e.waitForChatRecovery(ctx); err != nil {
		return err
	}
	refs, err := e.runningReviewers(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, ref := range refs {
		relaunched, err := e.recoverReviewer(ctx, ref)
		if err != nil {
			failures = append(failures, fmt.Errorf("recover review %s of %s: %w", ref.reviewID, ref.workerID, err))
		}
		if relaunched && stagger > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(append(failures, ctx.Err())...)
			case <-time.After(stagger):
			}
		}
	}
	return errors.Join(failures...)
}

func (e *Engine) recoverReviewer(ctx stdctx.Context, ref reviewerRef) (bool, error) {
	unlock := e.lockWorker(ref.workerID)
	defer unlock()
	p, err := e.reviewerPassesLocked(ctx, ref)
	if err != nil || len(p.runs) == 0 {
		return false, err
	}
	if !p.live {
		return false, e.failRunningRestoredRuns(ctx, p.runs, restartFailedReason+reviewerGoneDetail)
	}
	if p.review.ReviewerHandleID != "" {
		alive, err := e.launcher.Alive(ctx, p.review.ReviewerHandleID, p.review.ReviewerLaunchID)
		if err != nil {
			// Unknown is not gone; the watchdog catches it if it stays silent.
			return false, fmt.Errorf("probe reviewer: %w", err)
		}
		if alive && (p.review.InterfaceMode != domain.ReviewerInterfaceChat || e.probe == nil || e.probe.Busy(p.review.ReviewerHandleID)) {
			return false, nil
		}
	}
	for i := range p.runs {
		// A pass that was already the automatic retry keeps saying so, or the
		// watchdog would retry it again.
		if strings.Contains(p.runs[i].Body, retriedStalledNote) {
			p.runs[i].Body = retriedStalledNote + "; " + resumedAfterRestartNote
		} else {
			p.runs[i].Body = resumedAfterRestartNote
		}
	}
	if err := e.relaunchReviewerLocked(ctx, p.worker, p.review, p.runs); err != nil {
		if ctx.Err() != nil {
			// The daemon is stopping again; the next start retries.
			return false, err
		}
		return false, errors.Join(err,
			e.failRunningRestoredRuns(ctx, p.runs, restartFailedReason+": "+err.Error()),
			e.markReviewerExited(ctx, p.worker.ID, p.review.Harness))
	}
	return true, nil
}

// relaunchReviewerLocked starts a new reviewer process for passes that are
// already recorded as running, with their Body as the note to keep on them. It
// resumes the reviewer's conversation when there is one and otherwise, or when
// that fails, starts a fresh reviewer on the same heads.
func (e *Engine) relaunchReviewerLocked(ctx stdctx.Context, worker domain.SessionRecord, review domain.Review, runs []domain.ReviewRun) error {
	harness := review.Harness
	mode := review.InterfaceMode
	if mode == "" {
		mode = e.launcher.InterfaceMode(harness)
	}
	if mode == domain.ReviewerInterfaceChat && e.launcher.InterfaceMode(harness) != domain.ReviewerInterfaceChat {
		mode = domain.ReviewerInterfaceTUI
	}
	config, err := e.reviewerConfigFor(ctx, worker, harness)
	if err != nil {
		return err
	}
	projectEnv, err := e.projectEnv(ctx, worker.ProjectID)
	if err != nil {
		return err
	}
	releaseHarness, err := e.beginHarnessUse(harness)
	if err != nil {
		return err
	}
	defer releaseHarness()
	if err := e.launcher.Preflight(ctx, harness, worker.Metadata.WorkspacePath, mode); err != nil {
		return fmt.Errorf("reviewer preflight: %w", err)
	}

	batchID := e.newID()
	moved := make([]domain.ReviewRun, 0, len(runs))
	for _, run := range runs {
		ok, err := e.store.RelaunchReviewRun(ctx, run.ID, batchID, run.Body)
		if err != nil {
			return err
		}
		if ok {
			run.BatchID = batchID
			moved = append(moved, run)
		}
	}
	if len(moved) == 0 {
		return nil
	}
	// Stop what is left of the old reviewer. The passes already carry the new
	// batch, so settling its turns cannot fail them.
	if err := e.stopReviewer(ctx, review); err != nil {
		return err
	}

	agentSessionID, conversationID := review.AgentSessionID, ""
	if mode == domain.ReviewerInterfaceChat {
		agentSessionID, conversationID = "", review.ProviderConversationID
		if conversationID == "" {
			conversationID = review.AgentSessionID
		}
	}
	if review.InterfaceMode != "" && mode != review.InterfaceMode {
		agentSessionID, conversationID = "", ""
	}
	spawn := func(agentSessionID, conversationID string) error {
		now := e.clock()
		launchID := e.newID()
		row, err := e.upsertReview(ctx, worker, harness, review.ReviewerHandleID, agentSessionID, launchID, domain.ActivityActive, now)
		if err != nil {
			return err
		}
		if err := e.setReviewerInterfaceMode(ctx, row.ID, mode, now); err != nil {
			return err
		}
		spec := reviewLaunchSpec(worker, harness, config, moved[0], reviewQueue(moved), agentSessionID, launchID)
		spec.ProjectEnv = projectEnv
		spec.InterfaceMode = mode
		spec.ProviderConversationID = conversationID
		launch, err := e.launcher.Spawn(ctx, spec)
		if err != nil {
			return fmt.Errorf("launch reviewer: %w", err)
		}
		if launch.AgentSessionID != "" {
			agentSessionID = launch.AgentSessionID
		}
		if launch.LaunchID != "" {
			launchID = launch.LaunchID
		}
		if _, err := e.upsertReview(ctx, worker, harness, launch.HandleID, agentSessionID, launchID, "", e.clock()); err != nil {
			return errors.Join(err, e.launcher.Destroy(ctx, launch.HandleID))
		}
		return nil
	}
	err = spawn(agentSessionID, conversationID)
	if err == nil || ctx.Err() != nil || (agentSessionID == "" && conversationID == "") {
		return err
	}
	if freshErr := spawn("", ""); freshErr != nil {
		return errors.Join(fmt.Errorf("resume reviewer conversation: %w", err), freshErr)
	}
	return nil
}

func (e *Engine) stopReviewer(ctx stdctx.Context, review domain.Review) error {
	if review.InterfaceMode == domain.ReviewerInterfaceChat {
		return e.stopReviewChatWork(ctx, review)
	}
	if review.ReviewerHandleID == "" {
		return nil
	}
	if err := e.launcher.Destroy(ctx, review.ReviewerHandleID); err != nil {
		return fmt.Errorf("stop previous reviewer: %w", err)
	}
	return nil
}

func (e *Engine) markReviewerExited(ctx stdctx.Context, workerID domain.SessionID, harness domain.ReviewerHarness) error {
	review, ok, err := e.store.GetReviewBySessionAndHarness(ctx, workerID, harness)
	if err != nil || !ok {
		return err
	}
	review.ReviewerActivityState = domain.ActivityExited
	review.UpdatedAt = e.clock()
	return e.store.UpsertReview(ctx, review)
}

// SweepStalledReviews fails the running passes of every reviewer that showed no
// activity for ReviewerStallAfter and retries them once on the same heads. It
// returns the reviewers it gave up on: the retry could not start, or stopped
// too. Call it from one goroutine.
func (e *Engine) SweepStalledReviews(ctx stdctx.Context) ([]StalledReview, error) {
	if e.restartPending.Load() {
		return nil, nil
	}
	if err := e.waitForChatRecovery(ctx); err != nil {
		return nil, err
	}
	refs, err := e.runningReviewers(ctx)
	if err != nil {
		return nil, err
	}
	// A reviewer with nothing running anymore starts afresh next time.
	for reviewID := range e.watches {
		if !slices.ContainsFunc(refs, func(ref reviewerRef) bool { return ref.reviewID == reviewID }) {
			delete(e.watches, reviewID)
		}
	}
	var gaveUp []StalledReview
	var failures []error
	for _, ref := range refs {
		stalled, err := e.sweepReviewer(ctx, ref)
		if err != nil {
			failures = append(failures, fmt.Errorf("watch review %s of %s: %w", ref.reviewID, ref.workerID, err))
		}
		if stalled != nil {
			gaveUp = append(gaveUp, *stalled)
		}
	}
	return gaveUp, errors.Join(failures...)
}

func (e *Engine) sweepReviewer(ctx stdctx.Context, ref reviewerRef) (*StalledReview, error) {
	unlock := e.lockWorker(ref.workerID)
	defer unlock()
	p, err := e.reviewerPassesLocked(ctx, ref)
	if err != nil || len(p.runs) == 0 {
		return nil, err
	}
	if !p.live {
		return nil, e.failRunningRestoredRuns(ctx, p.runs, reviewerStoppedReason+reviewerGoneDetail)
	}
	ids := make([]string, 0, len(p.runs))
	for _, run := range p.runs {
		ids = append(ids, run.ID)
	}
	key := strings.Join(ids, ",")
	now := e.clock()
	watch := e.watches[ref.reviewID]
	if watch == nil || watch.runs != key {
		watch = &reviewerWatch{runs: key, since: now}
		e.watches[ref.reviewID] = watch
	}
	last, err := e.reviewerLastActivity(ctx, p.review, watch, now)
	if err != nil || now.Sub(last) < ReviewerStallAfter {
		return nil, err
	}
	delete(e.watches, ref.reviewID)
	return e.retryStalledLocked(ctx, p)
}

// reviewerLastActivity is the newest sign of life: when the watchdog first saw
// these passes, the reviewer's own hooks, its Chat conversation, or its
// terminal screen changing.
func (e *Engine) reviewerLastActivity(ctx stdctx.Context, review domain.Review, watch *reviewerWatch, now time.Time) (time.Time, error) {
	last := watch.since
	later := func(at time.Time) {
		if at.After(last) {
			last = at
		}
	}
	later(review.UpdatedAt)
	if review.InterfaceMode == domain.ReviewerInterfaceChat {
		at, ok, err := e.store.ReviewChatLastActivity(ctx, review.ID)
		if err != nil {
			return last, err
		}
		if ok {
			later(at)
		}
		return last, nil
	}
	// An idle reviewer ended its turn: what still moves on its screen is not work.
	if e.probe != nil && review.ReviewerHandleID != "" && review.ReviewerActivityState != domain.ActivityIdle {
		// An unreadable screen is no sign of life either way.
		if output, err := e.probe.Output(ctx, review.ReviewerHandleID); err == nil {
			if watch.hasOutput && output != watch.output {
				watch.outputAt = now
			}
			watch.output, watch.hasOutput = output, true
		}
	}
	later(watch.outputAt)
	return last, nil
}

func (e *Engine) retryStalledLocked(ctx stdctx.Context, p reviewerPasses) (*StalledReview, error) {
	retried := false
	var stopped []domain.ReviewRun
	for _, run := range p.runs {
		retried = retried || strings.Contains(run.Body, retriedStalledNote)
		// A submission does not take the worker lock, so only passes this update
		// really failed are retried.
		updated, err := e.store.UpdateReviewRunResult(ctx, run.ID, domain.ReviewRunFailed, domain.VerdictNone, reviewerStoppedReason, "", run.AutoInjectReview)
		if err != nil {
			return nil, err
		}
		if updated {
			stopped = append(stopped, run)
		}
	}
	if len(stopped) == 0 {
		return nil, nil
	}
	if retried {
		return stalledReview(p, stopped, "the reviewer stopped again after one automatic retry"),
			errors.Join(e.stopReviewer(ctx, p.review), e.markReviewerExited(ctx, p.worker.ID, p.review.Harness))
	}

	now := e.clock()
	batchID := e.newID()
	var retries []domain.ReviewRun
	for _, run := range stopped {
		run.ID, run.BatchID, run.CreatedAt = e.newID(), batchID, now
		run.Status, run.Verdict, run.Body = domain.ReviewRunRunning, domain.VerdictNone, retriedStalledNote
		run.GithubReviewID, run.DeliveredAt = "", nil
		if err := e.store.InsertReviewRun(ctx, run); err != nil {
			if errors.Is(err, domain.ErrDuplicateReviewRun) {
				continue
			}
			return stalledReview(p, stopped, "the reviewer stopped and the automatic retry could not be recorded: "+err.Error()), err
		}
		retries = append(retries, run)
	}
	if len(retries) == 0 {
		return nil, nil
	}
	if err := e.relaunchReviewerLocked(ctx, p.worker, p.review, retries); err != nil {
		return stalledReview(p, retries, "the reviewer stopped and the automatic retry could not start: "+err.Error()),
			errors.Join(err,
				e.failRunningRestoredRuns(ctx, retries, reviewerStoppedReason+"; the automatic retry failed: "+err.Error()),
				e.markReviewerExited(ctx, p.worker.ID, p.review.Harness))
	}
	return nil, nil
}

func stalledReview(p reviewerPasses, runs []domain.ReviewRun, reason string) *StalledReview {
	targets := make([]string, 0, len(runs))
	for _, run := range runs {
		targets = append(targets, fmt.Sprintf("%s at %s", run.PRURL, shortSHA(run.TargetSHA)))
	}
	// The advice comes before the error so a long error cannot push it past the
	// report's length limit.
	note := []rune(fmt.Sprintf("[AUTOMATIC REPORT from AO, not written by the worker] the %s review for worker %s is not running anymore. Not reviewed: %s. Check the reviewer's login and model, then start a new review with `ao review trigger %s`. Reason: %s",
		p.review.Harness, p.worker.ID, strings.Join(targets, ", "), p.worker.ID, reason))
	if len(note) > domain.MaxReportTextCharacters {
		note = note[:domain.MaxReportTextCharacters]
	}
	return &StalledReview{WorkerID: p.worker.ID, Note: string(note)}
}
