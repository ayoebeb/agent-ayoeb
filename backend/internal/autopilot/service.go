// Package autopilot runs a task to completion without the user.
//
// The user hands one task to a session (usually a project's orchestrator) and
// switches Autopilot on. AO then keeps the agent on that task: at the end of
// every turn that did not finish it, AO sends the task again with a short status
// block (a Ralph loop), until the agent declares the task done and the
// verification gates pass. Nothing waits for the user meanwhile:
//
//   - a question the agent would ask is decided by the agent and recorded in a
//     log the user reads later;
//   - permission requests from the session and from the project workers started
//     after the run began are answered by a policy (allow inside the workspace,
//     deny the hard-limit list) instead of waiting;
//   - an item that needs a hard limit crossed is parked, with one notification,
//     while everything else carries on.
//
// The loop stops at: the agent's verified done claim, the iteration cap, three
// iterations without any change (stuck), the emergency stop, or the session
// ending. A usage limit pauses it. The hard limits are enforced in the agent's
// instructions and wherever AO can enforce them (the permission policy, the
// decision log, the merge check); AO cannot see what a terminal session runs
// that never asks.
package autopilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

const (
	// defaultIdleGrace is how long a session must sit idle before the watchdog
	// judges a turn the hook did not report.
	defaultIdleGrace = 45 * time.Second
	// defaultMinNudgeGap keeps a fast "ok" reply from turning the loop into a hot
	// spin: two messages to one session are at least this far apart.
	defaultMinNudgeGap = 20 * time.Second
	// stallAfter is how long a sent message may go without the session starting a
	// turn before it is sent again.
	stallAfter = 5 * time.Minute
	// maintenanceTick is how often the watchdog looks at every open run.
	maintenanceTick = 20 * time.Second
	// needsInputGrace is how long a covered session may sit on a prompt before the
	// run is parked. The policy answers an approval in milliseconds, so a prompt
	// still open after this is one Autopilot cannot answer.
	needsInputGrace = 90 * time.Second
	// eventListLimit caps the log lines one read returns.
	eventListLimit = 200
	// sendTimeout bounds one message to a session.
	sendTimeout = 30 * time.Second
)

// Store is the persistence the service needs. *store.Store satisfies it.
type Store interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
	CreateAutopilotRun(ctx context.Context, run domain.AutopilotRun) (domain.AutopilotRun, error)
	GetAutopilotRun(ctx context.Context, id string) (domain.AutopilotRun, bool, error)
	GetOpenAutopilotRunForSession(ctx context.Context, id domain.SessionID) (domain.AutopilotRun, bool, error)
	GetLatestAutopilotRunForSession(ctx context.Context, id domain.SessionID) (domain.AutopilotRun, bool, error)
	ListOpenAutopilotRuns(ctx context.Context) ([]domain.AutopilotRun, error)
	ListActiveAutopilotRunsForProject(ctx context.Context, id domain.ProjectID) ([]domain.AutopilotRun, error)
	UpdateAutopilotRun(ctx context.Context, run domain.AutopilotRun) (domain.AutopilotRun, error)
	CreateAutopilotEvent(ctx context.Context, ev domain.AutopilotEvent) (domain.AutopilotEvent, error)
	GetAutopilotEvent(ctx context.Context, id string) (domain.AutopilotEvent, bool, error)
	ListAutopilotEventsForRun(ctx context.Context, runID string, limit int) ([]domain.AutopilotEvent, error)
	ListOpenParkedAutopilotEvents(ctx context.Context, runID string) ([]domain.AutopilotEvent, error)
	CountOpenParkedAutopilotEventsForSession(ctx context.Context, id domain.SessionID) (int, error)
	ResolveAutopilotEvent(ctx context.Context, id string, at time.Time) (domain.AutopilotEvent, bool, error)
	ListMergedPRNotificationsForProjectSince(ctx context.Context, project domain.ProjectID, since time.Time) ([]domain.NotificationRecord, error)
	AutopilotMaxIterations(ctx context.Context) (int, error)
	SetAutopilotMaxIterations(ctx context.Context, n int, now time.Time) error
	ListPRFactsForSession(ctx context.Context, id domain.SessionID) ([]domain.PRFacts, error)
	ListCurrentHeadReviewRunsForSession(ctx context.Context, id domain.SessionID) ([]domain.CurrentHeadReviewRun, error)
}

