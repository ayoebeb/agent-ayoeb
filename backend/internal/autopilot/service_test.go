package autopilot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// ---- fakes ----

type sent struct {
	ID  domain.SessionID
	Msg string
}

type fakeMessenger struct {
	mu   sync.Mutex
	sent []sent
	fail error
}

func (m *fakeMessenger) Send(_ context.Context, id domain.SessionID, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return m.fail
	}
	m.sent = append(m.sent, sent{id, msg})
	return nil
}

func (m *fakeMessenger) to(id domain.SessionID) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range m.sent {
		if s.ID == id {
			out = append(out, s.Msg)
		}
	}
	return out
}

type fakeNotifier struct {
	mu       sync.Mutex
	intents  []ports.NotificationIntent
	resolved []ports.NotificationResolution
	seen     map[string]bool
	fail     error
}

func (n *fakeNotifier) Notify(_ context.Context, in ports.NotificationIntent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	if n.seen == nil {
		n.seen = map[string]bool{}
	}
	// The real notification table is idempotent on the source key.
	if in.SourceKey != "" && n.seen[in.SourceKey] {
		return nil
	}
	n.seen[in.SourceKey] = true
	n.intents = append(n.intents, in)
	return nil
}

func (n *fakeNotifier) Resolve(_ context.Context, r ports.NotificationResolution) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resolved = append(n.resolved, r)
	return nil
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.intents)
}

type fakeInterrupter struct{ ids []domain.SessionID }

func (i *fakeInterrupter) Interrupt(_ context.Context, id domain.SessionID) error {
	i.ids = append(i.ids, id)
	return nil
}

type fakeProgress struct {
	mu sync.Mutex
	fp map[domain.SessionID]string
}

func (p *fakeProgress) Fingerprint(_ context.Context, rec domain.SessionRecord) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fp[rec.ID], nil
}

func (p *fakeProgress) set(id domain.SessionID, fp string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fp[id] = fp
}

type fakeApprovals struct {
	mu        sync.Mutex
	resolved  []string
	decisions []string
}

func (a *fakeApprovals) Resolve(_ context.Context, id domain.SessionID, requestID string, d ports.ChatDecision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolved = append(a.resolved, string(id)+"/"+requestID)
	a.decisions = append(a.decisions, d.ID)
	return nil
}

// gateStore overrides the pull-request and review facts the gates read.
type gateStore struct {
	Store
	facts   map[domain.SessionID][]domain.PRFacts
	reviews map[domain.SessionID][]domain.CurrentHeadReviewRun
}

func (g *gateStore) ListPRFactsForSession(ctx context.Context, id domain.SessionID) ([]domain.PRFacts, error) {
	if f, ok := g.facts[id]; ok {
		return f, nil
	}
	return g.Store.ListPRFactsForSession(ctx, id)
}

func (g *gateStore) ListCurrentHeadReviewRunsForSession(ctx context.Context, id domain.SessionID) ([]domain.CurrentHeadReviewRun, error) {
	if r, ok := g.reviews[id]; ok {
		return r, nil
	}
	return g.Store.ListCurrentHeadReviewRunsForSession(ctx, id)
}

// ---- environment ----

type env struct {
	t         *testing.T
	ctx       context.Context
	db        *sqlite.Store
	gates     *gateStore
	svc       *Service
	msgs      *fakeMessenger
	notes     *fakeNotifier
	stops     *fakeInterrupter
	progress  *fakeProgress
	approvals *fakeApprovals
	base      time.Time
	now       time.Time
	limited   bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := sqlitetest.MustOpen(t)
	e := &env{
		t: t, ctx: context.Background(), db: db,
		gates: &gateStore{
			Store:   db,
			facts:   map[domain.SessionID][]domain.PRFacts{},
			reviews: map[domain.SessionID][]domain.CurrentHeadReviewRun{},
		},
		msgs: &fakeMessenger{}, notes: &fakeNotifier{}, stops: &fakeInterrupter{},
		progress:  &fakeProgress{fp: map[domain.SessionID]string{}},
		approvals: &fakeApprovals{},
		base:      time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
	}
	e.now = e.base
	ids := 0
	e.svc = New(Deps{
		Store: e.gates, Messenger: e.msgs, Interrupter: e.stops, Notifier: e.notes,
		Progress: e.progress, Approvals: e.approvals,
		UsageLimited: func(context.Context, domain.SessionID) (bool, error) { return e.limited, nil },
		Clock:        func() time.Time { return e.now },
		NewID:        func() string { ids++; return string(rune('a'+ids/26)) + string(rune('a'+ids%26)) },
		Background:   func(fn func()) { fn() },
		MinNudgeGap:  time.Second,
		IdleGrace:    time.Second,
	})
	if err := db.UpsertProject(e.ctx, domain.ProjectRecord{ID: "mer", Path: "/tmp/mer", RegisteredAt: e.base}); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) session(kind domain.SessionKind, at time.Time, mode domain.SessionMode) domain.SessionRecord {
	e.t.Helper()
	rec, err := e.db.CreateSession(e.ctx, domain.SessionRecord{
		ProjectID: "mer", Kind: kind, Harness: domain.HarnessClaudeCode, Mode: mode,
		DisplayName: string(kind) + "-x",
		Activity:    domain.Activity{State: domain.ActivityIdle, LastActivityAt: at},
		Metadata:    domain.SessionMetadata{Branch: "ao/x", WorkspacePath: "/ws/" + string(kind)},
		CreatedAt:   at, UpdatedAt: at,
	})
	if err != nil {
		e.t.Fatalf("create %s: %v", kind, err)
	}
	return rec
}

func (e *env) orchestrator() domain.SessionRecord {
	return e.session(domain.KindOrchestrator, e.base, domain.SessionModeChat)
}

// activity sets the session's activity as lifecycle would on a turn boundary.
func (e *env) activity(id domain.SessionID, state domain.ActivityState, at time.Time) {
	e.t.Helper()
	rec, ok, err := e.db.GetSession(e.ctx, id)
	if err != nil || !ok {
		e.t.Fatalf("get session: %v ok=%v", err, ok)
	}
	rec.Activity = domain.Activity{State: state, LastActivityAt: at}
	if applied, err := e.db.UpdateSessionFromActivitySignal(e.ctx, rec, rec.Revision); err != nil || !applied {
		e.t.Fatalf("set activity: applied=%v err=%v", applied, err)
	}
}

// turnEnds simulates one turn that ended minutes after the last message.
func (e *env) turnEnds(id domain.SessionID) {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	e.activity(id, domain.ActivityIdle, e.now)
	e.now = e.now.Add(time.Second * 2)
	rec, _, _ := e.db.GetSession(e.ctx, id)
	e.svc.SessionTurnEnded(rec)
}

func asUser(ctx context.Context) context.Context { return ctx }
func asAgent(ctx context.Context, id domain.SessionID) context.Context {
	return caller.With(ctx, string(id))
}

func (e *env) start(id domain.SessionID, task string) View {
	e.t.Helper()
	v, err := e.svc.Start(asUser(e.ctx), id, task, 0)
	if err != nil {
		e.t.Fatalf("start: %v", err)
	}
	return v
}

func (e *env) run(id domain.SessionID) domain.AutopilotRun {
	e.t.Helper()
	run, ok, err := e.db.GetLatestAutopilotRunForSession(e.ctx, id)
	if err != nil || !ok {
		e.t.Fatalf("get run: ok=%v err=%v", ok, err)
	}
	return run
}

