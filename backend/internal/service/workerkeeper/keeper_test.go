package workerkeeper

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu    sync.Mutex
	recs  map[domain.SessionID]domain.SessionRecord
	marks map[domain.SessionID]time.Time
	stops []domain.UsageLimitStop
}

func (s *fakeStore) ListWatchedWorkerIDs(context.Context) ([]domain.SessionID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []domain.SessionID
	for id, r := range s.recs {
		if r.Activity.State == domain.ActivityExited || r.Activity.State == domain.ActivityIdle {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *fakeStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	return r, ok, nil
}

func (s *fakeStore) AgentStopMark(_ context.Context, id domain.SessionID) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.marks[id]
	return at, ok, nil
}

func (s *fakeStore) ListPendingUsageLimitStops(context.Context) ([]domain.UsageLimitStop, error) {
	return s.stops, nil
}

func (s *fakeStore) set(id domain.SessionID, state domain.ActivityState, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.recs[id]
	r.Activity = domain.Activity{State: state, LastActivityAt: at}
	s.recs[id] = r
}

type fakeReports struct {
	finished map[domain.SessionID]bool
	stopped  []string
	stalled  []string
}

func (r *fakeReports) Unfinished(_ context.Context, rec domain.SessionRecord) (bool, error) {
	return !r.finished[rec.ID], nil
}

func (r *fakeReports) FileStopped(_ context.Context, rec domain.SessionRecord, reason string) error {
	r.stopped = append(r.stopped, string(rec.ID)+": "+reason)
	return nil
}

func (r *fakeReports) FileStalled(_ context.Context, rec domain.SessionRecord, state string, _ time.Duration) error {
	r.stalled = append(r.stalled, string(rec.ID)+" "+state)
	return nil
}

type busy map[domain.SessionID]bool

func (b busy) BackgroundWorkRunning(id domain.SessionID) bool { return b[id] }

type harness struct {
	store   *fakeStore
	reports *fakeReports
	keeper  *Keeper
	now     time.Time
	resumed []domain.SessionID
	sent    []string
	// resumeErr is returned by every resume attempt while set.
	resumeErr error
	slept     int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store:   &fakeStore{recs: map[domain.SessionID]domain.SessionRecord{}, marks: map[domain.SessionID]time.Time{}},
		reports: &fakeReports{finished: map[domain.SessionID]bool{}},
		now:     t0,
	}
	h.keeper = New(Deps{
		Store: h.store, Reports: h.reports, Stagger: time.Second,
		Now: func() time.Time { return h.now },
		Resume: func(_ context.Context, id domain.SessionID) error {
			if h.resumeErr != nil {
				return h.resumeErr
			}
			h.resumed = append(h.resumed, id)
			h.store.set(id, domain.ActivityActive, h.now)
			return nil
		},
		Send: func(_ context.Context, _ domain.SessionID, m string) error {
			h.sent = append(h.sent, m)
			return nil
		},
		Sleep: func(context.Context, time.Duration) error { h.slept++; return nil },
	})
	return h
}

func (h *harness) add(id domain.SessionID, state domain.ActivityState, at time.Time) {
	h.store.recs[id] = domain.SessionRecord{ID: id, Kind: domain.KindWorker, CreatedAt: t0.Add(-time.Hour),
		Activity: domain.Activity{State: state, LastActivityAt: at}}
}

func (h *harness) sweep(t *testing.T, boot bool, advance time.Duration) {
	t.Helper()
	h.now = h.now.Add(advance)
	if err := h.keeper.Sweep(context.Background(), boot); err != nil {
		t.Fatal(err)
	}
}

func TestRestartResumesAMidTurnWorkerOnce(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityExited, t0)

	h.sweep(t, true, 0)
	h.sweep(t, false, time.Minute)
	h.sweep(t, false, time.Minute)

	if len(h.resumed) != 1 || h.resumed[0] != "w-1" {
		t.Fatalf("resumed = %v, want w-1 once", h.resumed)
	}
	if len(h.sent) != 1 || h.sent[0] != RestartMessage {
		t.Fatalf("sent = %q, want the restart message once", h.sent)
	}
	if len(h.reports.stopped) != 0 {
		t.Errorf("a clean resume must not report: %v", h.reports.stopped)
	}
}

func TestRestartStaggersResumes(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityExited, t0)
	h.add("w-2", domain.ActivityExited, t0)
	h.add("w-3", domain.ActivityExited, t0)

	h.sweep(t, true, 0)

	if len(h.resumed) != 3 || h.slept != 2 {
		t.Fatalf("resumed %d, slept %d; want 3 resumes spaced by 2 waits", len(h.resumed), h.slept)
	}
}

func TestRestartLeavesStoppedAndFinishedWorkersAlone(t *testing.T) {
	h := newHarness(t)
	h.add("stopped", domain.ActivityExited, t0)
	h.store.marks["stopped"] = t0 // exit-agent recorded this exit
	h.add("finished", domain.ActivityExited, t0)
	h.reports.finished["finished"] = true
	h.add("limited", domain.ActivityExited, t0)
	h.store.stops = []domain.UsageLimitStop{{SessionID: "limited"}}
	h.add("old", domain.ActivityExited, t0.Add(-MaxAge-time.Hour))
	h.add("idle", domain.ActivityIdle, t0)

	h.sweep(t, true, 0)

	if len(h.resumed) != 0 || len(h.sent) != 0 {
		t.Fatalf("resumed %v and sent %q; want neither", h.resumed, h.sent)
	}
}

