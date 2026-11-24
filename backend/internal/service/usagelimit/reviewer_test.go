package usagelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
)

func (a fakeAccounts) ReviewerAccountID(context.Context, domain.AgentHarness) (string, error) {
	return a.account, nil
}

// fakeReviewers records what the watcher does to reviewers.
type fakeReviewers struct {
	mu        sync.Mutex
	paused    []string
	resumed   []string
	released  []string
	moved     []string
	resumeErr error
	pauseErr  error
}

func (r *fakeReviewers) Pause(_ context.Context, worker domain.SessionID, reviewID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pauseErr != nil {
		return r.pauseErr
	}
	r.paused = append(r.paused, string(worker)+"/"+reviewID)
	return nil
}

func (r *fakeReviewers) Resume(_ context.Context, worker domain.SessionID, reviewID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resumeErr != nil {
		return r.resumeErr
	}
	r.resumed = append(r.resumed, string(worker)+"/"+reviewID)
	return nil
}

func (r *fakeReviewers) Release(_ context.Context, worker domain.SessionID, reviewID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, string(worker)+"/"+reviewID+":"+reason)
	return nil
}

func (r *fakeReviewers) Move(_ context.Context, worker domain.SessionID, reviewID, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.moved = append(r.moved, string(worker)+"/"+reviewID+"->"+accountID)
	return nil
}

// addReviewStop registers a worker whose reviewer's Chat turn failed on the limit.
func (f *fakeStore) addReviewStop(worker domain.SessionID, reviewID, message string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	turn := "turn-" + reviewID
	completed := failedAt
	f.turns = append(f.turns, domain.LimitStopTurn{
		SessionID: worker, TurnID: turn, ErrorMessage: message, CompletedAt: &completed, RequestedAt: failedAt.Add(-time.Hour),
		ReviewID: reviewID, ReviewHarness: domain.ReviewerClaudeCode,
	})
	if _, ok := f.sessions[worker]; !ok {
		f.sessions[worker] = domain.SessionRecord{
			ID: worker, ProjectID: "proj", Kind: domain.KindWorker, Harness: domain.HarnessCodex, DisplayName: string(worker),
			Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle},
		}
	}
	return turn
}

func reviewerHarness(t *testing.T) (*harness, *fakeReviewers) {
	t.Helper()
	h := newHarness(t)
	rv := &fakeReviewers{}
	h.svc.reviewers = rv
	return h, rv
}

// A reviewer that stopped on the limit is paused (not left running), the owner
// is told it is the reviewer, and after the reset AO continues the reviewer, not
// the worker.
func TestReviewerStopPausesTheReviewAndResumesItAfterTheReset(t *testing.T) {
	h, rv := reviewerHarness(t)
	h.store.addReviewStop("w1", "rev-1", limitMessage)

	h.sweep()
	h.sweep()
	if len(rv.paused) != 1 || rv.paused[0] != "w1/rev-1" {
		t.Fatalf("paused = %v, want the reviewer paused exactly once", rv.paused)
	}
	if got := h.store.stops["turn-rev-1"]; got == nil || got.ReviewID != "rev-1" || got.SessionID != "w1" || got.AccountKey != "acct-1" {
		t.Fatalf("stop = %+v, want it recorded for the reviewer under its worker and the default account", got)
	}
	if notes := h.reporter.notes["w1"]; len(notes) != 1 || !strings.Contains(notes[0], "AO reviewer of this worker") || !strings.Contains(notes[0], "paused") {
		t.Fatalf("notes = %v, want one that names the reviewer", notes)
	}
	for _, intent := range h.notifier.stored {
		if !strings.Contains(intent.SessionDisplayName, "reviewer") {
			t.Fatalf("notification = %+v, want it to name the reviewer", intent)
		}
	}
	if len(rv.resumed) != 0 {
		t.Fatalf("resumed before the reset: %v", rv.resumed)
	}

	h.now = resetAt.Add(time.Minute)
	h.sweep()
	if len(rv.resumed) != 1 || rv.resumed[0] != "w1/rev-1" {
		t.Fatalf("resumed = %v, want the reviewer continued after the reset", rv.resumed)
	}
	if got := h.sender.ids(); len(got) != 0 {
		t.Fatalf("the worker was sent a message: %v", got)
	}
	if h.store.stops["turn-rev-1"].ResumeState != domain.UsageLimitStopResumed {
		t.Fatalf("stop = %+v, want it settled as resumed", h.store.stops["turn-rev-1"])
	}
	h.sweep()
	if len(rv.resumed) != 1 {
		t.Fatalf("resumed twice: %v", rv.resumed)
	}
}

func TestReviewerStopIsIgnoredWithoutReviewers(t *testing.T) {
	h := newHarness(t)
	h.store.addReviewStop("w1", "rev-1", limitMessage)
	h.sweep()
	if len(h.store.stops) != 0 {
		t.Fatalf("stops = %+v, want none when reviewers are not wired", h.store.stops)
	}
}