// hasCode reports whether err is a structured API error with the given code.
func hasCode(err error, code string) bool {
	var api *apierr.Error
	return errors.As(err, &api) && api.Code == code
}

// ---- starting ----

func TestStartSendsTheTaskTheRulesAndTheHardLimits(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	v := e.start(orch.ID, "Make the importer twice as fast and keep the tests green")

	if v.Run == nil || v.Run.State != domain.AutopilotRunning || v.Run.MaxIterations != 30 || v.Run.Iteration != 0 {
		t.Fatalf("run = %+v, want running with the default cap of 30", v.Run)
	}
	msgs := e.msgs.to(orch.ID)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the one start message", len(msgs))
	}
	for _, want := range []string{
		"Autopilot is ON", "Make the importer twice as fast", "ao autopilot criteria", "ao autopilot done --evidence",
		"ao autopilot decide", "ao autopilot park", "--ci-cannot-run", "at most 30", "orchestrator", "at most 2 workers", "never Opus",
		"never spend money", "never disable, skip or weaken CI", "never use administrator or elevated rights",
		"never message people outside AO", "never change logins, tokens, passwords or credentials",
	} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("start message lacks %q:\n%s", want, msgs[0])
		}
	}
	if len(v.Events) != 1 || v.Events[0].Kind != domain.AutopilotEventStarted {
		t.Fatalf("log = %+v, want one started line", v.Events)
	}
	if e.run(orch.ID).LastNudgeAt.IsZero() {
		t.Fatal("the start message must count as the last message sent")
	}
}

func TestStartRefusals(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()

	if _, err := e.svc.Start(e.ctx, orch.ID, "   ", 0); !hasCode(err, "AUTOPILOT_TASK_REQUIRED") {
		t.Errorf("empty task err = %v", err)
	}
	if _, err := e.svc.Start(e.ctx, "nope", "task", 0); !hasCode(err, "SESSION_NOT_FOUND") {
		t.Errorf("unknown session err = %v", err)
	}
	if _, err := e.svc.Start(e.ctx, orch.ID, "task", 9999); !hasCode(err, "AUTOPILOT_MAX_ITERATIONS_INVALID") {
		t.Errorf("huge cap err = %v", err)
	}
	// Autopilot is the user's switch: an agent's shell can never turn it on.
	if _, err := e.svc.Start(asAgent(e.ctx, orch.ID), orch.ID, "task", 0); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Errorf("agent start err = %v, want forbidden", err)
	}
	e.start(orch.ID, "first task")
	if _, err := e.svc.Start(e.ctx, orch.ID, "second task", 0); !hasCode(err, "AUTOPILOT_ALREADY_ON") {
		t.Errorf("second start err = %v", err)
	}
	if len(e.msgs.to(orch.ID)) != 1 {
		t.Errorf("a refused start must send nothing; messages = %d", len(e.msgs.to(orch.ID)))
	}
}

func TestStartThatCannotReachTheSessionLeavesNoRunningRun(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.msgs.fail = errors.New("composer is busy")
	_, err := e.svc.Start(e.ctx, orch.ID, "task", 0)
	if !hasCode(err, "AUTOPILOT_START_FAILED") {
		t.Fatalf("err = %v, want AUTOPILOT_START_FAILED", err)
	}
	if run := e.run(orch.ID); run.State != domain.AutopilotStopped {
		t.Fatalf("state = %s, want stopped so the session is free for a new run", run.State)
	}
	e.msgs.fail = nil
	e.start(orch.ID, "task again")
}

func TestStartUsesTheMaxIterationsOverrideAndWarnsAboutTerminalPrompts(t *testing.T) {
	e := newEnv(t)
	term := e.session(domain.KindOrchestrator, e.base, domain.SessionModeTUI)
	v, err := e.svc.Start(e.ctx, term.ID, "task", 7)
	if err != nil {
		t.Fatal(err)
	}
	if v.Run.MaxIterations != 7 {
		t.Fatalf("cap = %d, want the override 7", v.Run.MaxIterations)
	}
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "terminal session") {
		t.Fatalf("warnings = %v, want the terminal-prompt gap", v.Warnings)
	}
}

// ---- the loop ----

func TestTurnEndSendsTheTaskAgainWithTheStatusBlock(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "Fix the flaky importer test")
	if _, err := e.svc.SetCriteria(asAgent(e.ctx, orch.ID), orch.ID, "Done when the importer test passes 20 runs in a row"); err != nil {
		t.Fatal(err)
	}
	e.progress.set(orch.ID, "A")
	e.turnEnds(orch.ID)

	msgs := e.msgs.to(orch.ID)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want start + the first iteration", len(msgs))
	}
	for _, want := range []string{
		"Autopilot iteration 2 of 30", "Fix the flaky importer test",
		"Done when the importer test passes 20 runs in a row", "ao autopilot done --evidence", "Do not ask the user anything",
	} {
		if !strings.Contains(msgs[1], want) {
			t.Errorf("iteration message lacks %q:\n%s", want, msgs[1])
		}
	}
	run := e.run(orch.ID)
	if run.Iteration != 1 || run.State != domain.AutopilotRunning || run.Stagnant != 0 {
		t.Fatalf("run = %+v", run)
	}
}

func TestTurnEndDoesNothingUnlessTheSessionIsIdleAndSomethingChangedSinceTheLastMessage(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")

	// A session that is still working is not at a turn boundary.
	e.now = e.now.Add(time.Minute)
	e.activity(orch.ID, domain.ActivityActive, e.now)
	rec, _, _ := e.db.GetSession(e.ctx, orch.ID)
	e.svc.SessionTurnEnded(rec)
	// The hook firing twice for one turn sends once: the second sees no turn
	// since the message.
	e.activity(orch.ID, domain.ActivityIdle, e.now.Add(time.Minute))
	e.now = e.now.Add(3 * time.Minute)
	rec, _, _ = e.db.GetSession(e.ctx, orch.ID)
	e.svc.SessionTurnEnded(rec)
	e.now = e.now.Add(time.Minute)
	e.svc.SessionTurnEnded(rec)

	if got := len(e.msgs.to(orch.ID)); got != 2 {
		t.Fatalf("messages = %d, want start + exactly one iteration", got)
	}
}

func TestLoopNeverSendsTwiceWithinTheMinimumGap(t *testing.T) {
	e := newEnv(t)
	e.svc.minNudgeGap = time.Hour
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	e.turnEnds(orch.ID)
	if got := len(e.msgs.to(orch.ID)); got != 1 {
		t.Fatalf("messages = %d, want only the start message inside the gap", got)
	}
}

func TestRunParksAsStuckAfterThreeIterationsWithoutChangeAndNotifiesOnce(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "Make it work")
	e.progress.set(orch.ID, "same")

	// Turn 1 differs from "no fingerprint yet"; turns 2 and 3 repeat it; turn 4 is
	// the third repeat in a row.
	for i := 0; i < 3; i++ {
		e.turnEnds(orch.ID)
		if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
			t.Fatalf("after turn %d state = %s, want running", i+1, got)
		}
	}
	e.turnEnds(orch.ID)
	run := e.run(orch.ID)
	if run.State != domain.AutopilotParked || run.EndReason != domain.AutopilotEndStuck {
		t.Fatalf("run = %s/%s, want parked/stuck", run.State, run.EndReason)
	}
	if got := len(e.msgs.to(orch.ID)); got != 4 {
		t.Fatalf("messages = %d, want start + 3 iterations and none after parking", got)
	}
	if e.notes.count() != 1 || e.notes.intents[0].Type != domain.NotificationAutopilotParked {
		t.Fatalf("notifications = %+v, want exactly one autopilot_parked", e.notes.intents)
	}
	// A parked run does not send again, however many turns end.
	e.turnEnds(orch.ID)
	e.turnEnds(orch.ID)
	if got := len(e.msgs.to(orch.ID)); got != 4 || e.notes.count() != 1 {
		t.Fatalf("after more turn ends: messages = %d, notifications = %d", got, e.notes.count())
	}
}

