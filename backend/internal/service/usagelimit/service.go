// Package usagelimit follows up on turns that stopped on the provider's usage
// limit. A session whose latest turn failed on the limit is shown as limited by
// the session service, derived from the turn itself; this package does what must
// happen exactly once around it: tell the orchestrator, tell the person once
// per account and limit window, and continue the session after the window
// resets without sending every session into the new window at once.
package usagelimit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/notify"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// DefaultMaxResumes is how many continued turns may run at once by default.
	DefaultMaxResumes = 3
	// defaultStagger is the least time between two automatic resumes.
	defaultStagger = 20 * time.Second
	// resetGrace is how long after the reported reset AO waits, so a clock that
	// is a little ahead of the provider's does not send a message too early.
	resetGrace = 30 * time.Second
	// repeatWindow is how soon a second limit stop after an automatic resume
	// means the limit is not really lifted.
	repeatWindow = 10 * time.Minute
	// retryDelay is the wait before retrying a message the session refused.
	retryDelay = time.Minute
	// maxAttempts bounds retries of one stop.
	maxAttempts = 5
)

// ErrSessionGone reports that the session can no longer receive the message:
// it was killed, archived or never existed.
var ErrSessionGone = errors.New("session cannot receive the resume message")

// Store is the persistence the watcher needs.
type Store interface {
	ListLimitStopCandidates(ctx context.Context, completedSince time.Time) ([]domain.LimitStopTurn, error)
	ListLatestFailedTurns(ctx context.Context, ids []domain.SessionID) ([]domain.LimitStopTurn, error)
	InsertUsageLimitPrompt(ctx context.Context, p domain.UsageLimitPrompt) (bool, error)
	ListOpenUsageLimitPrompts(ctx context.Context) ([]domain.UsageLimitPrompt, error)
	GetUsageLimitPrompt(ctx context.Context, id string) (domain.UsageLimitPrompt, bool, error)
	DecideUsageLimitPrompt(ctx context.Context, id, state, targetAccountID string, at time.Time) (bool, error)
	ReopenUsageLimitPrompt(ctx context.Context, id string) error
	ListUsageLimitPromptStops(ctx context.Context, windowKey string) ([]domain.UsageLimitStop, error)
	InsertUsageLimitStop(ctx context.Context, stop domain.UsageLimitStop) (bool, error)
	ListPendingUsageLimitStops(ctx context.Context) ([]domain.UsageLimitStop, error)
	ListUnreportedUsageLimitStops(ctx context.Context) ([]domain.UsageLimitStop, error)
	MarkUsageLimitStopReported(ctx context.Context, turnID string, at time.Time) error
	SkipUsageLimitStop(ctx context.Context, turnID, reason string) (bool, error)
	SetPendingUsageLimitStopsDisabled(ctx context.Context, disabled bool) error
	DeferUsageLimitStop(ctx context.Context, turnID string, retryAt time.Time) error
	MarkUsageLimitStopResumed(ctx context.Context, turnID string, resumedAt, since time.Time) error
	CountRunningUsageLimitResumes(ctx context.Context) (int, error)
	LastUsageLimitResumeAt(ctx context.Context) (time.Time, bool, error)
	CountRecentUsageLimitResumes(ctx context.Context, session domain.SessionID, reviewID string, since time.Time) (int, error)
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
}

// Settings is the user's resume preference.
type Settings interface {
	UsageLimitResume(ctx context.Context) (enabled bool, maxConcurrent int, err error)
}

// Accounts names the login a session runs on.
type Accounts interface {
	SessionAccountID(ctx context.Context, id domain.SessionID, harness domain.AgentHarness) (string, error)
}

// ReviewerAccounts names the login a reviewer runs on. A reviewer is not bound to
// an account of its own: it runs on the provider's default login, unless a
// switch moved it. Accounts that also implement it let reviewer stops be keyed
// by that login.
type ReviewerAccounts interface {
	ReviewerAccountID(ctx context.Context, harness domain.AgentHarness) (string, error)
}

// Reviewers pauses, continues and moves the reviewer of a worker whose Chat
// stopped on the usage limit. The worker id is the session the review is of.
type Reviewers interface {
	// Pause marks the reviewer's running passes paused.
	Pause(ctx context.Context, worker domain.SessionID, reviewID string) error
	// Resume continues the reviewer's paused passes. It returns ErrSessionGone when
	// the reviewer cannot continue at all.
	Resume(ctx context.Context, worker domain.SessionID, reviewID string) error
	// Release ends the paused passes of a reviewer that will not be continued.
	Release(ctx context.Context, worker domain.SessionID, reviewID, reason string) error
	// Move restarts the reviewer under accountID and continues its passes.
	Move(ctx context.Context, worker domain.SessionID, reviewID, accountID string) error
}