// Messenger delivers a message to a session, Chat or terminal alike.
type Messenger interface {
	Send(ctx context.Context, id domain.SessionID, message string) error
}

// Interrupter cancels a session's current turn.
type Interrupter interface {
	Interrupt(ctx context.Context, id domain.SessionID) error
}

// Notifier writes the notifications. *notify.Manager satisfies it.
type Notifier interface {
	Notify(ctx context.Context, intent ports.NotificationIntent) error
	Resolve(ctx context.Context, res ports.NotificationResolution) error
}

// Progress fingerprints a session's workspace (commit, changed files) so the loop
// can tell an iteration that changed nothing from one that did.
type Progress interface {
	Fingerprint(ctx context.Context, rec domain.SessionRecord) (string, error)
}

// ApprovalResolver answers a pending approval of a Chat session.
type ApprovalResolver interface {
	Resolve(ctx context.Context, id domain.SessionID, requestID string, decision ports.ChatDecision) error
}

// UsageLimitCheck reports whether the session's account is on a usage limit.
//
// INTEGRATION POINT (PR #23, usage-limit state and auto-resume): wire this to the
// session's usage-limit state (status usage_limit, Session.usageLimit) once that
// lands. Until then it is nil, which reads as "never limited", and the loop keeps
// going. While it reports true, the loop sends nothing and the run reads paused;
// it resumes by itself when the check turns false.
type UsageLimitCheck func(ctx context.Context, id domain.SessionID) (bool, error)

// TestsVerifier is an optional check that the tests an agent lists as evidence
// really ran, for example against the session's transcript. It returns the
// problems it found; nil means it found none or could not tell. The daemon wires
// none yet: the evidence is recorded and shown as the agent's own account.
type TestsVerifier func(ctx context.Context, rec domain.SessionRecord, evidence string) []string

// Deps configures a Service.
type Deps struct {
	Store        Store
	Messenger    Messenger
	Interrupter  Interrupter
	Notifier     Notifier
	Progress     Progress
	Approvals    ApprovalResolver
	UsageLimited UsageLimitCheck
	VerifyTests  TestsVerifier
	Clock        func() time.Time
	NewID        func() string
	Logger       *slog.Logger
	IdleGrace    time.Duration
	MinNudgeGap  time.Duration
	// Background runs work off the caller's goroutine; it defaults to a go
	// statement that recovers from a panic. Tests run it inline.
	Background func(func())
}

// Service owns Autopilot runs.
type Service struct {
	store        Store
	messenger    Messenger
	interrupter  Interrupter
	notifier     Notifier
	progress     Progress
	approvals    ApprovalResolver
	usageLimited UsageLimitCheck
	verifyTests  TestsVerifier
	clock        func() time.Time
	newID        func() string
	log          *slog.Logger
	idleGrace    time.Duration
	minNudgeGap  time.Duration
	background   func(func())

	// mu serializes every change to a run's row, so the loop, an approval, a
	// decision and a stop never overwrite each other. It is never held across a
	// message to a session.
	mu sync.Mutex
	// judging holds the sessions whose turn end is being judged, so the hook and
	// the watchdog cannot both send the next message for one turn.
	judging map[domain.SessionID]bool
}

// New constructs the service.
func New(d Deps) *Service {
	s := &Service{
		store: d.Store, messenger: d.Messenger, interrupter: d.Interrupter, notifier: d.Notifier,
		progress: d.Progress, approvals: d.Approvals, usageLimited: d.UsageLimited, verifyTests: d.VerifyTests,
		clock: d.Clock, newID: d.NewID, log: d.Logger, idleGrace: d.IdleGrace, minNudgeGap: d.MinNudgeGap,
		background: d.Background, judging: map[domain.SessionID]bool{},
	}
	if s.clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
	}
	if s.newID == nil {
		s.newID = uuid.NewString
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.idleGrace <= 0 {
		s.idleGrace = defaultIdleGrace
	}
	if s.minNudgeGap <= 0 {
		s.minNudgeGap = defaultMinNudgeGap
	}
	if s.background == nil {
		s.background = func(fn func()) {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						s.log.Error("autopilot: background work panicked", "panic", r)
					}
				}()
				fn()
			}()
		}
	}
	return s
}