func TestProgressResetsTheStuckCounter(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	for i, fp := range []string{"a", "a", "a", "b", "b", "b"} {
		e.progress.set(orch.ID, fp)
		e.turnEnds(orch.ID)
		if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
			t.Fatalf("turn %d (%s): state = %s, want running while each stretch is shorter than the limit", i+1, fp, got)
		}
	}
}

func TestRunParksWhenItUsesAllItsIterations(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if _, err := e.svc.Start(e.ctx, orch.ID, "task", 3); err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{"1", "2", "3"} {
		e.progress.set(orch.ID, fp)
		e.turnEnds(orch.ID)
	}
	run := e.run(orch.ID)
	if run.State != domain.AutopilotParked || run.EndReason != domain.AutopilotEndMaxIterations || run.Iteration != 3 {
		t.Fatalf("run = %s/%s iteration %d, want parked/max_iterations at 3", run.State, run.EndReason, run.Iteration)
	}
	if e.notes.count() != 1 {
		t.Fatalf("notifications = %d, want one", e.notes.count())
	}
	if last := e.msgs.to(orch.ID); !strings.Contains(last[len(last)-1], "This is the last iteration") {
		t.Fatalf("the message before the cap must say it is the last:\n%s", last[len(last)-1])
	}
}

func TestResumeContinuesAParkedRunAndExtendsAnExhaustedCap(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if _, err := e.svc.Start(e.ctx, orch.ID, "task", 2); err != nil {
		t.Fatal(err)
	}
	for _, fp := range []string{"1", "2"} {
		e.progress.set(orch.ID, fp)
		e.turnEnds(orch.ID)
	}
	if e.run(orch.ID).State != domain.AutopilotParked {
		t.Fatal("setup: the run should be parked")
	}
	if _, err := e.svc.Resume(asAgent(e.ctx, orch.ID), orch.ID); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Fatalf("agent resume err = %v, want forbidden", err)
	}
	before := len(e.msgs.to(orch.ID))
	if _, err := e.svc.Resume(e.ctx, orch.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	run := e.run(orch.ID)
	if run.State != domain.AutopilotRunning || run.MaxIterations != 32 || run.Stagnant != 0 {
		t.Fatalf("run = %+v, want running with 30 more iterations", run)
	}
	msgs := e.msgs.to(orch.ID)
	if len(msgs) != before+1 || !strings.Contains(msgs[len(msgs)-1], "resumed by the user") {
		t.Fatalf("resume must send the task again; messages = %d", len(msgs))
	}
	if _, err := e.svc.Resume(e.ctx, orch.ID); !hasCode(err, "AUTOPILOT_NOT_PARKED") {
		t.Fatalf("resume of a running run err = %v", err)
	}
	// Resuming the run closes the notification about it.
	if len(e.notes.resolved) == 0 {
		t.Error("the parked notification must be resolved when nothing is left parked")
	}
}

func TestUsageLimitPausesTheLoopAndItContinuesAfterTheReset(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	e.limited = true
	e.turnEnds(orch.ID)
	if got := e.run(orch.ID).State; got != domain.AutopilotPausedUsageLimit {
		t.Fatalf("state = %s, want paused_usage_limit", got)
	}
	if got := len(e.msgs.to(orch.ID)); got != 1 {
		t.Fatalf("messages = %d: nothing may be sent while the account is limited", got)
	}
	e.svc.maintain(e.ctx)
	if got := e.run(orch.ID).State; got != domain.AutopilotPausedUsageLimit {
		t.Fatalf("still limited, state = %s", got)
	}

	e.limited = false
	e.now = e.now.Add(time.Hour)
	e.svc.maintain(e.ctx)
	if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
		t.Fatalf("after the reset state = %s, want running", got)
	}
	msgs := e.msgs.to(orch.ID)
	if len(msgs) != 2 || !strings.Contains(msgs[1], "usage limit has reset") {
		t.Fatalf("messages = %v, want one resume message", msgs)
	}
}

func TestOrchestratorWaitsForItsWorkersInsteadOfBurningIterations(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "ship the feature")
	worker := e.session(domain.KindWorker, e.now.Add(time.Second), domain.SessionModeChat)
	e.activity(worker.ID, domain.ActivityActive, e.now.Add(time.Second))

	e.turnEnds(orch.ID)
	if got := e.run(orch.ID).Iteration; got != 0 || len(e.msgs.to(orch.ID)) != 1 {
		t.Fatalf("iteration = %d, messages = %d: an orchestrator with active workers is waiting on them", got, len(e.msgs.to(orch.ID)))
	}

	e.activity(worker.ID, domain.ActivityIdle, e.now)
	e.turnEnds(orch.ID)
	if got := e.run(orch.ID).Iteration; got != 1 {
		t.Fatalf("iteration = %d, want 1 once the workers are idle", got)
	}
}

func TestWatchdogJudgesATurnTheHookMissedAndResendsAMessageThatStartedNoTurn(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	e.progress.set(orch.ID, "1")

	// A turn ended but the hook never reported it.
	e.now = e.now.Add(time.Minute)
	e.activity(orch.ID, domain.ActivityIdle, e.now)
	e.now = e.now.Add(time.Minute)
	e.svc.maintain(e.ctx)
	if got := e.run(orch.ID).Iteration; got != 1 {
		t.Fatalf("iteration = %d, want the watchdog to judge the missed turn", got)
	}

	// The message went out, but the session never started a turn for it.
	e.progress.set(orch.ID, "2")
	e.now = e.now.Add(stallAfter + time.Minute)
	e.svc.maintain(e.ctx)
	if got := e.run(orch.ID).Iteration; got != 2 {
		t.Fatalf("iteration = %d, want a resend after the stall", got)
	}
}

func TestWatchdogEndsTheRunOfASessionThatIsGone(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	rec, _, _ := e.db.GetSession(e.ctx, orch.ID)
	rec.IsTerminated = true
	if err := e.db.UpdateSession(e.ctx, rec); err != nil {
		t.Fatalf("terminate the session: %v", err)
	}
	e.svc.maintain(e.ctx)
	run := e.run(orch.ID)
	if run.State != domain.AutopilotStopped || run.EndReason != domain.AutopilotEndSessionEnded {
		t.Fatalf("run = %s/%s, want stopped/session_ended", run.State, run.EndReason)
	}
}

// ---- the emergency stop ----