// Notifier stores one notification for the person.
type Notifier interface {
	Notify(ctx context.Context, intent ports.NotificationIntent) error
}

// Reporter tells a worker's orchestrator what happened to it.
type Reporter interface {
	ReportUsageLimit(ctx context.Context, worker domain.SessionID, note string) error
}

// Sender delivers the continue message and returns once the session accepted
// it. The key makes a repeated delivery of the same stop a no-op.
type Sender interface {
	SendSemantic(ctx context.Context, id domain.SessionID, message, key string) error
}

// Headroom says whether an account has usage left, from its last limits reading.
type Headroom interface {
	HasHeadroom(ctx context.Context, accountID string) bool
}

// Deps configures a Service. Accounts, Headroom, Notifier and Reporter are optional.
type Deps struct {
	Store    Store
	Settings Settings
	Accounts Accounts
	Headroom Headroom
	// Lister and Mover turn on the owner prompt: which accounts could take the
	// stopped sessions, and how to move them. Without them AO only notifies.
	Lister AccountLister
	Mover  SessionMover
	// Reviewers turns on the handling of reviewer stops. Without it a reviewer's
	// stop is not looked at.
	Reviewers Reviewers
	Notifier  Notifier
	Reporter  Reporter
	Sender    Sender
	Logger    *slog.Logger
	Now       func() time.Time
	// Stagger overrides the least time between two automatic resumes.
	Stagger time.Duration
}

// AutopilotPolicy says how a usage limit is handled for a session that runs on
// Autopilot, so the owner is not asked while they are away.
type AutopilotPolicy interface {
	// UsageLimit reports whether the session is under Autopilot and, if so,
	// whether the owner's profile clearly allows moving work to another account.
	// The default and every doubt is to wait for the reset.
	UsageLimit(ctx context.Context, id domain.SessionID) (covered, switchAccount bool)
}

// SetAutopilotPolicy binds the Autopilot policy after construction.
func (s *Service) SetAutopilotPolicy(p AutopilotPolicy) {
	s.policyMu.Lock()
	s.policy = p
	s.policyMu.Unlock()
}

func (s *Service) autopilotPolicy(ctx context.Context, id domain.SessionID) (covered, switchAccount bool) {
	s.policyMu.RLock()
	p := s.policy
	s.policyMu.RUnlock()
	if p == nil {
		return false, false
	}
	return p.UsageLimit(ctx, id)
}

// Service is the usage-limit watcher.
type Service struct {
	policyMu sync.RWMutex
	policy   AutopilotPolicy

	store     Store
	settings  Settings
	accounts  Accounts
	headroom  Headroom
	lister    AccountLister
	mover     SessionMover
	reviewers Reviewers
	notifier  Notifier
	reporter  Reporter
	sender    Sender
	log       *slog.Logger
	now       func() time.Time
	stagger   time.Duration

	mu      sync.Mutex
	running bool
	// parkedKnown and parked remember the setting last applied to pending stops.
	parkedKnown bool
	parked      bool
}