// View is a session's Autopilot state as the API shows it.
type View struct {
	// Run is the session's open run, or its latest finished one; nil when the
	// session never had one.
	Run *domain.AutopilotRun
	// Events is the run's log, newest first.
	Events []domain.AutopilotEvent
	// Parked is the run's parked items the user has not handled.
	Parked []domain.AutopilotEvent
	// Warnings are plain sentences about gaps Autopilot cannot close for this
	// session, such as a terminal prompt it cannot answer.
	Warnings []string
	// Report is the "what I did while you were away" summary.
	Report *Report
}

// MergedPR is a pull request that merged while a run was going.
type MergedPR struct {
	URL   string
	Title string
	At    time.Time
}

// Report is the return report: what happened while the user was away.
type Report struct {
	RunID              string
	Task               string
	State              domain.AutopilotState
	EndReason          domain.AutopilotEndReason
	Iterations         int
	StartedAt          time.Time
	EndedAt            time.Time
	MergedPRs          []MergedPR
	Decisions          []domain.AutopilotEvent
	Parked             []domain.AutopilotEvent
	Gaps               []string
	PermissionsAllowed int
	PermissionsDenied  int
}

func (s *Service) newEventID() string { return "ape_" + s.newID() }

func sessionNotFound() error { return apierr.NotFound("SESSION_NOT_FOUND", "Unknown session") }

func noOpenRun() error {
	return apierr.Conflict("AUTOPILOT_NOT_ON", "Autopilot is not on for this session", nil)
}

// requireUser refuses a call that came from an agent's shell. Switching Autopilot
// on, off or back on is the user's decision; the CLI sends the calling session on
// every call made from an agent's shell, so an agent can never take it.
func requireUser(ctx context.Context) error {
	if who := caller.From(ctx); who != "" {
		return apierr.Forbidden("AUTOPILOT_USER_ONLY",
			"Only the user can switch Autopilot on, off or back on; an agent finishes its task with `ao autopilot done`")
	}
	return nil
}

// requireAgent refuses a call that did not come from the run's own session:
// only the agent doing the task can record its decisions, park an item or
// declare it done.
func requireAgent(ctx context.Context, id domain.SessionID) error {
	if caller.From(ctx) != string(id) {
		return apierr.Forbidden("AUTOPILOT_AGENT_ONLY",
			"Only the session running this task can do this; run it from that session's shell")
	}
	return nil
}

// ---- starting, stopping, reading ----

// Start puts a task on a session and sends it the first message. At most one run
// per session is open.
func (s *Service) Start(ctx context.Context, id domain.SessionID, task string, maxIterations int) (View, error) {
	if err := requireUser(ctx); err != nil {
		return View{}, err
	}
	task = domain.CleanAutopilotText(task, domain.AutopilotMaxTaskRunes)
	if task == "" {
		return View{}, apierr.Invalid("AUTOPILOT_TASK_REQUIRED", "Say what the task is", nil)
	}
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return View{}, fmt.Errorf("autopilot: read session %s: %w", id, err)
	}
	if !ok {
		return View{}, sessionNotFound()
	}
	if rec.IsTerminated {
		return View{}, apierr.Conflict("SESSION_TERMINATED", "The session has ended", nil)
	}
	limit, err := s.iterationCap(ctx, maxIterations)
	if err != nil {
		return View{}, err
	}
	now := s.clock()

	s.mu.Lock()
	run, err := s.store.CreateAutopilotRun(ctx, domain.AutopilotRun{
		ID: "ap_" + s.newID(), SessionID: id, ProjectID: rec.ProjectID, Task: task,
		State: domain.AutopilotRunning, MaxIterations: limit, StartedAt: now, UpdatedAt: now,
	})
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, store.ErrAutopilotRunOpen) {
			return View{}, apierr.Conflict("AUTOPILOT_ALREADY_ON", "Autopilot is already on for this session", nil)
		}
		return View{}, fmt.Errorf("autopilot: create run: %w", err)
	}
	s.logEventLocked(ctx, run, domain.AutopilotEventStarted, "Autopilot started: "+clip(task, 140), task)
	s.mu.Unlock()

	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.messenger.Send(sendCtx, id, startMessage(run, rec.Kind)); err != nil {
		s.mu.Lock()
		s.finishLocked(ctx, run.ID, domain.AutopilotStopped, domain.AutopilotEndUser, "Autopilot could not start: the session did not take the message ("+clip(err.Error(), 160)+")")
		s.mu.Unlock()
		return View{}, apierr.Unavailable("AUTOPILOT_START_FAILED", "The session could not be reached, so Autopilot did not start: "+clip(err.Error(), 200))
	}
	s.mu.Lock()
	if cur, found, _ := s.store.GetAutopilotRun(ctx, run.ID); found && cur.State == domain.AutopilotRunning {
		cur.LastNudgeAt = s.clock()
		cur.UpdatedAt = cur.LastNudgeAt
		if updated, err := s.store.UpdateAutopilotRun(ctx, cur); err == nil {
			run = updated
		}
	}
	s.mu.Unlock()
	return s.View(ctx, id)
}