func TestStopEndsTheRunInterruptsTheTurnAndTellsTheAgent(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")

	if _, err := e.svc.Stop(asAgent(e.ctx, orch.ID), orch.ID); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Fatalf("agent stop err = %v: an agent must not switch itself off", err)
	}
	stopped, err := e.svc.Stop(e.ctx, orch.ID)
	if err != nil || !stopped {
		t.Fatalf("stop = %v, %v", stopped, err)
	}
	run := e.run(orch.ID)
	if run.State != domain.AutopilotStopped || run.EndReason != domain.AutopilotEndEmergencyStop || run.EndedAt.IsZero() {
		t.Fatalf("run = %+v", run)
	}
	if len(e.stops.ids) != 1 || e.stops.ids[0] != orch.ID {
		t.Fatalf("interrupts = %v, want the session's current turn cut", e.stops.ids)
	}
	msgs := e.msgs.to(orch.ID)
	if last := msgs[len(msgs)-1]; !strings.Contains(last, "stopped by the user") {
		t.Fatalf("last message = %q, want the stop notice", last)
	}
	// Nothing restarts it.
	e.turnEnds(orch.ID)
	if got := len(e.msgs.to(orch.ID)); got != len(msgs) {
		t.Fatalf("a stopped run sent %d more message(s)", got-len(msgs))
	}
	if again, err := e.svc.Stop(e.ctx, orch.ID); err != nil || again {
		t.Fatalf("second stop = %v, %v, want false nil", again, err)
	}
}

func TestStopAllStopsEveryOpenRun(t *testing.T) {
	e := newEnv(t)
	a, b := e.orchestrator(), e.session(domain.KindWorker, e.base, domain.SessionModeChat)
	e.start(a.ID, "task a")
	e.start(b.ID, "task b")
	n, err := e.svc.StopAll(e.ctx)
	if err != nil || n != 2 {
		t.Fatalf("stopped = %d, %v, want 2", n, err)
	}
	if open, _ := e.svc.List(e.ctx); len(open) != 0 {
		t.Fatalf("open runs after stop-all = %d", len(open))
	}
	if _, err := e.svc.StopAll(asAgent(e.ctx, a.ID)); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Fatalf("agent stop-all err = %v", err)
	}
}

// ---- the wait hook ----

func TestWaitDeclaredWhileRunningIsDivertedAndQuotedBackNotSentToTheUser(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")

	handled, err := e.svc.OnWaitDeclared(e.ctx, orch.ID, "Merge PR #42 now, or wait for the second review?")
	if err != nil || !handled {
		t.Fatalf("handled = %v, %v, want true", handled, err)
	}
	if e.notes.count() != 0 {
		t.Fatalf("notifications = %d: an Autopilot orchestrator never notifies the user for a wait", e.notes.count())
	}
	e.progress.set(orch.ID, "1")
	e.turnEnds(orch.ID)
	msgs := e.msgs.to(orch.ID)
	if last := msgs[len(msgs)-1]; !strings.Contains(last, "You asked the user: Merge PR #42 now") || !strings.Contains(last, "decide it yourself") {
		t.Fatalf("the next message must quote the open question:\n%s", last)
	}

	// Deciding it clears the open question.
	if _, err := e.svc.Decide(asAgent(e.ctx, orch.ID), orch.ID, "Merge PR #42 now?", "Wait for the second review", "The exact head has only one approval so far"); err != nil {
		t.Fatal(err)
	}
	if gap := e.run(orch.ID).LastGap; strings.Contains(gap, "You asked the user") {
		t.Fatalf("LastGap = %q, want the answered question cleared", gap)
	}
}

func TestWaitDeclaredWithoutAnOpenRunIsNotTaken(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if handled, err := e.svc.OnWaitDeclared(e.ctx, orch.ID, "Ship it?"); err != nil || handled {
		t.Fatalf("no run: handled = %v, %v, want false nil so the user is notified as before", handled, err)
	}
	e.start(orch.ID, "task")
	if _, err := e.svc.Stop(e.ctx, orch.ID); err != nil {
		t.Fatal(err)
	}
	if handled, _ := e.svc.OnWaitDeclared(e.ctx, orch.ID, "Ship it?"); handled {
		t.Fatal("a stopped run must not take a wait")
	}
}

func TestWaitDeclaredOnAParkedRunStillReachesTheUser(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if _, err := e.svc.Start(e.ctx, orch.ID, "task", 1); err != nil {
		t.Fatal(err)
	}
	e.progress.set(orch.ID, "1")
	e.turnEnds(orch.ID)
	if e.run(orch.ID).State != domain.AutopilotParked {
		t.Fatal("setup: parked")
	}
	if handled, _ := e.svc.OnWaitDeclared(e.ctx, orch.ID, "What now?"); handled {
		t.Fatal("a parked run waits for the user, so its question must reach them")
	}
}

// ---- decisions and parked items ----

func TestDecideRecordsTheDecisionAndRefusesAHardLimit(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)

	ev, err := e.svc.Decide(agent, orch.ID, "Refactor or patch?", "Patch the one function", "The refactor touches 40 files and the task is one bug")
	if err != nil || ev.Kind != domain.AutopilotEventDecision || !strings.Contains(ev.Summary, "Patch the one function") || !strings.Contains(ev.Detail, "40 files") {
		t.Fatalf("decision = %+v, %v", ev, err)
	}
	if _, err := e.svc.Decide(agent, orch.ID, "Need more CI minutes?", "Upgrade the paid plan", "It is quicker"); !hasCode(err, "AUTOPILOT_HARD_LIMIT") {
		t.Fatalf("a choice that spends money err = %v, want AUTOPILOT_HARD_LIMIT", err)
	}
	if _, err := e.svc.Decide(agent, orch.ID, "", "x", "y"); !hasCode(err, "AUTOPILOT_DECISION_INCOMPLETE") {
		t.Fatalf("incomplete err = %v", err)
	}
	if _, err := e.svc.Decide(e.ctx, orch.ID, "q", "c", "w"); !hasCode(err, "AUTOPILOT_AGENT_ONLY") {
		t.Fatalf("a call from outside the session err = %v", err)
	}
	if _, err := e.svc.Decide(asAgent(e.ctx, "someone-else"), orch.ID, "q", "c", "w"); !hasCode(err, "AUTOPILOT_AGENT_ONLY") {
		t.Fatalf("another session's call err = %v", err)
	}
	v, _ := e.svc.View(e.ctx, orch.ID)
	decisions := 0
	for _, ev := range v.Events {
		if ev.Kind == domain.AutopilotEventDecision {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("logged decisions = %d, want only the one accepted", decisions)
	}
}

func TestUndoAsksTheAgentToReverseTheDecisionAndMarksItUndoneOnce(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	dec, err := e.svc.Decide(agent, orch.ID, "Refactor or patch?", "Patch the one function", "The refactor touches 40 files")
	if err != nil {
		t.Fatal(err)
	}
	before := len(e.msgs.to(orch.ID))

	if _, err := e.svc.UndoDecision(agent, dec.ID); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Fatalf("an agent must not undo its own decision: %v", err)
	}
	if _, err := e.svc.UndoDecision(e.ctx, "missing"); !hasCode(err, "AUTOPILOT_DECISION_NOT_FOUND") {
		t.Fatalf("unknown decision err = %v", err)
	}
	undone, err := e.svc.UndoDecision(e.ctx, dec.ID)
	if err != nil || undone.ResolvedAt.IsZero() {
		t.Fatalf("undo = %+v, %v; want the decision stamped as undone", undone, err)
	}
	msgs := e.msgs.to(orch.ID)
	if len(msgs) != before+1 {
		t.Fatalf("messages = %d, want one undo request to the agent", len(msgs)-before)
	}
	last := msgs[len(msgs)-1]
	for _, want := range []string{"undo a decision", "Patch the one function", "40 files", "ao autopilot decide"} {
		if !strings.Contains(last, want) {
			t.Errorf("undo message %q lacks %q", last, want)
		}
	}
	if _, err := e.svc.UndoDecision(e.ctx, dec.ID); err != nil || len(e.msgs.to(orch.ID)) != before+1 {
		t.Fatalf("undoing twice must not message again: %v", err)
	}

	// A parked item is not a decision; it has its own route.
	parked, _, _ := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "Pay for the plan", "x")
	if _, err := e.svc.UndoDecision(e.ctx, parked.ID); !hasCode(err, "AUTOPILOT_DECISION_NOT_FOUND") {
		t.Fatalf("undo of a parked item err = %v", err)
	}
	// If the agent cannot be reached nothing is marked, so the user can retry.
	dec2, _ := e.svc.Decide(agent, orch.ID, "Q2?", "A2", "because")
	e.msgs.fail = errors.New("session gone")
	if _, err := e.svc.UndoDecision(e.ctx, dec2.ID); !hasCode(err, "AUTOPILOT_UNDO_UNDELIVERED") {
		t.Fatalf("undelivered err = %v", err)
	}
	e.msgs.fail = nil
	if ev, err := e.svc.UndoDecision(e.ctx, dec2.ID); err != nil || ev.ResolvedAt.IsZero() {
		t.Fatalf("retry after a failed delivery = %+v, %v", ev, err)
	}
}