// New builds the watcher.
func New(d Deps) *Service {
	s := &Service{
		store: d.Store, settings: d.Settings, accounts: d.Accounts, headroom: d.Headroom, lister: d.Lister, mover: d.Mover, reviewers: d.Reviewers, notifier: d.Notifier,
		reporter: d.Reporter, sender: d.Sender, log: d.Logger, now: d.Now, stagger: d.Stagger,
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.stagger <= 0 {
		s.stagger = defaultStagger
	}
	return s
}

// Run sweeps until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("usage-limit sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep notices new limit stops, follows each up once, and starts at most one
// automatic resume that is due. A sweep that overlaps another does nothing.
func (s *Service) Sweep(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	enabled, maxConcurrent, err := s.settings.UsageLimitResume(ctx)
	if err != nil {
		return fmt.Errorf("read usage-limit resume setting: %w", err)
	}
	if maxConcurrent < 1 {
		maxConcurrent = DefaultMaxResumes
	}
	if err := s.applySetting(ctx, enabled); err != nil {
		return err
	}
	now := s.now()
	candidates, err := s.store.ListLimitStopCandidates(ctx, now.Add(-domain.UsageLimitScanWindow))
	if err != nil {
		return fmt.Errorf("list limit stops: %w", err)
	}
	// Reports that failed or were cut off by a restart go out again before new
	// stops are looked at, so a stop is never left unreported for good.
	if err := s.retryReports(ctx, now); err != nil && ctx.Err() == nil {
		s.log.Warn("retry usage-limit reports", "err", err)
	}
	live := make(map[string]bool, len(candidates))
	var pending []domain.LimitStopTurn
	for _, turn := range candidates {
		limit := turn.Limit()
		if limit == nil {
			continue
		}
		if turn.ReviewID != "" && s.reviewers == nil {
			continue
		}
		live[turn.TurnID] = true
		if turn.ResumeState == "" {
			if err := s.detect(ctx, turn, limit, enabled, now); err != nil && ctx.Err() == nil {
				s.log.Warn("follow up usage-limit stop", "session", turn.SessionID, "turn", turn.TurnID, "err", err)
			}
			continue
		}
		if turn.ResumeState == domain.UsageLimitStopPending {
			// A pending stop is listed whatever its age, so supersede only ever
			// closes a stop whose turn really is no longer the latest. One that
			// outlived its reset by more than the horizon is closed as expired.
			if domain.UsageLimitStopExpired(turn.StoppedAt(), turn.StopResetsAt, now) {
				if _, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffExpired); err != nil {
					return err
				}
				s.releaseReviewer(ctx, turn.SessionID, turn.ReviewID, domain.UsageLimitOffExpired)
				continue
			}
			pending = append(pending, turn)
		}
	}
	if err := s.supersede(ctx, live); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	return s.resumeOne(ctx, pending, maxConcurrent, now)
}

// applySetting parks the pending stops while automatic resume is off and
// reopens them when it comes back on. It runs only when the setting changes.
func (s *Service) applySetting(ctx context.Context, enabled bool) error {
	s.mu.Lock()
	unchanged := s.parkedKnown && s.parked == !enabled
	s.mu.Unlock()
	if unchanged {
		return nil
	}
	if err := s.store.SetPendingUsageLimitStopsDisabled(ctx, !enabled); err != nil {
		return fmt.Errorf("apply usage-limit resume setting: %w", err)
	}
	s.mu.Lock()
	s.parkedKnown, s.parked = true, !enabled
	s.mu.Unlock()
	return nil
}