// MaxIterations is the default cap on a run, from Settings.
func (s *Service) MaxIterations(ctx context.Context) (int, error) {
	n, err := s.store.AutopilotMaxIterations(ctx)
	if err != nil {
		return 0, fmt.Errorf("autopilot: read the iteration cap: %w", err)
	}
	return n, nil
}

// SetMaxIterations changes the default cap on a run. A run already going keeps
// the cap it started with.
func (s *Service) SetMaxIterations(ctx context.Context, n int) error {
	if n < 1 || n > domain.AutopilotMaxIterationsCeiling {
		return apierr.Invalid("AUTOPILOT_MAX_ITERATIONS_INVALID",
			fmt.Sprintf("maxIterations must be between 1 and %d", domain.AutopilotMaxIterationsCeiling), nil)
	}
	if err := s.store.SetAutopilotMaxIterations(ctx, n, s.clock()); err != nil {
		return fmt.Errorf("autopilot: save the iteration cap: %w", err)
	}
	return nil
}

func (s *Service) iterationCap(ctx context.Context, requested int) (int, error) {
	if requested != 0 {
		if requested < 1 || requested > domain.AutopilotMaxIterationsCeiling {
			return 0, apierr.Invalid("AUTOPILOT_MAX_ITERATIONS_INVALID",
				fmt.Sprintf("maxIterations must be between 1 and %d", domain.AutopilotMaxIterationsCeiling), nil)
		}
		return requested, nil
	}
	n, err := s.store.AutopilotMaxIterations(ctx)
	if err != nil || n < 1 {
		return domain.AutopilotDefaultMaxIterations, nil
	}
	return n, nil
}

// Stop ends the session's run at once and interrupts its current turn: the
// emergency stop. It reports whether a run was open.
func (s *Service) Stop(ctx context.Context, id domain.SessionID) (bool, error) {
	if err := requireUser(ctx); err != nil {
		return false, err
	}
	return s.stop(ctx, id, domain.AutopilotEndEmergencyStop)
}

// StopAll stops every open run, for the one stop control that does not name a
// session (the phone client). It returns how many it stopped.
func (s *Service) StopAll(ctx context.Context) (int, error) {
	if err := requireUser(ctx); err != nil {
		return 0, err
	}
	runs, err := s.store.ListOpenAutopilotRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("autopilot: list open runs: %w", err)
	}
	stopped := 0
	var firstErr error
	for _, run := range runs {
		ok, err := s.stop(ctx, run.SessionID, domain.AutopilotEndEmergencyStop)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if ok {
			stopped++
		}
	}
	return stopped, firstErr
}

