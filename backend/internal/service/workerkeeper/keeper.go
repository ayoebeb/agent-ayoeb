// Package workerkeeper keeps AO's worker sessions running. A worker whose agent
// exited without anyone stopping it (Ayo restarted under it, or the agent
// crashed) is resumed once, and the orchestrator is told when that fails or does
// not hold. A worker that sits idle or exited with unfinished work and no report
// for too long gets one automatic report, through the missing-report fallback.
package workerkeeper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// Messages sent to a resumed worker.
const (
	RestartMessage = "Ayo restarted while you were working. Continue where you left off."
	CrashMessage   = "Your agent process stopped unexpectedly and Ayo restarted it. Continue where you left off."
)

const (
	// DefaultSweepEvery is how often the keeper looks at the workers.
	DefaultSweepEvery = 30 * time.Second
	// DefaultStagger spaces resumes so a restart does not start every agent at
	// once.
	DefaultStagger = 5 * time.Second
	// CrashWindow is how soon after an automatic resume a second exit counts as
	// "could not be resumed" and is reported instead of resumed again.
	CrashWindow = 10 * time.Minute
	// StallAfter is how long a worker may sit exited or idle with unfinished work
	// and no report before the watchdog reports it.
	StallAfter = 20 * time.Minute
	// MaxAge bounds how old a worker's last activity can be for the keeper to
	// act on it. Older sessions were left alone on purpose or long ago finished.
	MaxAge = 24 * time.Hour
)

// Store is what the keeper reads.
type Store interface {
	ListWatchedWorkerIDs(context.Context) ([]domain.SessionID, error)
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	AgentStopMark(context.Context, domain.SessionID) (time.Time, bool, error)
	ListPendingUsageLimitStops(context.Context) ([]domain.UsageLimitStop, error)
}

// Reports is the missing-report fallback: it decides whether work is unfinished
// and files the automatic reports.
type Reports interface {
	Unfinished(context.Context, domain.SessionRecord) (bool, error)
	FileStopped(ctx context.Context, rec domain.SessionRecord, reason string) error
	FileStalled(ctx context.Context, rec domain.SessionRecord, state string, idleFor time.Duration) error
}

// Busy reports whether the provider still has background work for a session.
type Busy interface {
	BackgroundWorkRunning(domain.SessionID) bool
}

// Deps configures a Keeper.
type Deps struct {
	Store   Store
	Reports Reports
	Busy    Busy // optional
	// Resume relaunches an exited agent in its live session, as
	// `ao session resume-agent` does.
	Resume func(context.Context, domain.SessionID) error
	// Send delivers a message to the worker.
	Send func(ctx context.Context, id domain.SessionID, message string) error
	// Stagger spaces resumes within one sweep. Zero disables the spacing.
	Stagger time.Duration
	Now     func() time.Time
	// Sleep waits for d or the context; tests replace it.
	Sleep  func(context.Context, time.Duration) error
	Logger *slog.Logger
}

type sessionState struct {
	// seenExitedAt is when a sweep first saw the current exit. A crash is acted on
	// only at a later sweep, so an exit that is part of a daemon shutdown is gone
	// before the keeper looks again.
	seenExitedAt time.Time
	// resumedAt is when the keeper last resumed the session.
	resumedAt time.Time
	// stopReported is set once the orchestrator was told the worker stopped; the
	// keeper then leaves the session alone until it has run on its own.
	stopReported bool
	// stalledFor is the activity time of the stretch the watchdog already reported.
	stalledFor time.Time
	// alive is set by each sweep that sees the session running.
	alive bool
}

// Keeper resumes and watches workers.
type Keeper struct {
	d   Deps
	log *slog.Logger
	st  map[domain.SessionID]*sessionState
}

// New builds a Keeper.
func New(d Deps) *Keeper {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleepContext
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Keeper{d: d, log: log, st: map[domain.SessionID]*sessionState{}}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run sweeps once when ready closes, as the restart recovery (every exited
// worker with unfinished work is taken as stopped by the restart), then every
// interval until ctx ends. Pass the channel that closes once startup
// reconciliation settled which sessions are live.
func (k *Keeper) Run(ctx context.Context, ready <-chan struct{}, every time.Duration) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	if every <= 0 {
		every = DefaultSweepEvery
	}
	if err := k.Sweep(ctx, true); err != nil && ctx.Err() == nil {
		k.log.Warn("worker keeper: restart sweep failed", "error", err)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := k.Sweep(ctx, false); err != nil && ctx.Err() == nil {
				k.log.Warn("worker keeper: sweep failed", "error", err)
			}
		}
	}
}

// Sweep looks at every watched worker once. boot marks the pass after a daemon
// start: its exits were caused by the restart, so they are resumed at once with
// the restart message.
func (k *Keeper) Sweep(ctx context.Context, boot bool) error {
	ids, err := k.d.Store.ListWatchedWorkerIDs(ctx)
	if err != nil {
		return err
	}
	limited := k.limitedSessions(ctx)
	for _, s := range k.st {
		s.alive = false
	}
	resumed := false
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec, ok, err := k.d.Store.GetSession(ctx, id)
		if err != nil || !ok || rec.IsTerminated || rec.Kind != domain.KindWorker || rec.HibernatedAt != nil {
			continue
		}
		switch rec.Activity.State {
		case domain.ActivityExited:
			k.exited(ctx, rec, boot, limited[id], &resumed)
		case domain.ActivityIdle:
			if !limited[id] {
				k.stalled(ctx, rec, "idle")
			}
			k.markAlive(id)
		default:
			k.markAlive(id)
		}
	}
	// A session that ran past the crash window since its resume is healthy: a
	// later exit is a new crash, resumed and reported afresh.
	now := k.d.Now()
	for id, s := range k.st {
		if s.alive && (s.resumedAt.IsZero() || now.Sub(s.resumedAt) >= CrashWindow) {
			s.resumedAt, s.stopReported, s.seenExitedAt = time.Time{}, false, time.Time{}
		}
		if s.resumedAt.IsZero() && !s.stopReported && s.seenExitedAt.IsZero() && s.stalledFor.IsZero() {
			delete(k.st, id)
		}
	}
	return nil
}