// detect records a stop the first time AO sees it and does the once-only work:
// one notification per account and window, one report to the orchestrator.
func (s *Service) detect(ctx context.Context, turn domain.LimitStopTurn, limit *domain.ConversationUsageLimit, enabled bool, now time.Time) error {
	rec, found, err := s.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	stoppedAt := turn.StoppedAt()
	window, _ := domain.UsageLimitStopMessage(turn.ErrorMessage)
	stop := domain.UsageLimitStop{
		TurnID: turn.TurnID, SessionID: turn.SessionID, ProjectID: rec.ProjectID,
		WindowLabel: window, ResetsAt: limit.ResetsAt, DetectedAt: now, ReviewID: turn.ReviewID,
	}
	// The agent that hit the limit: the session's own, or its reviewer's.
	harness := rec.Harness
	displayName := rec.DisplayName
	if turn.ReviewID != "" {
		harness = domain.AgentHarness(turn.ReviewHarness)
		displayName += " (reviewer)"
	}
	s.setAccountKey(ctx, &stop, rec, harness)
	stop.WindowKey = domain.UsageLimitWindowKey(stop.AccountKey, limit.ResetsAt, stoppedAt)

	if rec.IsTerminated || rec.Activity.State == domain.ActivityExited || domain.UsageLimitStopExpired(stoppedAt, limit.ResetsAt, now) {
		// Stopped on purpose, or too old to be news: record it so it is not
		// looked at again, and do nothing else.
		reason := domain.UsageLimitSkipStopped
		if !rec.IsTerminated && rec.Activity.State != domain.ActivityExited {
			reason = domain.UsageLimitOffExpired
		}
		return s.recordSkipped(ctx, stop, reason)
	}

	// Under Autopilot the owner is away: the profile decides. By default the
	// sessions wait for the reset (nothing is asked, nothing moves); only a profile
	// that clearly allows it moves them to another account.
	covered, switchByProfile := s.autopilotPolicy(ctx, rec.ID)
	askOwner := !covered
	// The notification is idempotent by its key, so it goes first: a failure
	// before the stop row exists is retried by the next sweep.
	if s.notifier != nil {
		intent := ports.NotificationIntent{
			Type: domain.NotificationUsageLimit, SessionID: rec.ID, ProjectID: rec.ProjectID,
			CreatedAt: now, SourceKey: "usage_limit:" + stop.WindowKey, SessionDisplayName: displayName,
			UsageLimitWindow: window, UsageLimitProvider: providerLabel(harness),
			UsageLimitAutoResume: enabled && limit.ResetsAt != nil,
		}
		if provider, ok := domain.AgentAccountProviderFor(harness); ok && stop.AccountKey != "" && askOwner {
			intent.UsageLimitSwitchPrompt = s.hasSwitchTargets(ctx, string(provider), stop.AccountKey)
		}
		if limit.ResetsAt != nil {
			intent.UsageLimitResetsAt = *limit.ResetsAt
		}
		if err := s.notifier.Notify(ctx, intent); err != nil {
			return fmt.Errorf("notify usage limit: %w", err)
		}
	}
	// A reviewer's passes stop reading as running before the stop is recorded: a
	// failure here is retried by the next sweep, which has no stop row yet.
	if turn.ReviewID != "" {
		if err := s.reviewers.Pause(ctx, rec.ID, turn.ReviewID); err != nil {
			return fmt.Errorf("pause reviewer: %w", err)
		}
	}
	inserted, err := s.store.InsertUsageLimitStop(ctx, stop)
	if err != nil || !inserted {
		return err
	}
	// One owner prompt per limit window, however many sessions stop on it.
	if askOwner || switchByProfile {
		if err := s.ensurePrompt(ctx, stop, harness, now); err != nil {
			s.log.Warn("record usage-limit prompt", "session", rec.ID, "err", err)
		}
	}
	if covered && switchByProfile {
		s.switchByProfile(ctx, stop, harness)
	}
	reason := ""
	switch {
	case limit.ResetsAt == nil:
		reason = domain.UsageLimitOffUnknownReset
	case !enabled:
		reason = domain.UsageLimitOffDisabled
	case turn.RetryArmed:
		reason = domain.UsageLimitOffRetryArmed
	}
	if reason != "" {
		if _, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, reason); err != nil {
			return err
		}
	}
	if s.reporter != nil && rec.Kind == domain.KindWorker {
		note := stopReportNote(rec, turn.ReviewID, window, limit.ResetsAt, reason, now)
		if err := s.reporter.ReportUsageLimit(ctx, rec.ID, note); err != nil {
			s.log.Warn("report usage-limit stop to orchestrator", "session", rec.ID, "err", err)
		} else if err := s.store.MarkUsageLimitStopReported(ctx, turn.TurnID, now); err != nil {
			return err
		}
	}
	return nil
}

