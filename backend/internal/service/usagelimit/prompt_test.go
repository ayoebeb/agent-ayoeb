package usagelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
)

// Prompt storage of the fake store, keyed like the database.
type promptState struct {
	mu      sync.Mutex
	prompts map[string]*domain.UsageLimitPrompt
}

var promptsByStore sync.Map // *fakeStore -> *promptState

func (f *fakeStore) promptState() *promptState {
	v, _ := promptsByStore.LoadOrStore(f, &promptState{prompts: map[string]*domain.UsageLimitPrompt{}})
	return v.(*promptState)
}

func (f *fakeStore) InsertUsageLimitPrompt(_ context.Context, p domain.UsageLimitPrompt) (bool, error) {
	ps := f.promptState()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, existing := range ps.prompts {
		if existing.WindowKey == p.WindowKey {
			return false, nil
		}
	}
	p.State = domain.UsageLimitPromptOpen
	ps.prompts[p.ID] = &p
	return true, nil
}

func (f *fakeStore) ListOpenUsageLimitPrompts(context.Context) ([]domain.UsageLimitPrompt, error) {
	ps := f.promptState()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []domain.UsageLimitPrompt
	for _, p := range ps.prompts {
		if p.State == domain.UsageLimitPromptOpen {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *fakeStore) GetUsageLimitPrompt(_ context.Context, id string) (domain.UsageLimitPrompt, bool, error) {
	ps := f.promptState()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.prompts[id]; ok {
		return *p, true, nil
	}
	return domain.UsageLimitPrompt{}, false, nil
}

func (f *fakeStore) DecideUsageLimitPrompt(_ context.Context, id, state, target string, _ time.Time) (bool, error) {
	ps := f.promptState()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.prompts[id]
	if !ok || p.State != domain.UsageLimitPromptOpen {
		return false, nil
	}
	p.State, p.TargetAccountID = state, target
	return true, nil
}

func (f *fakeStore) ReopenUsageLimitPrompt(_ context.Context, id string) error {
	ps := f.promptState()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p, ok := ps.prompts[id]; ok && p.State == domain.UsageLimitPromptSwitched {
		p.State, p.TargetAccountID = domain.UsageLimitPromptOpen, ""
	}
	return nil
}

func (f *fakeStore) ListUsageLimitPromptStops(_ context.Context, windowKey string) ([]domain.UsageLimitStop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.UsageLimitStop
	for _, stop := range f.stops {
		if stop.WindowKey != windowKey || stop.ResumeState == domain.UsageLimitStopResumed {
			continue
		}
		if stop.ResumeState == domain.UsageLimitStopSkipped &&
			(stop.SkipReason == domain.UsageLimitSkipStopped || stop.SkipReason == domain.UsageLimitSkipSuperseded || stop.SkipReason == domain.UsageLimitOffExpired) {
			continue
		}
		out = append(out, *stop)
	}
	return out, nil
}

type fakeAccountLister struct {
	views []agentaccounts.AccountView
}

func (l *fakeAccountLister) List(context.Context, bool) ([]agentaccounts.AccountView, error) {
	return l.views, nil
}

func claudeView(id, email string, signedIn bool, fiveHour, weekly float64) agentaccounts.AccountView {
	reset := failedAt.Add(4 * time.Hour)
	return agentaccounts.AccountView{
		Account:  domain.AgentAccount{ID: id, Provider: domain.AgentAccountProviderClaude, Email: email},
		SignedIn: signedIn, LoggedIn: signedIn,
		Limits: &domain.AgentAccountLimits{Windows: []domain.AgentAccountLimitWindow{
			{ID: agentaccounts.FiveHourWindowID, UsedPercent: fiveHour, ResetsAt: &reset},
			{ID: "seven_day", UsedPercent: weekly},
		}},
	}
}

// fakeMover records moves; with finish set it behaves like the daemon, where the
// move's completion continues the interrupted turn.
type fakeMover struct {
	mu     sync.Mutex
	moved  []domain.SessionID
	errs   map[domain.SessionID]error
	finish func(domain.SessionID, string)
}

func (m *fakeMover) MoveToAccount(_ context.Context, id domain.SessionID, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.errs[id]; err != nil {
		return err
	}
	m.moved = append(m.moved, id)
	if m.finish != nil {
		m.finish(id, accountID)
	}
	return nil
}

// promptHarness has an orchestrator and two workers stopped on acct-1's limit,
// and three other accounts: a light one, a heavy one and a signed-out one.
func promptHarness(t *testing.T) (*harness, *fakeAccountLister, *fakeMover) {
	t.Helper()
	h := newHarness(t)
	h.store.addStop("orch", domain.KindOrchestrator, limitMessage)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.store.addStop("w2", domain.KindWorker, limitMessage)
	lister := &fakeAccountLister{views: []agentaccounts.AccountView{
		claudeView("acct-1", "limited@example.com", true, 100, 40),
		claudeView("acct-heavy", "heavy@example.com", true, 80, 70),
		claudeView("acct-light", "light@example.com", true, 10, 20),
		claudeView("acct-out", "out@example.com", false, 0, 0),
		{Account: domain.AgentAccount{ID: "codex-1", Provider: domain.AgentAccountProviderCodex}, SignedIn: true, LoggedIn: true},
	}}
	mover := &fakeMover{errs: map[domain.SessionID]error{}}
	h.svc.lister, h.svc.mover = lister, mover
	mover.finish = func(id domain.SessionID, accountID string) {
		if _, err := h.svc.ResumeAfterAccountMove(context.Background(), id, accountID); err != nil {
			t.Errorf("resume %s: %v", id, err)
		}
	}
	h.sweep()
	return h, lister, mover
}

// One limit event is one prompt, however many sessions stopped: it names the
// limited account and its reset, lists every stopped session (the orchestrator
// first) and offers the other signed-in accounts of the same provider.
func TestOneLimitEventIsOnePromptWithAllSessionsAndTheOtherAccounts(t *testing.T) {
	h, _, _ := promptHarness(t)

	prompts, err := h.svc.ListPrompts(context.Background())
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompts = %+v, err = %v, want exactly one", prompts, err)
	}
	p := prompts[0]
	if p.AccountID != "acct-1" || p.AccountEmail != "limited@example.com" || p.ResetsAt == nil || !p.ResetsAt.Equal(resetAt) {
		t.Fatalf("prompt = %+v, want the limited account and its reset", p)
	}
	if len(p.Sessions) != 3 || p.Sessions[0].ID != "orch" || p.Sessions[0].Kind != domain.KindOrchestrator {
		t.Fatalf("sessions = %+v, want three with the orchestrator first", p.Sessions)
	}
	var ids []string
	for _, o := range p.Options {
		ids = append(ids, o.AccountID)
	}
	if len(ids) != 2 || ids[0] != "acct-light" || ids[1] != "acct-heavy" {
		t.Fatalf("options = %v, want the signed-in Claude accounts but the limited one, least 5h usage first", ids)
	}
	if o := p.Options[0]; o.FiveHourUsedPercent == nil || *o.FiveHourUsedPercent != 10 || o.WeeklyUsedPercent == nil || *o.WeeklyUsedPercent != 20 {
		t.Fatalf("option usage = %+v, want 5h 10%% and weekly 20%%", o)
	}
	// Sweeping again neither duplicates the prompt nor the notification.
	h.sweep()
	if again, _ := h.svc.ListPrompts(context.Background()); len(again) != 1 {
		t.Fatalf("prompts after another sweep = %d, want 1", len(again))
	}
	intent := h.notifier.stored["usage_limit:"+domain.UsageLimitWindowKey("acct-1", &resetAt, failedAt)]
	if !intent.UsageLimitSwitchPrompt {
		t.Fatalf("notification = %+v, want it to point at the switch prompt", intent)
	}
}

func TestNoPromptWithoutAnotherUsableAccount(t *testing.T) {
	h, lister, _ := promptHarness(t)
	lister.views = []agentaccounts.AccountView{
		claudeView("acct-1", "limited@example.com", true, 100, 40),
		claudeView("acct-out", "out@example.com", false, 0, 0),
	}
	if prompts, err := h.svc.ListPrompts(context.Background()); err != nil || len(prompts) != 0 {
		t.Fatalf("prompts = %+v, err = %v, want none", prompts, err)
	}
}

// Switch moves the orchestrator and both workers to the chosen account, and each
// interrupted turn continues on it, without waiting for the reset.
func TestSwitchMovesEveryStoppedSessionAndContinuesEachTurn(t *testing.T) {
	h, _, mover := promptHarness(t)
	prompts, _ := h.svc.ListPrompts(context.Background())

	result, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light")
	if err != nil || len(result.Moved) != 3 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, err = %v, want three moved", result, err)
	}
	if len(mover.moved) != 3 {
		t.Fatalf("moved = %v", mover.moved)
	}
	if got := len(h.sender.sent); got != 3 {
		t.Fatalf("continue messages = %d, want one per session", got)
	}
	for _, m := range h.sender.sent {
		if m.key == "" || m.message == "" {
			t.Fatalf("message = %+v", m)
		}
	}
	if left, _ := h.svc.ListPrompts(context.Background()); len(left) != 0 {
		t.Fatalf("prompts after the switch = %+v, want none", left)
	}
	// The question is answered once.
	if _, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light"); !errors.Is(err, ErrPromptAnswered) {
		t.Fatalf("second switch err = %v, want ErrPromptAnswered", err)
	}
	if err := h.svc.Wait(context.Background(), prompts[0].ID); !errors.Is(err, ErrPromptAnswered) {
		t.Fatalf("wait after switch err = %v, want ErrPromptAnswered", err)
	}
	// The reset passing later sends nothing more.
	h.now = resetAt.Add(10 * time.Minute)
	h.sweep()
	if got := len(h.sender.sent); got != 3 {
		t.Fatalf("messages after the reset = %d, want still 3", got)
	}
}

