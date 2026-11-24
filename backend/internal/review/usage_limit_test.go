package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func limitPausedStore(status domain.ReviewRunStatus) *fakeStore {
	return &fakeStore{
		reviews: map[domain.ReviewerHarness]domain.Review{
			domain.ReviewerClaudeCode: {
				ID: "rev-claude-code", SessionID: "mer-1", Harness: domain.ReviewerClaudeCode,
				InterfaceMode: domain.ReviewerInterfaceChat, ReviewerHandleID: "review-chat:rev-claude-code", ReviewerLaunchID: "launch-1",
			},
		},
		runs: []domain.ReviewRun{requestedRun("run-1", domain.ReviewerClaudeCode, status, domain.VerdictNone, 1)},
	}
}

// A reviewer that stopped on the usage limit stops reading as running: its
// pass is paused, `ao review ls` shows it as in flight, and a rerun is refused
// instead of piling a second pass onto the same stalled reviewer.
func TestPauseForUsageLimitMarksRunsPausedNotRunning(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunRunning)
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{alive: true})

	n, err := eng.PauseForUsageLimit(context.Background(), "mer-1", "rev-claude-code")
	if err != nil || n != 1 {
		t.Fatalf("PauseForUsageLimit = %d, %v; want 1 run paused", n, err)
	}
	if got := store.runs[0].Status; got != domain.ReviewRunPaused {
		t.Fatalf("run = %q, want paused", got)
	}
	ls, err := eng.List(context.Background(), "mer-1")
	if err != nil || ls.Reviews[0].Status != ReviewStateRunning || ls.Reviews[0].LatestRun.Status != domain.ReviewRunPaused {
		t.Fatalf("ls = %+v err=%v, want the head in flight with a paused run", ls.Reviews, err)
	}
	if _, err := eng.TriggerWithOptions(context.Background(), "mer-1", TriggerOptions{Source: domain.ReviewTriggerAgent, Rerun: true}); !errors.Is(err, ErrReviewAlreadyRunning) {
		t.Fatalf("rerun while paused: err = %v, want ErrReviewAlreadyRunning", err)
	}
}

// After the reset the watcher resumes the reviewer: it is asked to review the
// paused commit again and the pass runs again.
func TestResumeAfterUsageLimitNotifiesReviewerAndRunsAgain(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	launcher := &fakeLauncher{alive: true}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if err := eng.ResumeAfterUsageLimit(context.Background(), "mer-1", "rev-claude-code"); err != nil {
		t.Fatalf("ResumeAfterUsageLimit: %v", err)
	}
	if !launcher.notified || launcher.gotHandle != "review-chat:rev-claude-code" {
		t.Fatalf("launcher = %+v, want the live reviewer notified", launcher)
	}
	if len(launcher.gotSpec.ReviewQueue) != 1 || launcher.gotSpec.ReviewQueue[0].RunID != "run-1" {
		t.Fatalf("queue = %+v, want the paused run", launcher.gotSpec.ReviewQueue)
	}
	if got := store.runs[0].Status; got != domain.ReviewRunRunning {
		t.Fatalf("run = %q, want running again", got)
	}
}

// A paused review whose worker is gone cannot continue; it must not stay paused
// forever, so its pass is released as failed.
func TestResumeAfterUsageLimitReleasesRunsOfATerminatedWorker(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	worker := liveWorker()
	worker.IsTerminated = true
	eng := newEngineForTest(store, fakeSessions{rec: worker, ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{alive: true})

	if err := eng.ResumeAfterUsageLimit(context.Background(), "mer-1", "rev-claude-code"); !errors.Is(err, ErrReviewerGone) {
		t.Fatalf("err = %v, want ErrReviewerGone", err)
	}
	if got := store.runs[0].Status; got != domain.ReviewRunFailed {
		t.Fatalf("run = %q, want failed", got)
	}
}

// A failed notify leaves the pass paused so the watcher can retry.
func TestResumeAfterUsageLimitKeepsPauseWhenReviewerCannotBeNotified(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	launcher := &fakeLauncher{alive: true, notifyErr: errors.New("chat busy")}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)

	if err := eng.ResumeAfterUsageLimit(context.Background(), "mer-1", "rev-claude-code"); err == nil {
		t.Fatal("want the notify error")
	}
	if got := store.runs[0].Status; got != domain.ReviewRunPaused {
		t.Fatalf("run = %q, want it still paused", got)
	}
}

func TestReleaseUsageLimitPauseFreesTheSlot(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{alive: true, handle: "claude-pane"})

	if err := eng.ReleaseUsageLimitPause(context.Background(), "mer-1", "rev-claude-code", "superseded"); err != nil {
		t.Fatalf("ReleaseUsageLimitPause: %v", err)
	}
	if got := store.runs[0].Status; got != domain.ReviewRunFailed {
		t.Fatalf("run = %q, want failed", got)
	}
	if _, err := eng.TriggerWithOptions(context.Background(), "mer-1", TriggerOptions{Source: domain.ReviewTriggerAgent, Rerun: true}); err != nil {
		t.Fatalf("rerun after release: %v", err)
	}
}

// `ao review cancel` also drops a paused pass.
func TestCancelCancelsAPausedRun(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{alive: true})

	res, err := eng.Cancel(context.Background(), "mer-1")
	if err != nil || len(res.CancelledRuns) != 1 {
		t.Fatalf("Cancel = %+v, %v; want the paused run cancelled", res, err)
	}
	if got := store.runs[0].Status; got != domain.ReviewRunCancelled {
		t.Fatalf("run = %q, want cancelled", got)
	}
}

// Switch: the reviewer restarts under the target account's environment and
// continues there.
func TestMoveReviewerToAccountRestartsUnderThatAccount(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	launcher := &fakeLauncher{handle: "review-chat:rev-claude-code", interfaceMode: domain.ReviewerInterfaceChat}
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, launcher)
	eng.accountEnv = func(_ context.Context, worker domain.SessionID, accountID string) (map[string]string, error) {
		if worker != "mer-1" || accountID != "acct-2" {
			t.Fatalf("accountEnv(%q, %q)", worker, accountID)
		}
		return map[string]string{"CLAUDE_CONFIG_DIR": "/accounts/acct-2"}, nil
	}

	if err := eng.MoveReviewerToAccount(context.Background(), "mer-1", "rev-claude-code", "acct-2"); err != nil {
		t.Fatalf("MoveReviewerToAccount: %v", err)
	}
	if !launcher.restored || launcher.specs[0].AccountEnv["CLAUDE_CONFIG_DIR"] != "/accounts/acct-2" {
		t.Fatalf("restore spec = %+v, want the target account's environment", launcher.specs)
	}
	if !launcher.notified || store.runs[0].Status != domain.ReviewRunRunning {
		t.Fatalf("notified=%v run=%q, want the reviewer continued", launcher.notified, store.runs[0].Status)
	}
}

func TestMoveReviewerToAccountWithoutResolverIsInvalid(t *testing.T) {
	store := limitPausedStore(domain.ReviewRunPaused)
	eng := newEngineForTest(store, fakeSessions{rec: liveWorker(), ok: true}, prAt("sha1"), fakeProjects{}, &fakeLauncher{})
	if err := eng.MoveReviewerToAccount(context.Background(), "mer-1", "rev-claude-code", "acct-2"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}