// retryReports sends the orchestrator report of every recorded stop that never
// got one. A stop whose worker is gone, or that is older than the scan window,
// is settled without sending: there is nobody to tell, or nothing current to say.
func (s *Service) retryReports(ctx context.Context, now time.Time) error {
	if s.reporter == nil {
		return nil
	}
	stops, err := s.store.ListUnreportedUsageLimitStops(ctx)
	if err != nil {
		return err
	}
	for _, stop := range stops {
		rec, found, err := s.store.GetSession(ctx, stop.SessionID)
		if err != nil {
			return err
		}
		if !found || rec.Kind != domain.KindWorker || rec.IsTerminated || rec.Activity.State == domain.ActivityExited ||
			stop.DetectedAt.Before(now.Add(-domain.UsageLimitScanWindow)) {
			if err := s.store.MarkUsageLimitStopReported(ctx, stop.TurnID, now); err != nil {
				return err
			}
			continue
		}
		note := stopReportNote(rec, stop.ReviewID, stop.WindowLabel, stop.ResetsAt, stop.SkipReason, now)
		if err := s.reporter.ReportUsageLimit(ctx, rec.ID, note); err != nil {
			s.log.Warn("report usage-limit stop to orchestrator", "session", rec.ID, "err", err)
			continue
		}
		if err := s.store.MarkUsageLimitStopReported(ctx, stop.TurnID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) recordSkipped(ctx context.Context, stop domain.UsageLimitStop, reason string) error {
	inserted, err := s.store.InsertUsageLimitStop(ctx, stop)
	if err != nil || !inserted {
		return err
	}
	_, err = s.store.SkipUsageLimitStop(ctx, stop.TurnID, reason)
	return err
}

// supersede closes the pending stops whose turn is no longer the session's
// latest: the person sent something new, retried it, or rolled it back.
func (s *Service) supersede(ctx context.Context, live map[string]bool) error {
	stops, err := s.store.ListPendingUsageLimitStops(ctx)
	if err != nil {
		return fmt.Errorf("list pending usage-limit stops: %w", err)
	}
	for _, stop := range stops {
		if live[stop.TurnID] {
			continue
		}
		if _, err := s.store.SkipUsageLimitStop(ctx, stop.TurnID, domain.UsageLimitSkipSuperseded); err != nil {
			return err
		}
		s.releaseReviewer(ctx, stop.SessionID, stop.ReviewID, domain.UsageLimitSkipSuperseded)
	}
	return nil
}

// resumeOne continues the next due session, if a slot is free and the stagger
// since the last resume has passed. Orchestrators go first so that they can
// coordinate the workers that follow.
func (s *Service) resumeOne(ctx context.Context, pending []domain.LimitStopTurn, maxConcurrent int, now time.Time) error {
	type due struct {
		turn domain.LimitStopTurn
		rec  domain.SessionRecord
	}
	var ready []due
	for _, turn := range pending {
		if turn.StopResetsAt == nil || turn.StopResetsAt.Add(resetGrace).After(now) {
			continue
		}
		if turn.RetryAt != nil && turn.RetryAt.After(now) {
			continue
		}
		rec, found, err := s.store.GetSession(ctx, turn.SessionID)
		if err != nil {
			return err
		}
		if !found || rec.IsTerminated || rec.Activity.State == domain.ActivityExited {
			// Killed or stopped by a person: never continue it.
			if _, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitSkipStopped); err != nil {
				return err
			}
			s.releaseReviewer(ctx, turn.SessionID, turn.ReviewID, domain.UsageLimitSkipStopped)
			continue
		}
		if turn.RetryArmed {
			if _, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffRetryArmed); err != nil {
				return err
			}
			continue
		}
		ready = append(ready, due{turn: turn, rec: rec})
	}
	if len(ready) == 0 {
		return nil
	}
	running, err := s.store.CountRunningUsageLimitResumes(ctx)
	if err != nil {
		return err
	}
	if running >= maxConcurrent {
		return nil
	}
	if last, ok, err := s.store.LastUsageLimitResumeAt(ctx); err != nil {
		return err
	} else if ok && now.Sub(last) < s.stagger {
		return nil
	}
	sort.SliceStable(ready, func(i, j int) bool {
		oi, oj := ready[i].rec.Kind == domain.KindOrchestrator, ready[j].rec.Kind == domain.KindOrchestrator
		if oi != oj {
			return oi
		}
		return ready[i].turn.StopResetsAt.Before(*ready[j].turn.StopResetsAt)
	})
	for _, next := range ready {
		sent, err := s.resume(ctx, next.turn, next.rec, now)
		if err != nil || sent {
			return err
		}
	}
	return nil
}

// resume sends the continue message to one session. It returns true when a
// message went out, which uses up this sweep's resume.
func (s *Service) resume(ctx context.Context, turn domain.LimitStopTurn, rec domain.SessionRecord, now time.Time) (bool, error) {
	if turn.ReviewID != "" {
		return s.resumeReviewer(ctx, turn, rec, now)
	}
	if recent, err := s.store.CountRecentUsageLimitResumes(ctx, rec.ID, "", now.Add(-repeatWindow)); err != nil {
		return false, err
	} else if recent > 0 {
		// It stopped again right after AO continued it: the limit is not lifted
		// (or a bigger one applies), so do not keep spending turns on it.
		_, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffRepeated)
		return false, err
	}
	window, _ := domain.UsageLimitStopMessage(turn.ErrorMessage)
	err := s.sender.SendSemantic(ctx, rec.ID, ResumeMessage(window), "usage-limit-resume:"+turn.TurnID)
	switch {
	case err == nil:
		return true, s.store.MarkUsageLimitStopResumed(ctx, turn.TurnID, now, now)
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, ErrSessionGone):
		_, skipErr := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitSkipStopped)
		return false, skipErr
	case turn.Attempts+1 >= maxAttempts:
		s.log.Warn("give up resuming usage-limited session", "session", rec.ID, "err", err)
		_, skipErr := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffUnavailable)
		return false, skipErr
	default:
		s.log.Info("usage-limited session could not take the resume message yet", "session", rec.ID, "err", err)
		return false, s.store.DeferUsageLimitStop(ctx, turn.TurnID, now.Add(retryDelay))
	}
}