// A pause that failed leaves no stop row, so the next sweep tries again.
func TestReviewerPauseFailureIsRetriedBySweep(t *testing.T) {
	h, rv := reviewerHarness(t)
	h.store.addReviewStop("w1", "rev-1", limitMessage)
	rv.pauseErr = errors.New("store busy")
	if err := h.svc.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(h.store.stops) != 0 {
		t.Fatalf("stops = %+v, want none after a failed pause", h.store.stops)
	}
	rv.pauseErr = nil
	h.sweep()
	if len(rv.paused) != 1 || len(h.store.stops) != 1 {
		t.Fatalf("paused=%v stops=%v, want the retry to pause and record", rv.paused, h.store.stops)
	}
}

// When the reviewer's turn is no longer the latest (someone sent it something
// new) the stop is closed and the paused passes are released, never stuck.
func TestReviewerStopThatWasSupersededReleasesThePausedRuns(t *testing.T) {
	h, rv := reviewerHarness(t)
	turn := h.store.addReviewStop("w1", "rev-1", limitMessage)
	h.sweep()
	h.store.dropTurn(turn)
	h.sweep()
	if len(rv.released) != 1 || !strings.Contains(rv.released[0], "w1/rev-1") {
		t.Fatalf("released = %v, want the reviewer's paused runs released", rv.released)
	}
}

// A reviewer that cannot continue (worker gone) closes its stop; other errors retry.
func TestReviewerResumeFailureRetriesAndGoneClosesTheStop(t *testing.T) {
	h, rv := reviewerHarness(t)
	h.store.addReviewStop("w1", "rev-1", limitMessage)
	h.sweep()
	h.now = resetAt.Add(time.Minute)

	rv.resumeErr = errors.New("chat starting")
	h.sweep()
	if st := h.store.stops["turn-rev-1"]; st.ResumeState != domain.UsageLimitStopPending || st.RetryAt == nil {
		t.Fatalf("stop = %+v, want it deferred for a retry", st)
	}
	rv.resumeErr = errors.Join(ErrSessionGone, errors.New("reviewer archived"))
	h.now = h.now.Add(2 * time.Minute)
	h.sweep()
	if st := h.store.stops["turn-rev-1"]; st.ResumeState != domain.UsageLimitStopSkipped || st.SkipReason != domain.UsageLimitSkipStopped {
		t.Fatalf("stop = %+v, want it closed as stopped", st)
	}
}

// The switch-or-wait prompt lists the reviewer next to the sessions, and Switch
// moves it to the chosen account through the reviewer path, not as a session.
func TestSwitchPromptIncludesReviewersAndMovesThem(t *testing.T) {
	h, rv := reviewerHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.store.addReviewStop("w1", "rev-1", limitMessage)
	lister := &fakeAccountLister{views: []agentaccounts.AccountView{
		claudeView("acct-1", "limited@example.com", true, 100, 40),
		claudeView("acct-light", "light@example.com", true, 10, 20),
	}}
	mover := &fakeMover{errs: map[domain.SessionID]error{}}
	h.svc.lister, h.svc.mover = lister, mover
	mover.finish = func(id domain.SessionID, accountID string) {
		if _, err := h.svc.ResumeAfterAccountMove(context.Background(), id, accountID); err != nil {
			t.Errorf("resume %s: %v", id, err)
		}
	}
	h.sweep()

	prompts, err := h.svc.ListPrompts(context.Background())
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompts = %+v, err = %v, want exactly one for the shared window", prompts, err)
	}
	var reviewerEntry *PromptSession
	for i, s := range prompts[0].Sessions {
		if s.ReviewID == "rev-1" {
			reviewerEntry = &prompts[0].Sessions[i]
		}
	}
	if len(prompts[0].Sessions) != 2 || reviewerEntry == nil || !strings.Contains(reviewerEntry.DisplayName, "reviewer") {
		t.Fatalf("sessions = %+v, want the worker and its reviewer", prompts[0].Sessions)
	}

	result, err := h.svc.Switch(context.Background(), prompts[0].ID, "acct-light")
	if err != nil || len(result.Moved) != 2 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, err = %v, want both moved", result, err)
	}
	if len(mover.moved) != 1 || mover.moved[0] != "w1" {
		t.Fatalf("session moves = %v, want only the worker moved as a session", mover.moved)
	}
	if len(rv.moved) != 1 || rv.moved[0] != "w1/rev-1->acct-light" {
		t.Fatalf("reviewer moves = %v, want the reviewer moved to the chosen account", rv.moved)
	}
	// The reset passing later must not continue the reviewer a second time.
	h.now = resetAt.Add(10 * time.Minute)
	h.sweep()
	if len(rv.resumed) != 0 {
		t.Fatalf("reviewer resumed after the switch: %v", rv.resumed)
	}
}