func TestDecideWithoutARunIsRefused(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if _, err := e.svc.Decide(asAgent(e.ctx, orch.ID), orch.ID, "q", "c", "w"); !hasCode(err, "AUTOPILOT_NOT_ON") {
		t.Fatalf("err = %v, want AUTOPILOT_NOT_ON", err)
	}
}

func TestParkRecordsOneItemAndSendsOneNotificationAndTheRunCarriesOn(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)

	ev, already, err := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "Upgrade the CI plan", "The free minutes ran out")
	if err != nil || already || ev.Kind != domain.AutopilotEventParked {
		t.Fatalf("park = %+v already=%v err=%v", ev, already, err)
	}
	if e.notes.count() != 1 {
		t.Fatalf("notifications = %d, want exactly one", e.notes.count())
	}
	in := e.notes.intents[0]
	if in.Type != domain.NotificationAutopilotParked || in.SessionID != orch.ID || !strings.Contains(in.Detail, "Upgrade the CI plan") ||
		in.SourceKey != domain.AutopilotParkedSourceKeyPrefix+ev.ID {
		t.Fatalf("intent = %+v", in)
	}
	if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
		t.Fatalf("state = %s: parking one item must not stop the run", got)
	}

	// The same open item again is a no-op, not a second notification.
	again, already, err := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "upgrade the ci plan", "Still no free minutes")
	if err != nil || !already || again.ID != ev.ID || e.notes.count() != 1 {
		t.Fatalf("repeat = %+v already=%v err=%v notifications=%d", again, already, err, e.notes.count())
	}
	// A different item is its own parked item and its own notification.
	if _, _, err := e.svc.Park(agent, orch.ID, domain.HardLimitMessaging, "Email the customer", "They asked for an update"); err != nil || e.notes.count() != 2 {
		t.Fatalf("second item err=%v notifications=%d", err, e.notes.count())
	}

	if _, _, err := e.svc.Park(agent, orch.ID, "whatever", "x", "y"); !hasCode(err, "AUTOPILOT_LIMIT_INVALID") {
		t.Fatalf("bad limit err = %v", err)
	}
	if _, _, err := e.svc.Park(agent, orch.ID, domain.HardLimitAdmin, "", "y"); !hasCode(err, "AUTOPILOT_PARK_INCOMPLETE") {
		t.Fatalf("incomplete err = %v", err)
	}
}

func TestParkRetryAfterAFailedNotificationStillReachesTheUser(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)

	e.notes.fail = errors.New("hub down")
	if _, _, err := e.svc.Park(agent, orch.ID, domain.HardLimitCredentials, "Rotate the deploy token", "It expired"); err == nil {
		t.Fatal("a notification that fails must surface, or the user is never told")
	}
	e.notes.fail = nil
	if _, already, err := e.svc.Park(agent, orch.ID, domain.HardLimitCredentials, "Rotate the deploy token", "It expired"); err != nil || !already {
		t.Fatalf("retry already=%v err=%v", already, err)
	}
	if e.notes.count() != 1 {
		t.Fatalf("notifications = %d, want the retry to deliver the one", e.notes.count())
	}
}

func TestResolvingParkedItemsClosesTheNotificationOnlyWhenNoneAreLeft(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	first, _, _ := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "Pay for the plan", "x")
	second, _, _ := e.svc.Park(agent, orch.ID, domain.HardLimitAdmin, "Install the driver as admin", "y")

	if _, err := e.svc.ResolveParked(asAgent(e.ctx, orch.ID), first.ID); !hasCode(err, "AUTOPILOT_USER_ONLY") {
		t.Fatalf("an agent must not mark its own parked item handled: %v", err)
	}
	if _, err := e.svc.ResolveParked(e.ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.notes.resolved) != 0 {
		t.Fatalf("resolved = %v: one item is still open", e.notes.resolved)
	}
	if _, err := e.svc.ResolveParked(e.ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.notes.resolved) != 1 || e.notes.resolved[0].Type != domain.NotificationAutopilotParked || e.notes.resolved[0].SessionID != orch.ID {
		t.Fatalf("resolved = %+v, want the parked notification closed once", e.notes.resolved)
	}
	if _, err := e.svc.ResolveParked(e.ctx, "missing"); !hasCode(err, "AUTOPILOT_ITEM_NOT_FOUND") {
		t.Fatalf("unknown item err = %v", err)
	}
	v, _ := e.svc.View(e.ctx, orch.ID)
	if len(v.Parked) != 0 {
		t.Fatalf("open parked items = %d, want none", len(v.Parked))
	}
}

// ---- done and the gates ----

func prFact(n int, ci domain.CIState) domain.PRFacts {
	return domain.PRFacts{URL: "https://github.com/o/r/pull/" + string(rune('0'+n)), Number: n, CI: ci, HeadSHA: "abc123", SourceBranch: "ao/x"}
}

func review(url string, verdict domain.ReviewVerdict, at time.Time) domain.CurrentHeadReviewRun {
	return domain.CurrentHeadReviewRun{PRURL: url, Verdict: verdict, Status: domain.ReviewRunComplete, CreatedAt: at}
}

func TestDoneNeedsCriteriaEvidenceAndTheAgentItself(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)

	if _, err := e.svc.Done(e.ctx, orch.ID, "go test ./... ok", ""); !hasCode(err, "AUTOPILOT_AGENT_ONLY") {
		t.Fatalf("user-side done err = %v", err)
	}
	if _, err := e.svc.Done(agent, orch.ID, "  ", ""); !hasCode(err, "AUTOPILOT_EVIDENCE_REQUIRED") {
		t.Fatalf("no evidence err = %v", err)
	}
	res, err := e.svc.Done(agent, orch.ID, "go test ./... passed", "")
	if err != nil || res.Accepted || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "acceptance criteria") {
		t.Fatalf("done without criteria = %+v, %v, want one failure", res, err)
	}
	if got := e.run(orch.ID); got.State != domain.AutopilotRunning || !strings.Contains(got.LastGap, "acceptance criteria") {
		t.Fatalf("a refused claim keeps the run going with the reason open: %+v", got)
	}
}