// ResumeAfterAccountMove continues a session whose latest turn stopped on the
// old account's usage limit, now that its agent runs on targetAccountID. It does
// nothing when the session is not stopped on a limit, when the target account is
// known to be at its own limit, or when the session already continued. It
// reports whether the continue message went out.
func (s *Service) ResumeAfterAccountMove(ctx context.Context, id domain.SessionID, targetAccountID string) (bool, error) {
	if s.headroom != nil && targetAccountID != "" && !s.headroom.HasHeadroom(ctx, targetAccountID) {
		return false, nil
	}
	now := s.now()
	// The session's own latest failed turn, whatever became of its stop row: a
	// move is an explicit action, so it continues a turn the automatic resume
	// had parked (resume off, reset time unknown) too.
	latest, err := s.store.ListLatestFailedTurns(ctx, []domain.SessionID{id})
	if err != nil {
		return false, fmt.Errorf("list latest failed turn: %w", err)
	}
	for _, turn := range latest {
		if turn.SessionID != id || turn.Limit() == nil || turn.ResumeState == domain.UsageLimitStopResumed {
			continue
		}
		rec, found, err := s.store.GetSession(ctx, id)
		if err != nil {
			return false, err
		}
		if !found || rec.IsTerminated {
			return false, nil
		}
		window, _ := domain.UsageLimitStopMessage(turn.ErrorMessage)
		if err := s.sender.SendSemantic(ctx, id, AccountMoveResumeMessage(window), "usage-limit-account-move:"+turn.TurnID); err != nil {
			return false, err
		}
		// Settle the stop so the watcher does not continue the session a second
		// time; a stop AO never recorded (or already closed) has no row to update.
		if err := s.store.MarkUsageLimitStopResumed(ctx, turn.TurnID, now, now); err != nil {
			s.log.Warn("mark usage-limit stop resumed after account move", "session", id, "turn", turn.TurnID, "err", err)
		}
		return true, nil
	}
	return false, nil
}

// AccountMoveResumeMessage is the prompt sent to a session that was moved to an
// account with usage left after its turn stopped on a limit.
func AccountMoveResumeMessage(window string) string {
	limit := "usage limit"
	if window != "" {
		limit = window + " usage limit"
	}
	return "Your previous turn stopped because the " + limit + " of the previous account was reached. You are now on another account with usage left. Continue where you left off."
}

// ResumeMessage is the short prompt sent to a session whose limit has reset.
func ResumeMessage(window string) string {
	limit := "usage limit"
	if window != "" {
		limit = window + " usage limit"
	}
	return "Your previous turn stopped because the " + limit + " was reached. It has reset. Continue where you left off."
}