func (s *Service) stop(ctx context.Context, id domain.SessionID, reason domain.AutopilotEndReason) (bool, error) {
	s.mu.Lock()
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return false, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok {
		s.mu.Unlock()
		return false, nil
	}
	summary := "Autopilot stopped by the user"
	if reason == domain.AutopilotEndEmergencyStop {
		summary = "Autopilot stopped by the user (stop button)"
	}
	s.finishLocked(ctx, run.ID, domain.AutopilotStopped, reason, summary)
	s.mu.Unlock()
	s.resolveParkedNotifications(ctx, run)

	// The run is already ended, so nothing below can restart it. Cutting the
	// session's current turn and telling it to stop is best-effort.
	if s.interrupter != nil {
		if err := s.interrupter.Interrupt(ctx, id); err != nil {
			s.log.Warn("autopilot: interrupt on stop", "session", id, "err", err)
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.messenger.Send(sendCtx, id, stopMessage()); err != nil {
		s.log.Warn("autopilot: tell the session it was stopped", "session", id, "err", err)
	}
	return true, nil
}

// Resume continues a parked run: the user decided the run may go on. A run that
// parked on its iteration cap gets another cap's worth.
func (s *Service) Resume(ctx context.Context, id domain.SessionID) (View, error) {
	if err := requireUser(ctx); err != nil {
		return View{}, err
	}
	s.mu.Lock()
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil {
		s.mu.Unlock()
		return View{}, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok {
		s.mu.Unlock()
		return View{}, noOpenRun()
	}
	if run.State != domain.AutopilotParked {
		s.mu.Unlock()
		return View{}, apierr.Conflict("AUTOPILOT_NOT_PARKED", "Autopilot is not parked for this session", nil)
	}
	if run.EndReason == domain.AutopilotEndMaxIterations {
		extra, _ := s.iterationCap(ctx, 0)
		run.MaxIterations = min(run.MaxIterations+extra, domain.AutopilotMaxIterationsCeiling*2)
	}
	run.State, run.EndReason, run.Stagnant = domain.AutopilotRunning, domain.AutopilotEndNone, 0
	run.UpdatedAt = s.clock()
	run, err = s.store.UpdateAutopilotRun(ctx, run)
	if err != nil {
		s.mu.Unlock()
		return View{}, fmt.Errorf("autopilot: resume run: %w", err)
	}
	s.logEventLocked(ctx, run, domain.AutopilotEventResumed, "Autopilot resumed by the user", "")
	s.mu.Unlock()
	s.resolveParkedNotifications(ctx, run)

	if err := s.sendIteration(ctx, run, "Autopilot was resumed by the user."); err != nil {
		return View{}, apierr.Unavailable("AUTOPILOT_RESUME_FAILED", "The session could not be reached: "+clip(err.Error(), 200))
	}
	return s.View(ctx, id)
}

// sendIteration sends the next iteration message and records that it went out.
func (s *Service) sendIteration(ctx context.Context, run domain.AutopilotRun, lead string) error {
	msg := iterationMessage(run, run.Iteration+1)
	if lead != "" {
		msg = resumeMessage(run, run.Iteration+1, lead)
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.messenger.Send(sendCtx, run.SessionID, msg); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok, _ := s.store.GetAutopilotRun(ctx, run.ID); ok && cur.State == domain.AutopilotRunning {
		cur.LastNudgeAt = s.clock()
		cur.UpdatedAt = cur.LastNudgeAt
		_, _ = s.store.UpdateAutopilotRun(ctx, cur)
	}
	return nil
}

// View reads the session's Autopilot state: its open run, or its latest finished
// one, with the log and the return report.
func (s *Service) View(ctx context.Context, id domain.SessionID) (View, error) {
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return View{}, fmt.Errorf("autopilot: read session %s: %w", id, err)
	}
	if !ok {
		return View{}, sessionNotFound()
	}
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err == nil && !ok {
		run, ok, err = s.store.GetLatestAutopilotRunForSession(ctx, id)
	}
	if err != nil {
		return View{}, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok {
		return View{}, nil
	}
	events, err := s.store.ListAutopilotEventsForRun(ctx, run.ID, eventListLimit)
	if err != nil {
		return View{}, fmt.Errorf("autopilot: read log: %w", err)
	}
	parked, err := s.store.ListOpenParkedAutopilotEvents(ctx, run.ID)
	if err != nil {
		return View{}, fmt.Errorf("autopilot: read parked items: %w", err)
	}
	report, err := s.buildReport(ctx, run, events)
	if err != nil {
		return View{}, err
	}
	v := View{Run: &run, Events: events, Parked: parked, Report: &report}
	if run.State.Open() {
		v.Warnings = warnings(rec)
	}
	return v, nil
}

// List returns every open run (running, paused or parked), for the badges.
func (s *Service) List(ctx context.Context) ([]domain.AutopilotRun, error) {
	runs, err := s.store.ListOpenAutopilotRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("autopilot: list open runs: %w", err)
	}
	return runs, nil
}

// warnings names what Autopilot cannot do for this session. A terminal session
// keeps its permission prompts inside the terminal, where no API can answer them.
func warnings(rec domain.SessionRecord) []string {
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		return nil
	}
	switch rec.Metadata.Permissions {
	case domain.PermissionModeAuto, domain.PermissionModeBypassPermissions:
		return nil
	}
	return []string{"This is a terminal session: a permission prompt inside it cannot be answered by AO and would stop the run. Start the session in auto or bypass-permissions mode, or use a Chat session."}
}

func (s *Service) buildReport(ctx context.Context, run domain.AutopilotRun, events []domain.AutopilotEvent) (Report, error) {
	rep := Report{
		RunID: run.ID, Task: run.Task, State: run.State, EndReason: run.EndReason, Iterations: run.Iteration,
		StartedAt: run.StartedAt, EndedAt: run.EndedAt,
		PermissionsAllowed: run.PermissionsAllowed, PermissionsDenied: run.PermissionsDenied,
	}
	// Oldest first reads as a story.
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case domain.AutopilotEventDecision:
			rep.Decisions = append(rep.Decisions, events[i])
		case domain.AutopilotEventParked:
			rep.Parked = append(rep.Parked, events[i])
		}
	}
	if run.ProjectID != "" {
		merged, err := s.store.ListMergedPRNotificationsForProjectSince(ctx, run.ProjectID, run.StartedAt)
		if err != nil {
			return Report{}, fmt.Errorf("autopilot: read merged PRs: %w", err)
		}
		for _, n := range merged {
			if !run.EndedAt.IsZero() && n.CreatedAt.After(run.EndedAt) {
				continue
			}
			rep.MergedPRs = append(rep.MergedPRs, MergedPR{URL: n.PRURL, Title: n.Title, At: n.CreatedAt})
		}
	}
	if run.CIGap != "" {
		rep.Gaps = append(rep.Gaps, "CI could not run: "+run.CIGap)
	}
	if run.State == domain.AutopilotDone && run.Evidence != "" {
		rep.Gaps = append(rep.Gaps, "The tests listed as evidence are the agent's own account; AO did not re-run them.")
	}
	switch run.EndReason {
	case domain.AutopilotEndMaxIterations:
		rep.Gaps = append(rep.Gaps, fmt.Sprintf("The run used all %d iterations without finishing.", run.MaxIterations))
	case domain.AutopilotEndStuck:
		rep.Gaps = append(rep.Gaps, fmt.Sprintf("The run changed nothing for %d iterations in a row.", domain.AutopilotStuckIterations))
	}
	if run.State != domain.AutopilotDone && strings.TrimSpace(run.LastGap) != "" {
		rep.Gaps = append(rep.Gaps, "Still open: "+clip(run.LastGap, 300))
	}
	return rep, nil
}