func TestDoneWithEverythingInPlaceFinishesTheRun(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	if _, err := e.svc.SetCriteria(agent, orch.ID, "Done when go test passes"); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Done(agent, orch.ID, "go test ./... → ok, go vet → clean", "")
	if err != nil || !res.Accepted {
		t.Fatalf("done = %+v, %v", res, err)
	}
	run := e.run(orch.ID)
	if run.State != domain.AutopilotDone || run.EndReason != domain.AutopilotEndGoalDone || run.EndedAt.IsZero() || run.Evidence == "" {
		t.Fatalf("run = %+v", run)
	}
	e.turnEnds(orch.ID)
	if got := len(e.msgs.to(orch.ID)); got != 1 {
		t.Fatalf("a finished run sent %d more message(s)", got-1)
	}
	// The session is free for the next task.
	e.start(orch.ID, "next task")
}

func TestDoneGateRequiresAnApprovedReviewOfTheExactHeadAndGreenCI(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	if _, err := e.svc.SetCriteria(agent, orch.ID, "Done when the PR is approved and green"); err != nil {
		t.Fatal(err)
	}
	pr := prFact(7, domain.CIPassing)

	// Open PR, no review of the current head.
	e.gates.facts[orch.ID] = []domain.PRFacts{pr}
	res, _ := e.svc.Done(agent, orch.ID, "tests ok", "")
	if res.Accepted || len(res.Failures) != 1 || !strings.Contains(res.Failures[0], "no completed AO review") {
		t.Fatalf("no review: %+v", res)
	}

	// The review asked for changes.
	e.gates.reviews[orch.ID] = []domain.CurrentHeadReviewRun{review(pr.URL, domain.VerdictChangesRequested, e.now)}
	res, _ = e.svc.Done(agent, orch.ID, "tests ok", "")
	if res.Accepted || !strings.Contains(strings.Join(res.Failures, " "), "asked for changes") {
		t.Fatalf("changes requested: %+v", res)
	}

	// Approved, but CI is not green.
	e.gates.reviews[orch.ID] = []domain.CurrentHeadReviewRun{review(pr.URL, domain.VerdictApproved, e.now)}
	e.gates.facts[orch.ID] = []domain.PRFacts{prFact(7, domain.CIFailing)}
	res, _ = e.svc.Done(agent, orch.ID, "tests ok", "")
	if res.Accepted || !strings.Contains(strings.Join(res.Failures, " "), "CI for PR #7 is failing") ||
		!strings.Contains(strings.Join(res.Failures, " "), "--ci-cannot-run") {
		t.Fatalf("failing CI: %+v", res)
	}
	if got := e.run(orch.ID); got.State != domain.AutopilotRunning || got.CIGap == "" && got.Evidence == "" {
		t.Fatalf("a refused claim must keep the run going: %+v", got)
	}

	// CI cannot start (billing): recorded as a gap, never as a pass.
	res, err := e.svc.Done(agent, orch.ID, "tests ok", "GitHub Actions is blocked by a billing limit")
	if err != nil || !res.Accepted || len(res.Gaps) != 1 || !strings.Contains(res.Gaps[0], "billing limit") {
		t.Fatalf("CI cannot run: %+v, %v", res, err)
	}
	rep := e.run(orch.ID)
	if rep.CIGap == "" {
		t.Fatal("the gap must be stored on the run")
	}
	v, _ := e.svc.View(e.ctx, orch.ID)
	if !strings.Contains(strings.Join(v.Report.Gaps, " "), "CI could not run: GitHub Actions is blocked") {
		t.Fatalf("the return report must show the gap: %v", v.Report.Gaps)
	}
}

func TestDoneGateAcceptsApprovedAndGreenAndIgnoresMergedPRs(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	_, _ = e.svc.SetCriteria(agent, orch.ID, "Done when merged")
	open := prFact(8, domain.CIPassing)
	merged := prFact(5, domain.CIFailing)
	merged.Merged = true
	e.gates.facts[orch.ID] = []domain.PRFacts{merged, open}
	e.gates.reviews[orch.ID] = []domain.CurrentHeadReviewRun{review(open.URL, domain.VerdictApproved, e.now)}
	res, err := e.svc.Done(agent, orch.ID, "tests ok", "")
	if err != nil || !res.Accepted {
		t.Fatalf("done = %+v, %v", res, err)
	}
}

func TestDoneGateAlsoCoversTheWorkersOfAnOrchestratorRun(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "ship it")
	agent := asAgent(e.ctx, orch.ID)
	_, _ = e.svc.SetCriteria(agent, orch.ID, "Done when the worker's PR is approved")
	worker := e.session(domain.KindWorker, e.now.Add(time.Second), domain.SessionModeChat)
	e.gates.facts[worker.ID] = []domain.PRFacts{prFact(9, domain.CIPassing)}

	res, _ := e.svc.Done(agent, orch.ID, "tests ok", "")
	if res.Accepted || !strings.Contains(strings.Join(res.Failures, " "), "PR #9 has no completed AO review") {
		t.Fatalf("a worker's unreviewed PR must block the orchestrator's done: %+v", res)
	}
	// A worker from before the run is not this run's business.
	old := e.session(domain.KindWorker, e.base.Add(-time.Hour), domain.SessionModeChat)
	e.gates.facts[old.ID] = []domain.PRFacts{prFact(3, domain.CIFailing)}
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{review(prFact(9, domain.CIPassing).URL, domain.VerdictApproved, e.now)}
	if res, err := e.svc.Done(agent, orch.ID, "tests ok", ""); err != nil || !res.Accepted {
		t.Fatalf("done = %+v, %v", res, err)
	}
}

func TestTestsVerifierProblemsFailTheClaim(t *testing.T) {
	e := newEnv(t)
	e.svc.verifyTests = func(context.Context, domain.SessionRecord, string) []string {
		return []string{"`go test ./...` does not appear in the session's commands"}
	}
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)
	_, _ = e.svc.SetCriteria(agent, orch.ID, "criteria")
	res, _ := e.svc.Done(agent, orch.ID, "go test ./... ok", "")
	if res.Accepted || !strings.Contains(strings.Join(res.Failures, " "), "does not appear") {
		t.Fatalf("done = %+v", res)
	}
}

// ---- permission policy ----

func approval(id, tool, kind, input string) ApprovalNotice {
	return ApprovalNotice{
		RequestID: id, Summary: tool,
		Detail: []byte(`{"providerToolName":"` + tool + `","toolKind":"` + kind + `","input":` + input + `}`),
		Decisions: []ports.ChatDecisionOption{
			{ID: "allow", Kind: ports.ChatDecisionAllowOnce},
			{ID: "allow_always", Kind: ports.ChatDecisionAllowAlways},
			{ID: "reject", Kind: ports.ChatDecisionRejectOnce},
		},
	}
}

func TestApprovalsOfTheRunsOwnSessionAreAnsweredByPolicy(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")

	e.svc.HandleApproval(e.ctx, orch.ID, approval("r1", "Bash", "execute", `{"command":"go test ./..."}`))
	e.svc.HandleApproval(e.ctx, orch.ID, approval("r2", "Edit", "edit", `{"file_path":"src/app.go"}`))
	e.svc.HandleApproval(e.ctx, orch.ID, approval("r3", "Bash", "execute", `{"command":"git push origin main"}`))

	if got := strings.Join(e.approvals.decisions, ","); got != "allow,allow,reject" {
		t.Fatalf("decisions = %s, want allow,allow,reject", got)
	}
	run := e.run(orch.ID)
	if run.PermissionsAllowed != 2 || run.PermissionsDenied != 1 {
		t.Fatalf("counts = %d allowed, %d denied", run.PermissionsAllowed, run.PermissionsDenied)
	}
	// The denied hard-limit item is parked with one notification, never waited on.
	if e.notes.count() != 1 || e.notes.intents[0].Type != domain.NotificationAutopilotParked {
		t.Fatalf("notifications = %+v, want one parked item", e.notes.intents)
	}
	v, _ := e.svc.View(e.ctx, orch.ID)
	if len(v.Parked) != 1 || !strings.Contains(v.Parked[0].Summary, "git push origin main") || !strings.Contains(v.Parked[0].Detail, "main_push") {
		t.Fatalf("parked = %+v", v.Parked)
	}
	// Asking again for the same thing parks nothing new and notifies nobody again.
	e.svc.HandleApproval(e.ctx, orch.ID, approval("r4", "Bash", "execute", `{"command":"git push origin main"}`))
	if e.notes.count() != 1 {
		t.Fatalf("a repeated denial notified again: %d", e.notes.count())
	}
}