func TestSwitchRefusesAnAccountThatCannotTakeTheSessions(t *testing.T) {
	h, lister, mover := promptHarness(t)
	prompts, _ := h.svc.ListPrompts(context.Background())
	lister.views = append(lister.views, claudeView("acct-full", "full@example.com", true, 100, 100))

	for _, target := range []string{"acct-out", "acct-1", "codex-1", "acct-full", "nope"} {
		if _, err := h.svc.Switch(context.Background(), prompts[0].ID, target); !errors.Is(err, ErrPromptTargetInvalid) {
			t.Fatalf("switch to %s: err = %v, want ErrPromptTargetInvalid", target, err)
		}
	}
	if _, err := h.svc.Switch(context.Background(), "unknown", "acct-light"); !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("unknown prompt err = %v", err)
	}
	if len(mover.moved) != 0 {
		t.Fatalf("a refused switch moved %v", mover.moved)
	}
	// Refusals do not use the question up.
	if left, _ := h.svc.ListPrompts(context.Background()); len(left) != 1 {
		t.Fatalf("prompts = %d, want the question still open", len(left))
	}
}

// A session already on the account (moved by hand) is continued directly; one
// that fails to move is reported while the rest still move.
func TestSwitchHandlesAlreadyMovedAndFailedSessions(t *testing.T) {
	h, _, mover := promptHarness(t)
	prompts, _ := h.svc.ListPrompts(context.Background())
	mover.errs["orch"] = ports.ErrAgentAccountUnchanged
	mover.errs["w2"] = errors.New("workspace gone")

	result, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 2 || len(result.Failed) != 1 || result.Failed[0].SessionID != "w2" {
		t.Fatalf("result = %+v, want orch and w1 moved, w2 failed", result)
	}
	continued := map[domain.SessionID]bool{}
	for _, m := range h.sender.sent {
		continued[m.id] = true
	}
	if !continued["orch"] || !continued["w1"] || continued["w2"] {
		t.Fatalf("continued = %v, want orch and w1 only", continued)
	}
}