func (k *Keeper) state(id domain.SessionID) *sessionState {
	s := k.st[id]
	if s == nil {
		s = &sessionState{}
		k.st[id] = s
	}
	return s
}

func (k *Keeper) markAlive(id domain.SessionID) {
	if s, ok := k.st[id]; ok {
		s.alive = true
		s.seenExitedAt = time.Time{}
	}
}

// limitedSessions is the sessions stopped by a provider usage limit; the
// usage-limit service resumes those after the reset.
func (k *Keeper) limitedSessions(ctx context.Context) map[domain.SessionID]bool {
	stops, err := k.d.Store.ListPendingUsageLimitStops(ctx)
	if err != nil {
		k.log.Warn("worker keeper: list usage-limit stops", "error", err)
		return nil
	}
	out := make(map[domain.SessionID]bool, len(stops))
	for _, s := range stops {
		out[s.SessionID] = true
	}
	return out
}

// deliberate reports whether AO stopped the agent on purpose for this exit.
func (k *Keeper) deliberate(ctx context.Context, rec domain.SessionRecord) bool {
	at, ok, err := k.d.Store.AgentStopMark(ctx, rec.ID)
	if err != nil {
		k.log.Warn("worker keeper: read agent stop mark", "session", rec.ID, "error", err)
		return true // when unsure, never resume a session someone may have stopped
	}
	return ok && !at.Before(rec.Activity.LastActivityAt.Add(-2*time.Second))
}

func (k *Keeper) exited(ctx context.Context, rec domain.SessionRecord, boot, limited bool, resumed *bool) {
	if limited || k.deliberate(ctx, rec) {
		return
	}
	now := k.d.Now()
	if last := rec.Activity.LastActivityAt; !last.IsZero() && now.Sub(last) > MaxAge {
		return
	}
	unfinished, err := k.d.Reports.Unfinished(ctx, rec)
	if err != nil {
		k.log.Warn("worker keeper: read reports", "session", rec.ID, "error", err)
		return
	}
	if !unfinished {
		return
	}
	s := k.state(rec.ID)
	if s.stopReported {
		k.stalled(ctx, rec, "exited")
		return
	}
	if !boot && s.seenExitedAt.IsZero() {
		s.seenExitedAt = now
		return
	}
	if !s.resumedAt.IsZero() && now.Sub(s.resumedAt) < CrashWindow {
		k.reportStopped(ctx, rec, s, fmt.Sprintf("the agent exited again %d minutes after it was resumed", int(now.Sub(s.resumedAt).Minutes())))
		return
	}
	if *resumed && k.d.Stagger > 0 {
		if k.d.Sleep(ctx, k.d.Stagger) != nil {
			return
		}
	}
	*resumed = true
	err = k.d.Resume(ctx, rec.ID)
	switch {
	case err == nil:
	case ctx.Err() != nil, errors.Is(err, sessionmanager.ErrResumeInProgress):
		return // try again next sweep
	case errors.Is(err, sessionmanager.ErrTerminated), errors.Is(err, sessionmanager.ErrNotFound),
		errors.Is(err, sessionmanager.ErrAgentNotExited):
		return
	default:
		k.log.Warn("worker keeper: resume failed", "session", rec.ID, "error", err)
		s.resumedAt = now
		k.reportStopped(ctx, rec, s, err.Error())
		return
	}
	s.resumedAt = now
	s.seenExitedAt = time.Time{}
	message := CrashMessage
	if boot {
		message = RestartMessage
	}
	k.log.Info("worker keeper: resumed a stopped worker", "session", rec.ID, "afterRestart", boot)
	if err := k.d.Send(ctx, rec.ID, message); err != nil {
		k.log.Warn("worker keeper: could not tell the resumed worker to continue", "session", rec.ID, "error", err)
	}
}

func (k *Keeper) reportStopped(ctx context.Context, rec domain.SessionRecord, s *sessionState, reason string) {
	s.stopReported = true
	if err := k.d.Reports.FileStopped(ctx, rec, reason); err != nil {
		k.log.Warn("worker keeper: stopped-worker report failed", "session", rec.ID, "error", err)
	}
}

// stalled is the watchdog: a worker in state (idle or exited) for StallAfter
// with unfinished work and no report gets one automatic report.
func (k *Keeper) stalled(ctx context.Context, rec domain.SessionRecord, state string) {
	last := rec.Activity.LastActivityAt
	if last.IsZero() {
		return
	}
	idleFor := k.d.Now().Sub(last)
	if idleFor < StallAfter || idleFor > MaxAge {
		return
	}
	if s := k.st[rec.ID]; s != nil && s.stalledFor.Equal(last) {
		return
	}
	if state == "idle" && k.d.Busy != nil && k.d.Busy.BackgroundWorkRunning(rec.ID) {
		return
	}
	unfinished, err := k.d.Reports.Unfinished(ctx, rec)
	if err != nil || !unfinished {
		return
	}
	if err := k.d.Reports.FileStalled(ctx, rec, state, idleFor); err != nil {
		k.log.Warn("worker keeper: watchdog report failed", "session", rec.ID, "error", err)
		return
	}
	k.state(rec.ID).stalledFor = last
}
