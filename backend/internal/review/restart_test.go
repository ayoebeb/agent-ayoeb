package review

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// probeLauncher is a fakeLauncher whose reviewer can be gone, busy or printing.
type probeLauncher struct {
	fakeLauncher
	gone   bool
	busy   bool
	screen string
}

func (l *probeLauncher) Alive(ctx context.Context, handleID, launchID string) (bool, error) {
	if l.gone {
		return false, nil
	}
	return l.fakeLauncher.Alive(ctx, handleID, launchID)
}

func (l *probeLauncher) Busy(string) bool { return l.busy }

func (l *probeLauncher) Output(context.Context, string) (string, error) { return l.screen, nil }

type restartFixture struct {
	engine   *Engine
	store    *fakeStore
	launcher *probeLauncher
	now      time.Time
}

// newRestartFixture is a worker with one running pass recorded before the
// current daemon started.
func newRestartFixture(mode domain.ReviewerInterfaceMode, restarting bool) *restartFixture {
	f := &restartFixture{now: time.Unix(100_000, 0).UTC()}
	handle := "review-mer-1"
	if mode == domain.ReviewerInterfaceChat {
		handle = reviewerChatHandlePrefix + "rev-1"
	}
	review := domain.Review{
		ID: "rev-1", SessionID: "mer-1", ProjectID: "mer", Harness: domain.ReviewerClaudeCode,
		ReviewerHandleID: handle, AgentSessionID: "native-1", InterfaceMode: mode,
		ReviewerActivityState: domain.ActivityActive, UpdatedAt: f.now.Add(-time.Hour),
	}
	if mode == domain.ReviewerInterfaceChat {
		review.ProviderConversationID = "conversation-1"
	}
	f.store = &fakeStore{
		reviews: map[domain.ReviewerHarness]domain.Review{review.Harness: review},
		runs: []domain.ReviewRun{{
			ID: "run-1", ReviewID: "rev-1", SessionID: "mer-1", BatchID: "batch-1", Harness: review.Harness,
			TriggerSource: domain.ReviewTriggerManual, PRURL: "https://github.com/o/r/pull/1", TargetSHA: "sha-1",
			Status: domain.ReviewRunRunning, CreatedAt: f.now.Add(-time.Hour),
		}},
	}
	f.launcher = &probeLauncher{fakeLauncher: fakeLauncher{handle: handle, interfaceMode: mode}}
	ids := 0
	f.engine = New(Deps{
		Store: f.store, Sessions: fakeSessions{rec: liveWorker(), ok: true}, PRs: prAt("sha-1"),
		Projects: fakeProjects{}, Launcher: f.launcher, RecoversRunningRuns: restarting,
		Clock: func() time.Time { return f.now },
		NewID: func() string { ids++; return "new-" + string(rune('0'+ids)) },
	})
	return f
}

func (f *restartFixture) run(t *testing.T, id string) domain.ReviewRun {
	t.Helper()
	for _, run := range f.store.runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("no run %q in %+v", id, f.store.runs)
	return domain.ReviewRun{}
}

func (f *restartFixture) running() []domain.ReviewRun {
	var out []domain.ReviewRun
	for _, run := range f.store.runs {
		if run.Status == domain.ReviewRunRunning {
			out = append(out, run)
		}
	}
	return out
}

func (f *restartFixture) recover(t *testing.T) error {
	t.Helper()
	return f.engine.RecoverRunningReviews(context.Background(), 0)
}

func (f *restartFixture) sweepAfter(t *testing.T, d time.Duration) []StalledReview {
	t.Helper()
	f.now = f.now.Add(d)
	stalled, err := f.engine.SweepStalledReviews(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return stalled
}

func TestRecoverRunningReviewsRelaunchesGoneReviewer(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, true)
	f.launcher.gone = true

	if err := f.recover(t); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if f.launcher.spawnCount != 1 {
		t.Fatalf("spawns = %d, want 1", f.launcher.spawnCount)
	}
	spec := f.launcher.gotSpec
	if spec.RunID != "run-1" || spec.TargetSHA != "sha-1" || spec.AgentSessionID != "native-1" {
		t.Fatalf("spec = %+v, want the same run, head and conversation", spec)
	}
	run := f.run(t, "run-1")
	if run.Status != domain.ReviewRunRunning || run.Body != resumedAfterRestartNote {
		t.Fatalf("run = %+v, want running and marked resumed", run)
	}
	if run.BatchID == "batch-1" || run.BatchID != spec.BatchID {
		t.Fatalf("batch = %q (spec %q), want a new dispatch batch", run.BatchID, spec.BatchID)
	}
}

func TestRecoverRunningReviewsLeavesWorkingReviewerAlone(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, true)
	f.launcher.alive = true

	if err := f.recover(t); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if f.launcher.spawnCount != 0 || f.run(t, "run-1").Body != "" {
		t.Fatalf("spawns = %d, run = %+v, want untouched", f.launcher.spawnCount, f.run(t, "run-1"))
	}
}

func TestRecoverRunningReviewsContinuesRestoredIdleChat(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceChat, true)
	f.launcher.alive = true

	if err := f.recover(t); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if f.launcher.spawnCount != 1 || f.launcher.gotSpec.ProviderConversationID != "conversation-1" {
		t.Fatalf("spawns = %d, spec = %+v, want the conversation resumed", f.launcher.spawnCount, f.launcher.gotSpec)
	}

	busy := newRestartFixture(domain.ReviewerInterfaceChat, true)
	busy.launcher.alive, busy.launcher.busy = true, true
	if err := busy.recover(t); err != nil {
		t.Fatalf("recover busy: %v", err)
	}
	if busy.launcher.spawnCount != 0 {
		t.Fatal("a Chat reviewer with a turn in progress must not be relaunched")
	}
}