func TestADenialThatIsNotAHardLimitIsLoggedNotParked(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	e.svc.HandleApproval(e.ctx, orch.ID, approval("r1", "Write", "edit", `{"file_path":"/etc/hosts"}`))
	if got := strings.Join(e.approvals.decisions, ","); got != "reject" {
		t.Fatalf("decisions = %s, want reject", got)
	}
	if e.notes.count() != 0 {
		t.Fatalf("a write outside the workspace is not a hard limit; notifications = %d", e.notes.count())
	}
	v, _ := e.svc.View(e.ctx, orch.ID)
	if len(v.Events) < 2 || v.Events[0].Kind != domain.AutopilotEventPermissionDenied {
		t.Fatalf("log = %+v, want a permission_denied line", v.Events)
	}
}

func TestWorkersStartedAfterTheRunAreCoveredAndOthersAreNot(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	oldWorker := e.session(domain.KindWorker, e.base.Add(-time.Hour), domain.SessionModeChat)
	e.start(orch.ID, "task")
	newWorker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)

	e.svc.HandleApproval(e.ctx, newWorker.ID, approval("w1", "Bash", "execute", `{"command":"npm test"}`))
	e.svc.HandleApproval(e.ctx, oldWorker.ID, approval("w2", "Bash", "execute", `{"command":"npm test"}`))
	if got := strings.Join(e.approvals.resolved, ","); got != string(newWorker.ID)+"/w1" {
		t.Fatalf("answered = %s, want only the worker started after the run", got)
	}
	if got := e.run(orch.ID).PermissionsAllowed; got != 1 {
		t.Fatalf("allowed = %d", got)
	}
}

func TestNoApprovalIsAnsweredWithoutARunningRun(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	worker := e.session(domain.KindWorker, e.base.Add(time.Minute), domain.SessionModeChat)
	e.svc.HandleApproval(e.ctx, worker.ID, approval("x", "Bash", "execute", `{"command":"npm test"}`))
	e.start(orch.ID, "task")
	if _, err := e.svc.Stop(e.ctx, orch.ID); err != nil {
		t.Fatal(err)
	}
	e.svc.HandleApproval(e.ctx, orch.ID, approval("y", "Bash", "execute", `{"command":"npm test"}`))
	e.svc.HandleApproval(e.ctx, worker.ID, approval("z", "Bash", "execute", `{"command":"npm test"}`))
	if len(e.approvals.resolved) != 0 {
		t.Fatalf("answered = %v: with no running run every approval waits for the user", e.approvals.resolved)
	}
}

func TestAParkedRunAnswersNoApprovals(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if _, err := e.svc.Start(e.ctx, orch.ID, "task", 1); err != nil {
		t.Fatal(err)
	}
	e.progress.set(orch.ID, "1")
	e.turnEnds(orch.ID)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("p", "Bash", "execute", `{"command":"npm test"}`))
	if len(e.approvals.resolved) != 0 {
		t.Fatalf("a run that parked waits for the user; answered = %v", e.approvals.resolved)
	}
}

func TestMergeIsAllowedOnlyWithAnApprovedReviewOfTheExactHeadAndGreenChecks(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	pr := prFact(12, domain.CIPassing)
	e.gates.facts[worker.ID] = []domain.PRFacts{pr}
	merge := approval("m1", "Bash", "execute", `{"command":"gh pr merge 12 --squash"}`)

	// No review yet: refused and parked, never merged.
	e.svc.HandleApproval(e.ctx, orch.ID, merge)
	if got := strings.Join(e.approvals.decisions, ","); got != "reject" {
		t.Fatalf("unreviewed merge decisions = %s, want reject", got)
	}
	if e.notes.count() != 1 {
		t.Fatalf("a refused merge is parked once: notifications = %d", e.notes.count())
	}

	// Approved on the exact head and green: allowed.
	e.gates.reviews[worker.ID] = []domain.CurrentHeadReviewRun{review(pr.URL, domain.VerdictApproved, e.now)}
	merge.RequestID = "m2"
	e.svc.HandleApproval(e.ctx, orch.ID, merge)
	if got := strings.Join(e.approvals.decisions, ","); got != "reject,allow" {
		t.Fatalf("approved merge decisions = %s, want reject,allow", got)
	}

	// Approved but CI failing: refused again.
	e.gates.facts[worker.ID] = []domain.PRFacts{prFact(12, domain.CIFailing)}
	merge.RequestID = "m3"
	e.svc.HandleApproval(e.ctx, orch.ID, merge)
	if got := strings.Join(e.approvals.decisions, ","); got != "reject,allow,reject" {
		t.Fatalf("merge on failing CI decisions = %s, want reject,allow,reject", got)
	}

	// A merge AO cannot match to a tracked PR is refused too.
	unknown := approval("m4", "Bash", "execute", `{"command":"gh pr merge 999"}`)
	e.svc.HandleApproval(e.ctx, orch.ID, unknown)
	if got := e.approvals.decisions[len(e.approvals.decisions)-1]; got != "reject" {
		t.Fatalf("unmatched merge decision = %s, want reject", got)
	}
}

func TestPickDecisionFallsBackToWellKnownIDs(t *testing.T) {
	plain := []ports.ChatDecisionOption{{ID: "accept"}, {ID: "decline"}}
	if d, ok := pickDecision(plain, true); !ok || d.ID != "accept" {
		t.Errorf("allow = %+v %v", d, ok)
	}
	if d, ok := pickDecision(plain, false); !ok || d.ID != "decline" {
		t.Errorf("deny = %+v %v", d, ok)
	}
	if _, ok := pickDecision([]ports.ChatDecisionOption{{ID: "weird"}}, true); ok {
		t.Error("no known decision must report not found, so the request is left for the user")
	}
	alwaysOnly := []ports.ChatDecisionOption{{ID: "a", Kind: ports.ChatDecisionAllowAlways}, {ID: "r", Kind: ports.ChatDecisionRejectAlways}}
	if d, _ := pickDecision(alwaysOnly, true); d.ID != "a" {
		t.Errorf("allow-always fallback = %+v", d)
	}
}

// ---- the return report ----