func TestExitAfterAStopMarkIsACrash(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityExited, t0.Add(time.Hour))
	h.store.marks["w-1"] = t0 // an older, deliberate stop; the owner resumed since

	h.sweep(t, false, 0)
	h.sweep(t, false, time.Minute)

	if len(h.resumed) != 1 {
		t.Fatalf("resumed = %v, want a crash resume", h.resumed)
	}
}

func TestCrashResumesOnceThenReportsWhenItExitsAgain(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityExited, t0)

	h.sweep(t, false, 0) // first sight: wait for the exit to settle
	if len(h.resumed) != 0 {
		t.Fatalf("resumed on first sight: %v", h.resumed)
	}
	h.sweep(t, false, 30*time.Second)
	if len(h.resumed) != 1 || len(h.sent) != 1 || h.sent[0] != CrashMessage {
		t.Fatalf("resumed %v, sent %q; want one crash resume", h.resumed, h.sent)
	}

	h.sweep(t, false, 30*time.Second) // running again
	h.store.set("w-1", domain.ActivityExited, h.now)
	h.sweep(t, false, 30*time.Second) // second exit seen
	h.sweep(t, false, 30*time.Second) // settled: reported, not resumed again
	h.sweep(t, false, 30*time.Second)
	h.sweep(t, false, 30*time.Second)

	if len(h.resumed) != 1 {
		t.Errorf("resumed %v, want it once", h.resumed)
	}
	if len(h.reports.stopped) != 1 {
		t.Fatalf("stop reports = %v, want exactly one", h.reports.stopped)
	}
}

func TestCrashAfterTheWindowIsResumedAgain(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityExited, t0)
	h.sweep(t, false, 0)
	h.sweep(t, false, 30*time.Second)
	h.sweep(t, false, CrashWindow) // it ran for longer than the window

	h.store.set("w-1", domain.ActivityExited, h.now)
	h.sweep(t, false, 30*time.Second)
	h.sweep(t, false, 30*time.Second)

	if len(h.resumed) != 2 || len(h.reports.stopped) != 0 {
		t.Fatalf("resumed %v, reports %v; want a second resume and no report", h.resumed, h.reports.stopped)
	}
}

func TestFailedResumeReportsOnce(t *testing.T) {
	h := newHarness(t)
	h.resumeErr = errors.New("harness binary not found")
	h.add("w-1", domain.ActivityExited, t0)

	for range 6 {
		h.sweep(t, true, time.Minute)
	}

	if len(h.reports.stopped) != 1 || h.reports.stopped[0] != "w-1: harness binary not found" {
		t.Fatalf("stop reports = %v, want one with the last error", h.reports.stopped)
	}
	if len(h.resumed) != 0 {
		t.Errorf("resumed = %v", h.resumed)
	}
}

func TestWatchdogReportsOnceForAnIdleWorker(t *testing.T) {
	h := newHarness(t)
	h.add("w-1", domain.ActivityIdle, t0)

	h.sweep(t, false, StallAfter-time.Minute)
	if len(h.reports.stalled) != 0 {
		t.Fatalf("reported before the limit: %v", h.reports.stalled)
	}
	for range 4 {
		h.sweep(t, false, 2*time.Minute)
	}

	if len(h.reports.stalled) != 1 || h.reports.stalled[0] != "w-1 idle" {
		t.Fatalf("watchdog reports = %v, want one", h.reports.stalled)
	}
}

func TestWatchdogSkipsFinishedBusyAndLimitedWorkers(t *testing.T) {
	h := newHarness(t)
	h.keeper.d.Busy = busy{"busy": true}
	h.add("finished", domain.ActivityIdle, t0)
	h.reports.finished["finished"] = true
	h.add("busy", domain.ActivityIdle, t0)
	h.add("limited", domain.ActivityIdle, t0)
	h.store.stops = []domain.UsageLimitStop{{SessionID: "limited"}}

	h.sweep(t, false, StallAfter+time.Minute)

	if len(h.reports.stalled) != 0 {
		t.Fatalf("watchdog reports = %v, want none", h.reports.stalled)
	}
}

func TestWatchdogReportsAnExitedWorkerTheKeeperCouldNotMove(t *testing.T) {
	h := newHarness(t)
	h.resumeErr = errors.New("boom")
	h.add("w-1", domain.ActivityExited, t0)
	h.sweep(t, true, 0) // resume fails: stop report, no resume
	h.sweep(t, false, StallAfter+time.Minute)

	if len(h.reports.stopped) != 1 || len(h.reports.stalled) != 1 || h.reports.stalled[0] != "w-1 exited" {
		t.Fatalf("stopped %v, stalled %v", h.reports.stopped, h.reports.stalled)
	}
}