func TestRecoverRunningReviewsFailedRelaunchFreesTheHead(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, true)
	f.launcher.gone = true
	f.launcher.spawnErr = errors.New("no such binary")

	if err := f.recover(t); err == nil {
		t.Fatal("recover must report the failed relaunch")
	}

	// Resume, then a fresh reviewer.
	if f.launcher.spawnCount != 2 || f.launcher.gotSpec.AgentSessionID != "" {
		t.Fatalf("spawns = %d, last spec = %+v, want a fresh retry", f.launcher.spawnCount, f.launcher.gotSpec)
	}
	run := f.run(t, "run-1")
	if run.Status != domain.ReviewRunFailed || !strings.HasPrefix(run.Body, restartFailedReason) || !strings.Contains(run.Body, "no such binary") {
		t.Fatalf("run = %+v, want failed with the reason", run)
	}
	state := Plan(prAt("sha-1").prs, f.store.runs)
	if state[0].Status != ReviewStateNeedsReview || headHasReview(f.store.runs, state[0].PRURL, "sha-1") {
		t.Fatalf("state = %+v, want the head free for a new trigger", state[0])
	}
}

func TestDeadReviewerWaitsForRestartRecovery(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, true)
	f.launcher.gone = true

	if _, err := f.engine.List(context.Background(), "mer-1"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := f.run(t, "run-1").Status; got != domain.ReviewRunRunning {
		t.Fatalf("status before recovery = %q, want running", got)
	}
	if _, err := f.engine.RestoreReviewer(context.Background(), "mer-1"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if f.launcher.restored {
		t.Fatal("an idle restore must leave the running pass to the recovery")
	}
}

func TestWatchdogRetriesOnceThenReports(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, false)
	f.launcher.alive = true
	f.launcher.screen = "Login expired"

	if got := f.sweepAfter(t, 0); len(got) != 0 {
		t.Fatalf("first sight reported %+v", got)
	}
	if got := f.sweepAfter(t, ReviewerStallAfter-time.Minute); len(got) != 0 || f.launcher.spawnCount != 0 {
		t.Fatalf("early sweep acted: %+v", got)
	}

	if got := f.sweepAfter(t, 2*time.Minute); len(got) != 0 {
		t.Fatalf("the first stall is retried, not reported: %+v", got)
	}
	if run := f.run(t, "run-1"); run.Status != domain.ReviewRunFailed || run.Body != reviewerStoppedReason {
		t.Fatalf("stalled run = %+v, want failed as reviewer stopped", run)
	}
	retries := f.running()
	if len(retries) != 1 || retries[0].TargetSHA != "sha-1" || retries[0].Body != retriedStalledNote || f.launcher.spawnCount != 1 {
		t.Fatalf("retries = %+v, spawns = %d, want one retry on the same head", retries, f.launcher.spawnCount)
	}

	f.sweepAfter(t, 0)
	got := f.sweepAfter(t, ReviewerStallAfter+time.Minute)
	if len(got) != 1 || got[0].WorkerID != "mer-1" || !strings.Contains(got[0].Note, "again after one automatic retry") {
		t.Fatalf("second stall = %+v, want one report", got)
	}
	if len(f.running()) != 0 || f.launcher.spawnCount != 1 {
		t.Fatalf("running = %+v, spawns = %d, want no second retry", f.running(), f.launcher.spawnCount)
	}
	if got := f.sweepAfter(t, time.Hour); len(got) != 0 {
		t.Fatalf("reported twice: %+v", got)
	}
}

func TestWatchdogReportsWhenTheRetryCannotStart(t *testing.T) {
	f := newRestartFixture(domain.ReviewerInterfaceTUI, false)
	f.launcher.alive = true
	f.launcher.spawnErr = errors.New("no such binary")

	f.sweepAfter(t, 0)
	f.now = f.now.Add(ReviewerStallAfter)
	got, err := f.engine.SweepStalledReviews(context.Background())

	if err == nil || len(got) != 1 || !strings.Contains(got[0].Note, "no such binary") {
		t.Fatalf("got %+v, err %v, want one report naming the failure", got, err)
	}
	if len(f.running()) != 0 {
		t.Fatalf("running = %+v, want the slot freed", f.running())
	}
}

func TestWatchdogCountsReviewerActivity(t *testing.T) {
	tui := newRestartFixture(domain.ReviewerInterfaceTUI, false)
	tui.launcher.alive = true
	for i := range 6 {
		tui.launcher.screen = strings.Repeat(".", i+1)
		tui.sweepAfter(t, ReviewerStallAfter/2)
	}
	if got := tui.run(t, "run-1").Status; got != domain.ReviewRunRunning {
		t.Fatalf("a reviewer whose screen changes was failed: %q", got)
	}

	chat := newRestartFixture(domain.ReviewerInterfaceChat, false)
	chat.launcher.alive = true
	for range 6 {
		chat.store.chatActivityAt = chat.now
		chat.sweepAfter(t, ReviewerStallAfter/2)
	}
	if got := chat.run(t, "run-1").Status; got != domain.ReviewRunRunning {
		t.Fatalf("a Chat reviewer with new activity was failed: %q", got)
	}
}