func TestReportSummarisesDecisionsParkedItemsAndMergedPRs(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "ship the importer")
	agent := asAgent(e.ctx, orch.ID)
	if _, err := e.svc.Decide(agent, orch.ID, "Patch or refactor?", "Patch", "Smaller risk"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "Buy more CI minutes", "Ran out"); err != nil {
		t.Fatal(err)
	}
	// A PR merged in the project while the run was going.
	e.now = e.now.Add(time.Hour)
	if _, _, err := e.db.CreateNotification(e.ctx, domain.NotificationRecord{
		ID: "ntf_m", SessionID: orch.ID, ProjectID: "mer", PRURL: "https://github.com/o/r/pull/4", Type: domain.NotificationPRMerged,
		Title: "PR #4 merged", Status: domain.NotificationUnread, CreatedAt: e.now,
	}); err != nil {
		t.Fatal(err)
	}
	e.progress.set(orch.ID, "x")
	_, _ = e.svc.SetCriteria(agent, orch.ID, "criteria")
	if res, err := e.svc.Done(agent, orch.ID, "go test ok", ""); err != nil || !res.Accepted {
		t.Fatalf("done: %+v %v", res, err)
	}

	v, err := e.svc.View(e.ctx, orch.ID)
	if err != nil || v.Report == nil {
		t.Fatalf("view = %+v, %v", v, err)
	}
	rep := v.Report
	if rep.State != domain.AutopilotDone || rep.Task != "ship the importer" || len(rep.Decisions) != 1 || len(rep.Parked) != 1 ||
		len(rep.MergedPRs) != 1 || rep.MergedPRs[0].URL != "https://github.com/o/r/pull/4" {
		t.Fatalf("report = %+v", rep)
	}
	if len(v.Parked) != 1 {
		t.Fatalf("parked items needing the user = %d, want 1", len(v.Parked))
	}
	if !strings.Contains(strings.Join(rep.Gaps, " "), "AO did not re-run") {
		t.Fatalf("the report must say the test evidence is the agent's own account: %v", rep.Gaps)
	}
}

func TestViewOfASessionWithoutARunIsEmpty(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	v, err := e.svc.View(e.ctx, orch.ID)
	if err != nil || v.Run != nil || v.Report != nil {
		t.Fatalf("view = %+v, %v", v, err)
	}
	if _, err := e.svc.View(e.ctx, "nope"); !hasCode(err, "SESSION_NOT_FOUND") {
		t.Fatalf("unknown session err = %v", err)
	}
}

// ---- progress ----

func TestGitProgressFingerprintChangesWithCommitStatusAndDiff(t *testing.T) {
	outputs := map[string]string{"rev-parse": "abc\n", "status": " M a.go\n", "diff": "+x\n"}
	g := GitProgress{Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(outputs[args[0]]), nil
	}}
	rec := domain.SessionRecord{Metadata: domain.SessionMetadata{WorkspacePath: "/ws"}}
	base, err := g.Fingerprint(context.Background(), rec)
	if err != nil || base == "" {
		t.Fatalf("fingerprint = %q, %v", base, err)
	}
	same, _ := g.Fingerprint(context.Background(), rec)
	if same != base {
		t.Fatal("an unchanged workspace must fingerprint the same")
	}
	for _, change := range []string{"rev-parse", "status", "diff"} {
		old := outputs[change]
		outputs[change] = old + "changed"
		if fp, _ := g.Fingerprint(context.Background(), rec); fp == base {
			t.Errorf("a change in %s did not change the fingerprint", change)
		}
		outputs[change] = old
	}
	if fp, err := g.Fingerprint(context.Background(), domain.SessionRecord{}); err != nil || fp != "" {
		t.Fatalf("no workspace = %q, %v, want empty (never stuck)", fp, err)
	}
	failing := GitProgress{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not a repo") }}
	if _, err := failing.Fingerprint(context.Background(), rec); err == nil {
		t.Fatal("a git failure must surface so the loop treats progress as unknown")
	}
}

func TestUnreadableProgressNeverCountsAsStuck(t *testing.T) {
	e := newEnv(t)
	e.svc.progress = nil
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	for i := 0; i < 6; i++ {
		e.turnEnds(orch.ID)
	}
	if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
		t.Fatalf("state = %s: with no way to read progress the loop must not call the run stuck", got)
	}
}

func TestCleanAutopilotTextDropsControlCharactersAndCuts(t *testing.T) {
	got := domain.CleanAutopilotText("  hi\x1b[31m there\r\nnext\tline\x00  ", 100)
	if strings.ContainsAny(got, "\x1b\x00\r") || !strings.Contains(got, "hi") || !strings.Contains(got, "next\tline") {
		t.Fatalf("clean = %q", got)
	}
	if long := domain.CleanAutopilotText(strings.Repeat("é", 50), 10); len([]rune(long)) != 10 || !strings.HasSuffix(long, "…") {
		t.Fatalf("cut = %q", long)
	}
}

// ---- suppressing the notification for approvals Autopilot answers ----

func TestAnswersApprovalsForCoversChatSessionsOfARunningRunOnly(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	if e.svc.AnswersApprovalsFor(e.ctx, orch.ID) {
		t.Fatal("with no run nothing is answered, so the user must be notified")
	}
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeChat)
	termWorker := e.session(domain.KindWorker, e.now.Add(time.Minute), domain.SessionModeTUI)
	oldWorker := e.session(domain.KindWorker, e.base.Add(-time.Hour), domain.SessionModeChat)

	for name, c := range map[string]struct {
		id   domain.SessionID
		want bool
	}{
		"the run's own Chat session":                        {orch.ID, true},
		"a Chat worker started after the run":               {worker.ID, true},
		"a terminal worker (its prompt cannot be answered)": {termWorker.ID, false},
		"a worker from before the run":                      {oldWorker.ID, false},
	} {
		if got := e.svc.AnswersApprovalsFor(e.ctx, c.id); got != c.want {
			t.Errorf("%s: AnswersApprovalsFor = %v, want %v", name, got, c.want)
		}
	}
	if _, err := e.svc.Stop(e.ctx, orch.ID); err != nil {
		t.Fatal(err)
	}
	if e.svc.AnswersApprovalsFor(e.ctx, orch.ID) || e.svc.AnswersApprovalsFor(e.ctx, worker.ID) {
		t.Fatal("a stopped run answers nothing")
	}
}

// Autopilot suppresses the ordinary "needs your input" notification for what it
// answers, so a prompt it cannot answer must not stall the run in silence: the
// run parks with its one notification.
func TestRunParksWhenASessionSitsOnAPromptAutopilotCannotAnswer(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	e.activity(orch.ID, domain.ActivityWaitingInput, e.now)

	e.now = e.now.Add(needsInputGrace - time.Second)
	e.svc.maintain(e.ctx)
	if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
		t.Fatalf("state = %s: the policy has had no time to answer yet", got)
	}
	e.now = e.now.Add(2 * time.Second)
	e.svc.maintain(e.ctx)
	run := e.run(orch.ID)
	if run.State != domain.AutopilotParked || run.EndReason != domain.AutopilotEndNeedsInput {
		t.Fatalf("run = %s/%s, want parked/needs_input", run.State, run.EndReason)
	}
	if e.notes.count() != 1 || !strings.Contains(e.notes.intents[0].Detail, "waiting on a prompt it cannot answer") {
		t.Fatalf("notifications = %+v, want the one that tells the user", e.notes.intents)
	}
	e.now = e.now.Add(time.Minute)
	e.svc.maintain(e.ctx)
	if e.notes.count() != 1 {
		t.Fatalf("a parked run notified again: %d", e.notes.count())
	}
}

func TestRunParksWhenAWorkerItStartedSitsOnAPrompt(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	worker := e.session(domain.KindWorker, e.now.Add(time.Second), domain.SessionModeTUI)
	e.activity(worker.ID, domain.ActivityBlocked, e.now)
	e.now = e.now.Add(needsInputGrace + time.Minute)
	e.svc.maintain(e.ctx)
	if run := e.run(orch.ID); run.State != domain.AutopilotParked || run.EndReason != domain.AutopilotEndNeedsInput {
		t.Fatalf("run = %s/%s, want parked/needs_input", run.State, run.EndReason)
	}
}
