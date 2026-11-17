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
	InsertUsageLimitStop(ctx context.Context, stop domain.UsageLimitStop) (bool, error)
	ListPendingUsageLimitStops(ctx context.Context) ([]domain.UsageLimitStop, error)
	MarkUsageLimitStopReported(ctx context.Context, turnID string, at time.Time) error
	SkipUsageLimitStop(ctx context.Context, turnID, reason string) (bool, error)
	SetPendingUsageLimitStopsDisabled(ctx context.Context, disabled bool) error
	DeferUsageLimitStop(ctx context.Context, turnID string, retryAt time.Time) error
	MarkUsageLimitStopResumed(ctx context.Context, turnID string, resumedAt, since time.Time) error
	CountRunningUsageLimitResumes(ctx context.Context) (int, error)
	LastUsageLimitResumeAt(ctx context.Context) (time.Time, bool, error)
	CountRecentUsageLimitResumes(ctx context.Context, session domain.SessionID, since time.Time) (int, error)
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

// Deps configures a Service. Accounts, Notifier and Reporter are optional.
type Deps struct {
	Store    Store
	Settings Settings
	Accounts Accounts
	Notifier Notifier
	Reporter Reporter
	Sender   Sender
	Logger   *slog.Logger
	Now      func() time.Time
	// Stagger overrides the least time between two automatic resumes.
	Stagger time.Duration
}

// Service is the usage-limit watcher.
type Service struct {
	store    Store
	settings Settings
	accounts Accounts
	notifier Notifier
	reporter Reporter
	sender   Sender
	log      *slog.Logger
	now      func() time.Time
	stagger  time.Duration

	mu      sync.Mutex
	running bool
	// parkedKnown and parked remember the setting last applied to pending stops.
	parkedKnown bool
	parked      bool
}

// New builds the watcher.
func New(d Deps) *Service {
	s := &Service{
		store: d.Store, settings: d.Settings, accounts: d.Accounts, notifier: d.Notifier,
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
	live := make(map[string]bool, len(candidates))
	var pending []domain.LimitStopTurn
	for _, turn := range candidates {
		limit := turn.Limit()
		if limit == nil {
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
		WindowLabel: window, ResetsAt: limit.ResetsAt, DetectedAt: now,
	}
	if s.accounts != nil {
		if key, accErr := s.accounts.SessionAccountID(ctx, rec.ID, rec.Harness); accErr == nil {
			stop.AccountKey = key
		}
	}
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

	// The notification is idempotent by its key, so it goes first: a failure
	// before the stop row exists is retried by the next sweep.
	if s.notifier != nil {
		intent := ports.NotificationIntent{
			Type: domain.NotificationUsageLimit, SessionID: rec.ID, ProjectID: rec.ProjectID,
			CreatedAt: now, SourceKey: "usage_limit:" + stop.WindowKey, SessionDisplayName: rec.DisplayName,
			UsageLimitWindow: window, UsageLimitProvider: providerLabel(rec.Harness),
			UsageLimitAutoResume: enabled && limit.ResetsAt != nil,
		}
		if limit.ResetsAt != nil {
			intent.UsageLimitResetsAt = *limit.ResetsAt
		}
		if err := s.notifier.Notify(ctx, intent); err != nil {
			return fmt.Errorf("notify usage limit: %w", err)
		}
	}
	inserted, err := s.store.InsertUsageLimitStop(ctx, stop)
	if err != nil || !inserted {
		return err
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
		note := ReportNote(rec, window, limit.ResetsAt, reason, now)
		if err := s.reporter.ReportUsageLimit(ctx, rec.ID, note); err != nil {
			s.log.Warn("report usage-limit stop to orchestrator", "session", rec.ID, "err", err)
		} else if err := s.store.MarkUsageLimitStopReported(ctx, turn.TurnID, now); err != nil {
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
	if recent, err := s.store.CountRecentUsageLimitResumes(ctx, rec.ID, now.Add(-repeatWindow)); err != nil {
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