// ReportNote is what the orchestrator is told about a worker that stopped.
func ReportNote(rec domain.SessionRecord, window string, resetsAt *time.Time, offReason string, now time.Time) string {
	limit := "usage limit"
	if window != "" {
		limit = window + " usage limit"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Stopped on the %s, so it is not stuck and did not miss its task.", limit)
	if resetsAt != nil {
		fmt.Fprintf(&out, " The limit resets at %s.", notify.FormatUsageLimitReset(*resetsAt, now))
	} else {
		out.WriteString(" The provider gave no reset time.")
	}
	switch offReason {
	case "":
		out.WriteString(" AO continues it automatically after the reset, staggered with the other stopped sessions.")
	case domain.UsageLimitOffDisabled:
		out.WriteString(" Automatic resume is off: send it a message after the reset.")
	case domain.UsageLimitOffUnknownReset:
		out.WriteString(" AO cannot schedule a resume: send it a message once the limit is lifted.")
	default:
		out.WriteString(" AO will not resume it by itself: send it a message after the reset.")
	}
	return out.String()
}

func providerLabel(harness domain.AgentHarness) string {
	switch provider, _ := domain.AgentAccountProviderFor(harness); provider {
	case domain.AgentAccountProviderClaude:
		return "Claude"
	case domain.AgentAccountProviderCodex:
		return "Codex"
	default:
		return ""
	}
}

// setAccountKey names the login a stop happened on. A reviewer runs on the
// provider's default login, so it is keyed by that, not by its worker's account.
func (s *Service) setAccountKey(ctx context.Context, stop *domain.UsageLimitStop, rec domain.SessionRecord, harness domain.AgentHarness) {
	if s.accounts == nil {
		return
	}
	if stop.ReviewID != "" {
		if ra, ok := s.accounts.(ReviewerAccounts); ok {
			if key, err := ra.ReviewerAccountID(ctx, harness); err == nil {
				stop.AccountKey = key
			}
		}
		return
	}
	if key, err := s.accounts.SessionAccountID(ctx, rec.ID, harness); err == nil {
		stop.AccountKey = key
	}
}

// releaseReviewer ends the paused passes of a reviewer whose stop was closed
// without a continue, so they do not stay paused forever. It does nothing for a
// stop of a session itself.
func (s *Service) releaseReviewer(ctx context.Context, session domain.SessionID, reviewID, reason string) {
	if reviewID == "" || s.reviewers == nil {
		return
	}
	if err := s.reviewers.Release(ctx, session, reviewID, reason); err != nil && ctx.Err() == nil {
		s.log.Warn("release paused reviewer", "session", session, "review", reviewID, "err", err)
	}
}

// resumeReviewer continues a reviewer whose passes paused on the limit.
func (s *Service) resumeReviewer(ctx context.Context, turn domain.LimitStopTurn, rec domain.SessionRecord, now time.Time) (bool, error) {
	if recent, err := s.store.CountRecentUsageLimitResumes(ctx, rec.ID, turn.ReviewID, now.Add(-repeatWindow)); err != nil {
		return false, err
	} else if recent > 0 {
		// It stopped again right after AO continued it: the limit is not lifted.
		_, err := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffRepeated)
		s.releaseReviewer(ctx, rec.ID, turn.ReviewID, domain.UsageLimitOffRepeated)
		return false, err
	}
	err := s.reviewers.Resume(ctx, rec.ID, turn.ReviewID)
	switch {
	case err == nil:
		return true, s.store.MarkUsageLimitStopResumed(ctx, turn.TurnID, now, now)
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, ErrSessionGone):
		// The reviewer released its own passes.
		_, skipErr := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitSkipStopped)
		return false, skipErr
	case turn.Attempts+1 >= maxAttempts:
		s.log.Warn("give up resuming usage-limited reviewer", "session", rec.ID, "review", turn.ReviewID, "err", err)
		_, skipErr := s.store.SkipUsageLimitStop(ctx, turn.TurnID, domain.UsageLimitOffUnavailable)
		s.releaseReviewer(ctx, rec.ID, turn.ReviewID, domain.UsageLimitOffUnavailable)
		return false, skipErr
	default:
		s.log.Info("usage-limited reviewer could not continue yet", "session", rec.ID, "review", turn.ReviewID, "err", err)
		return false, s.store.DeferUsageLimitStop(ctx, turn.TurnID, now.Add(retryDelay))
	}
}

// stopReportNote is what the orchestrator is told about a stop: the worker's
// own, or the worker's reviewer's.
func stopReportNote(rec domain.SessionRecord, reviewID, window string, resetsAt *time.Time, offReason string, now time.Time) string {
	if reviewID == "" {
		return ReportNote(rec, window, resetsAt, offReason, now)
	}
	return ReviewerReportNote(window, resetsAt, offReason, now)
}

// ReviewerReportNote is what the orchestrator is told about a worker's reviewer
// that stopped: the worker itself is not affected, only its AO review.
func ReviewerReportNote(window string, resetsAt *time.Time, offReason string, now time.Time) string {
	limit := "usage limit"
	if window != "" {
		limit = window + " usage limit"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "The AO reviewer of this worker stopped on the %s, so its review is paused (status paused), not stuck.", limit)
	if resetsAt != nil {
		fmt.Fprintf(&out, " The limit resets at %s.", notify.FormatUsageLimitReset(*resetsAt, now))
	} else {
		out.WriteString(" The provider gave no reset time.")
	}
	if offReason == "" {
		out.WriteString(" AO continues the review automatically after the reset.")
	} else {
		out.WriteString(" AO will not continue it by itself: run `ao review cancel` and then `ao review trigger --rerun` after the reset, or choose another agent with --agent.")
	}
	return out.String()
}
