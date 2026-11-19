package report

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeReports is both the fallback's store and its creator, so a report the
// fallback files is what the next idle reads back.
type fakeReports struct {
	mu      sync.Mutex
	reports []domain.ReportRecord
	prs     []domain.PullRequest
	now     time.Time
}

func (f *fakeReports) ListReportsBySession(_ context.Context, id domain.SessionID) ([]domain.ReportRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.ReportRecord
	for _, r := range f.reports {
		if r.SessionID == id {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeReports) ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error) {
	return f.prs, nil
}

func (f *fakeReports) Create(_ context.Context, in CreateInput) (domain.ReportRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(time.Second)
	rec := domain.ReportRecord{SessionID: in.SessionID, State: in.State, Note: in.Note, CreatedAt: f.now}
	f.reports = append(f.reports, rec)
	return rec, nil
}

func (f *fakeReports) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

type fakeTurns struct{ response string }

func (t fakeTurns) SessionHistory(context.Context, domain.SessionID, int, int) ([]domain.HandoffTurn, int, error) {
	return []domain.HandoffTurn{{AgentResponse: t.response}}, 1, nil
}

var t0 = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

func newFallback(f *fakeReports, turns MissingReportTurns) *MissingReportFallback {
	return NewMissingReportFallback(MissingReportDeps{Store: f, Reports: f, Turns: turns, Synchronous: true})
}

func idleWorker() domain.SessionRecord {
	return domain.SessionRecord{
		ID: "w-1", Kind: domain.KindWorker, CreatedAt: t0,
		Activity: domain.Activity{State: domain.ActivityIdle},
	}
}

func TestIdleAfterTaskWithoutReportFilesOneAutomaticReport(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour), prs: []domain.PullRequest{{URL: "https://github.com/o/r/pull/7", Draft: true}}}
	fb := newFallback(f, nil)
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	long := strings.Repeat("a", 900)
	fb.SessionTurnIdle(idleWorker(), "stop", long)

	if f.count() != 1 {
		t.Fatalf("reports = %d, want 1", f.count())
	}
	r := f.reports[0]
	if r.State != domain.ReportCheckpoint {
		t.Errorf("state = %q, want checkpoint", r.State)
	}
	if !strings.HasPrefix(r.Note, "[AUTOMATIC REPORT") {
		t.Errorf("note is not marked automatic: %q", r.Note)
	}
	if !strings.Contains(r.Note, "https://github.com/o/r/pull/7 (draft)") {
		t.Errorf("note lacks the tracked PR: %q", r.Note)
	}
	if strings.Contains(r.Note, strings.Repeat("a", 601)) || !strings.Contains(r.Note, strings.Repeat("a", 590)) {
		t.Errorf("last message was not cut to about 600 characters")
	}
	if err := domain.ValidateReportContent(r.State, r.Note, "", nil); err != nil {
		t.Errorf("note is not a valid report: %v", err)
	}
}

func TestIdleAfterWorkerReportedFilesNothing(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, nil)
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))
	f.reports = append(f.reports, domain.ReportRecord{SessionID: "w-1", State: domain.ReportDone, CreatedAt: t0.Add(2 * time.Minute)})

	fb.SessionTurnIdle(idleWorker(), "stop", "done")

	if f.count() != 1 {
		t.Fatalf("reports = %d, want only the worker's own", f.count())
	}
}

func TestRepeatedIdleWithoutNewMessageFilesNothing(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, nil)
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "stop", "first")
	fb.SessionTurnIdle(idleWorker(), "stop", "second")
	if f.count() != 1 {
		t.Fatalf("reports after two idles = %d, want 1", f.count())
	}

	// A new message re-arms it, once.
	fb.SessionMessageReceived("w-1", f.now.Add(time.Minute))
	f.now = f.now.Add(2 * time.Minute)
	fb.SessionTurnIdle(idleWorker(), "stop", "third")
	fb.SessionTurnIdle(idleWorker(), "stop", "fourth")
	if f.count() != 2 {
		t.Fatalf("reports after a new message = %d, want 2", f.count())
	}
}

func TestNoAutomaticReportWhenNotAFinishedWorkerTurn(t *testing.T) {
	needsInput := idleWorker()
	needsInput.Activity.State = domain.ActivityWaitingInput
	orchestrator := idleWorker()
	orchestrator.Kind = domain.KindOrchestrator
	terminated := idleWorker()
	terminated.IsTerminated = true

	for name, c := range map[string]struct {
		rec   domain.SessionRecord
		event string
	}{
		"needs_input":  {needsInput, "stop"},
		"orchestrator": {orchestrator, "stop"},
		"terminated":   {terminated, "stop"},
		"interrupted":  {idleWorker(), "chat.interrupt.reconciled"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeReports{now: t0.Add(time.Hour)}
			fb := newFallback(f, nil)
			fb.SessionMessageReceived("w-1", t0.Add(time.Minute))
			fb.SessionTurnIdle(c.rec, c.event, "x")
			if f.count() != 0 {
				t.Fatalf("reports = %d, want 0", f.count())
			}
		})
	}
}

func TestMissingMessageTextFallsBackToTurnHistory(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, fakeTurns{response: "from history"})
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))

	fb.SessionTurnIdle(idleWorker(), "chat.turn.completed", "")

	if f.count() != 1 || !strings.Contains(f.reports[0].Note, "from history") {
		t.Fatalf("reports = %+v", f.reports)
	}
}
