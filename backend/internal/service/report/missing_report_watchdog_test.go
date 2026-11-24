package report

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStalledFilesOneReportPerMessage(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, nil)
	fb.SessionMessageReceived("w-1", t0.Add(time.Minute))
	rec := idleWorker()
	rec.Metadata.LatestAssistantUpdate = "waiting on the build"

	for range 3 {
		if err := fb.FileStalled(context.Background(), rec, "idle", 25*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != 1 {
		t.Fatalf("reports = %d, want exactly 1", f.count())
	}
	note := f.reports[0].Note
	for _, want := range []string{"[AUTOMATIC REPORT", "idle for 25 minutes", "waiting on the build"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
	if err := domain.ValidateReportContent(f.reports[0].State, note, "", nil); err != nil {
		t.Errorf("note is not a valid report: %v", err)
	}
}

func TestUnfinishedUntilDoneAfterTheLastMessage(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, nil)
	rec := idleWorker()
	ctx := context.Background()

	if u, err := fb.Unfinished(ctx, rec); err != nil || !u {
		t.Fatalf("no report: unfinished = %v, %v; want true", u, err)
	}
	f.reports = append(f.reports, domain.ReportRecord{SessionID: "w-1", State: domain.ReportCheckpoint, CreatedAt: t0.Add(time.Minute)})
	if u, _ := fb.Unfinished(ctx, rec); !u {
		t.Error("a checkpoint is not done: want unfinished")
	}
	f.reports = append(f.reports, domain.ReportRecord{SessionID: "w-1", State: domain.ReportDone, CreatedAt: t0.Add(2 * time.Minute)})
	if u, _ := fb.Unfinished(ctx, rec); u {
		t.Error("done after the last message: want finished")
	}
	rec.Metadata.LatestInteractionAt = t0.Add(3 * time.Minute) // a newer message reopens the work
	if u, _ := fb.Unfinished(ctx, rec); !u {
		t.Error("a message after done: want unfinished again")
	}
}

func TestFileStoppedIgnoresEarlierReports(t *testing.T) {
	f := &fakeReports{now: t0.Add(time.Hour)}
	fb := newFallback(f, nil)
	f.reports = append(f.reports, domain.ReportRecord{SessionID: "w-1", State: domain.ReportCheckpoint, CreatedAt: t0.Add(time.Minute)})

	if err := fb.FileStopped(context.Background(), idleWorker(), "agent binary missing"); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatalf("reports = %d, want the stop report added", f.count())
	}
	r := f.reports[1]
	if r.State != domain.ReportStuck || !strings.Contains(r.Note, "worker w-1 stopped and could not be resumed: agent binary missing") {
		t.Errorf("unexpected stop report: %+v", r)
	}
	if err := domain.ValidateReportContent(r.State, r.Note, "", nil); err != nil {
		t.Errorf("note is not a valid report: %v", err)
	}
}