// When nothing could be moved the question stays open for another try.
func TestSwitchThatMovesNothingKeepsThePromptOpen(t *testing.T) {
	h, _, mover := promptHarness(t)
	prompts, _ := h.svc.ListPrompts(context.Background())
	for _, id := range []domain.SessionID{"orch", "w1", "w2"} {
		mover.errs[id] = errors.New("boom")
	}
	if _, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light"); !errors.Is(err, ErrPromptSwitchFailed) {
		t.Fatalf("err = %v, want ErrPromptSwitchFailed", err)
	}
	if left, _ := h.svc.ListPrompts(context.Background()); len(left) != 1 {
		t.Fatalf("prompts = %d, want the question reopened", len(left))
	}
}

// Wait closes the question and moves nothing; the sessions continue after the
// reset like before.
func TestWaitClosesThePromptAndLeavesTheSessionsToTheReset(t *testing.T) {
	h, _, mover := promptHarness(t)
	prompts, _ := h.svc.ListPrompts(context.Background())

	if err := h.svc.Wait(context.Background(), prompts[0].ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := h.svc.ListPrompts(context.Background()); len(left) != 0 {
		t.Fatalf("prompts after wait = %+v, want none", left)
	}
	if len(mover.moved) != 0 || len(h.sender.sent) != 0 {
		t.Fatalf("wait moved %v and sent %v, want nothing", mover.moved, h.sender.sent)
	}
	if err := h.svc.Wait(context.Background(), prompts[0].ID); !errors.Is(err, ErrPromptAnswered) {
		t.Fatalf("second wait err = %v, want ErrPromptAnswered", err)
	}
	if _, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light"); !errors.Is(err, ErrPromptAnswered) {
		t.Fatalf("switch after wait err = %v, want ErrPromptAnswered", err)
	}
	// After the reset, the normal automatic resume still continues them.
	h.now = resetAt.Add(time.Minute)
	for i := 0; i < 3; i++ {
		h.sweep()
		h.now = h.now.Add(30 * time.Second)
	}
	if len(h.sender.sent) != 3 {
		t.Fatalf("messages after the reset = %d, want all three continued", len(h.sender.sent))
	}
}
