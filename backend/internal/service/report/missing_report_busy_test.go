package report

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeBusy answers true for the first n checks, then false.
type fakeBusy struct {
	mu    sync.Mutex
	left  int
	calls int
}

func (b *fakeBusy) BackgroundWorkRunning(domain.SessionID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.left > 0 {
		b.left--
		return true
	}
	return false
}

type fakeSessions struct {
	mu  sync.Mutex
	rec domain.SessionRecord
}

func (s *fakeSessions) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec, true, nil
}

func (s *fakeSessions) set(state domain.ActivityState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec.Activity.State = state
}

func immediately(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	c <- time.Time{}
	return c
}

func waitForReports(t *testing.T, f *fakeReports, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for f.count() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Give a wrongly filed second report time to show up.
	time.Sleep(50 * time.Millisecond)
	if got := f.count(); got != want {
		t.Fatalf("reports = %d, want %d", got, want)
	}
}

func TestIdleWithBackgroundWorkRunningFilesNothingThenOneReportWhenItEnds(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	busy := &fakeBusy{left: 1000}
	fb := NewMissingReportFallback(MissingReportDeps{
		Store: f, Reports: f, Busy: busy, Synchronous: true,
	})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "Waiting for the Go test results to arrive")
	if f.count() != 0 {
		t.Fatalf("reports = %d while background work runs, want 0", f.count())
	}

	// The worker resumes, ends its turn again, and nothing runs any more.
	busy.mu.Lock()
	busy.left = 0
	busy.mu.Unlock()
	fb.SessionTurnIdle(idleWorker(), "stop", "tests passed")
	if f.count() != 1 {
		t.Fatalf("reports = %d after the background work ended, want 1", f.count())
	}
	fb.SessionTurnIdle(idleWorker(), "stop", "again")
	if f.count() != 1 {
		t.Fatalf("reports = %d, want still 1 for the same message", f.count())
	}
}

func TestIdleWaitsOutBackgroundWorkThenFilesExactlyOnce(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	busy := &fakeBusy{left: 3}
	sessions := &fakeSessions{rec: idleWorker()}
	fb := NewMissingReportFallback(MissingReportDeps{
		Store: f, Reports: f, Busy: busy, Sessions: sessions, Grace: time.Minute, After: immediately,
	})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "waiting")

	waitForReports(t, f, 1)
	busy.mu.Lock()
	calls := busy.calls
	busy.mu.Unlock()
	if calls != 4 {
		t.Errorf("background checks = %d, want 4 (3 busy, then clear)", calls)
	}
}

func TestIdleThenResumedWithinGraceFilesNothing(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	sessions := &fakeSessions{rec: idleWorker()}
	sessions.set(domain.ActivityActive) // the worker resumed on its own
	fb := NewMissingReportFallback(MissingReportDeps{
		Store: f, Reports: f, Sessions: sessions, Grace: time.Minute, After: immediately,
	})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "waiting for the build")

	waitForReports(t, f, 0)
}

func TestStillIdleAfterGraceFilesOneReport(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	sessions := &fakeSessions{rec: idleWorker()}
	fb := NewMissingReportFallback(MissingReportDeps{
		Store: f, Reports: f, Sessions: sessions, Grace: time.Minute, After: immediately,
	})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "done, forgot to report")

	waitForReports(t, f, 1)
}

func TestNewerIdleSupersedesAnOlderPendingCheck(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	release := make(chan time.Time)
	fb := NewMissingReportFallback(MissingReportDeps{
		Store: f, Reports: f, Grace: time.Minute,
		After: func(time.Duration) <-chan time.Time { return release },
	})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "first")
	fb.SessionTurnIdle(idleWorker(), "stop", "second")
	release <- time.Time{}
	release <- time.Time{}

	waitForReports(t, f, 1)
}