// ---- log helpers (callers hold s.mu) ----

func (s *Service) logEventLocked(ctx context.Context, run domain.AutopilotRun, kind domain.AutopilotEventKind, summary, detail string) domain.AutopilotEvent {
	ev, err := s.store.CreateAutopilotEvent(ctx, domain.AutopilotEvent{
		ID: s.newEventID(), RunID: run.ID, SessionID: run.SessionID, Kind: kind, Iteration: run.Iteration,
		Summary:   domain.CleanAutopilotText(summary, domain.AutopilotMaxFieldRunes),
		Detail:    domain.CleanAutopilotText(detail, domain.AutopilotMaxFieldRunes),
		CreatedAt: s.clock(),
	})
	if err != nil {
		s.log.Warn("autopilot: write log line", "run", run.ID, "kind", kind, "err", err)
	}
	return ev
}

// finishLocked moves a run to a terminal state and logs why.
func (s *Service) finishLocked(ctx context.Context, runID string, state domain.AutopilotState, reason domain.AutopilotEndReason, summary string) {
	run, ok, err := s.store.GetAutopilotRun(ctx, runID)
	if err != nil || !ok || !run.State.Open() {
		return
	}
	now := s.clock()
	run.State, run.EndReason, run.UpdatedAt, run.EndedAt = state, reason, now, now
	run, err = s.store.UpdateAutopilotRun(ctx, run)
	if err != nil {
		s.log.Warn("autopilot: end run", "run", runID, "err", err)
		return
	}
	kind := domain.AutopilotEventStopped
	if state == domain.AutopilotDone {
		kind = domain.AutopilotEventDone
	}
	s.logEventLocked(ctx, run, kind, summary, "")
}

// fingerprintOf hashes the parts that show progress.
func fingerprintOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
