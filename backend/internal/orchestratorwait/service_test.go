package orchestratorwait

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var ctx = context.Background()

// fakeWorld stands in for the store and the notification manager together, so a
// test can follow one wait from declaration to resolution.
type fakeWorld struct {
	now      time.Time
	sessions map[domain.SessionID]domain.SessionRecord
	rows     []domain.NotificationRecord
	reminded []string // notification ids published as reminders
	nextID   int
	enabled  bool
	after    time.Duration
}

func newWorld() *fakeWorld {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	return &fakeWorld{
		now:     now,
		enabled: true,
		after:   10 * time.Minute,
		sessions: map[domain.SessionID]domain.SessionRecord{
			"mer-orchestrator-1": {ID: "mer-orchestrator-1", ProjectID: "mer", Kind: domain.KindOrchestrator, DisplayName: "Orchestrator"},
			"mer-1":              {ID: "mer-1", ProjectID: "mer", Kind: domain.KindWorker},
			"mer-orchestrator-2": {ID: "mer-orchestrator-2", ProjectID: "mer", Kind: domain.KindOrchestrator, IsTerminated: true},
		},
	}
}

func (w *fakeWorld) service() *Service {
	svc := New(Deps{Store: w, Notifier: w, Settings: w, Clock: func() time.Time { return w.now }})
	return svc
}

func (w *fakeWorld) open() []domain.NotificationRecord {
	var out []domain.NotificationRecord
	for _, row := range w.rows {
		if row.ResolvedAt.IsZero() {
			out = append(out, row)
		}
	}
	return out
}

// Store.
func (w *fakeWorld) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	rec, ok := w.sessions[id]
	return rec, ok, nil
}

func (w *fakeWorld) ListOpenOrchestratorWaiting(context.Context) ([]domain.NotificationRecord, error) {
	return w.open(), nil
}

func (w *fakeWorld) ListDueOrchestratorWaitingReminders(_ context.Context, cutoff time.Time) ([]domain.NotificationRecord, error) {
	var out []domain.NotificationRecord
	for _, row := range w.open() {
		if row.RemindedAt.IsZero() && !row.CreatedAt.After(cutoff) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (w *fakeWorld) MarkNotificationReminded(_ context.Context, id string, at time.Time) (domain.NotificationRecord, bool, error) {
	for i, row := range w.rows {
		if row.ID == id && row.ResolvedAt.IsZero() && row.RemindedAt.IsZero() {
			w.rows[i].RemindedAt = at
			return w.rows[i], true, nil
		}
	}
	return domain.NotificationRecord{}, false, nil
}

// Notifier.
func (w *fakeWorld) Notify(_ context.Context, intent ports.NotificationIntent) error {
	w.nextID++
	w.rows = append(w.rows, domain.NotificationRecord{
		ID: "ntf_" + string(rune('a'+w.nextID)), SessionID: intent.SessionID, ProjectID: intent.ProjectID,
		Type: intent.Type, Title: "mer: orchestrator is waiting for you", Body: intent.Detail, Status: domain.NotificationUnread,
		CreatedAt: intent.CreatedAt, SourceKey: intent.SourceKey,
	})
	return nil
}

func (w *fakeWorld) Resolve(_ context.Context, res ports.NotificationResolution) error {
	for i, row := range w.rows {
		if row.SessionID == res.SessionID && row.Type == res.Type && row.ResolvedAt.IsZero() {
			w.rows[i].ResolvedAt = res.ResolvedAt
		}
	}
	return nil
}

func (w *fakeWorld) Remind(_ context.Context, rec domain.NotificationRecord) error {
	w.reminded = append(w.reminded, rec.ID)
	return nil
}

// Settings.
func (w *fakeWorld) OrchestratorWaiting(context.Context) (bool, time.Duration, error) {
	return w.enabled, w.after, nil
}

func apiCode(err error) string {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func TestDeclare_RecordsAWaitWithTheQuestion(t *testing.T) {
	w := newWorld()
	outcome, err := w.service().Declare(ctx, "mer-orchestrator-1", "Merge PR 42 now, or wait for review?")
	if err != nil || outcome != OutcomeNotified {
		t.Fatalf("declare = %q, %v", outcome, err)
	}
	rows := w.open()
	if len(rows) != 1 {
		t.Fatalf("open waits = %+v", rows)
	}
	got := rows[0]
	if got.Type != domain.NotificationOrchestratorWaiting || got.SessionID != "mer-orchestrator-1" || got.ProjectID != "mer" ||
		got.Body != "Merge PR 42 now, or wait for review?" {
		t.Fatalf("row = %+v", got)
	}
	if !strings.HasPrefix(got.SourceKey, domain.OrchestratorWaitingSourceKeyPrefix) {
		t.Fatalf("a declared wait needs a source key of its own, got %q", got.SourceKey)
	}
}

// An orchestrator that declares at the end of every turn must not re-notify the
// user for a question that is still open.
func TestDeclare_SameQuestionWhileOpenIsANoOp(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}
	outcome, err := svc.Declare(ctx, "mer-orchestrator-1", "  Ship   it?\n")
	if err != nil || outcome != OutcomeUnchanged {
		t.Fatalf("second declare = %q, %v", outcome, err)
	}
	if len(w.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(w.rows))
	}
}

func TestDeclare_ADifferentQuestionReplacesTheOldOne(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Minute)
	outcome, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it today or Monday?")
	if err != nil || outcome != OutcomeNotified {
		t.Fatalf("declare = %q, %v", outcome, err)
	}
	if len(w.rows) != 2 || w.rows[0].ResolvedAt.IsZero() {
		t.Fatalf("the old question must be resolved: %+v", w.rows)
	}
	if open := w.open(); len(open) != 1 || open[0].Body != "Ship it today or Monday?" {
		t.Fatalf("open = %+v", open)
	}
}

// After the user answers, the next question is a new wait with a new
// notification, even though the old row was never opened.
func TestDeclare_AfterAReplyTheNextQuestionNotifiesAgain(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}
	svc.UserReplied(ctx, "mer-orchestrator-1")
	if len(w.open()) != 0 {
		t.Fatalf("a reply must close the wait: %+v", w.open())
	}
	outcome, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?")
	if err != nil || outcome != OutcomeNotified || len(w.open()) != 1 {
		t.Fatalf("declare after reply = %q, %v, open=%+v", outcome, err, w.open())
	}
}

func TestDeclare_OffInSettingsSendsNothing(t *testing.T) {
	w := newWorld()
	w.enabled = false
	outcome, err := w.service().Declare(ctx, "mer-orchestrator-1", "Ship it?")
	if err != nil || outcome != OutcomeDisabled || len(w.rows) != 0 {
		t.Fatalf("declare = %q, %v, rows=%+v", outcome, err, w.rows)
	}
}

func TestDeclare_Refusals(t *testing.T) {
	w := newWorld()
	svc := w.service()
	for _, tt := range []struct {
		name, id, question, code string
	}{
		{"empty question", "mer-orchestrator-1", " \n\t ", "WAITING_QUESTION_REQUIRED"},
		{"worker", "mer-1", "Ship it?", "NOT_AN_ORCHESTRATOR"},
		{"unknown session", "nope", "Ship it?", "SESSION_NOT_FOUND"},
		{"ended orchestrator", "mer-orchestrator-2", "Ship it?", "SESSION_TERMINATED"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Declare(ctx, domain.SessionID(tt.id), tt.question); apiCode(err) != tt.code {
				t.Fatalf("err = %v (code %q), want %s", err, apiCode(err), tt.code)
			}
			if len(w.rows) != 0 {
				t.Fatalf("a refused declaration left rows: %+v", w.rows)
			}
		})
	}
}

func TestClear_WithdrawsTheOpenWait(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if cleared, err := svc.Clear(ctx, "mer-orchestrator-1"); err != nil || cleared {
		t.Fatalf("nothing open: cleared=%v err=%v", cleared, err)
	}
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}
	if cleared, err := svc.Clear(ctx, "mer-orchestrator-1"); err != nil || !cleared || len(w.open()) != 0 {
		t.Fatalf("clear: cleared=%v err=%v open=%+v", cleared, err, w.open())
	}
	if _, err := svc.Clear(ctx, "mer-1"); apiCode(err) != "NOT_AN_ORCHESTRATOR" {
		t.Fatalf("clear for a worker = %v", err)
	}
}

// A reply to one orchestrator must not close another's question.
func TestUserReplied_ClosesOnlyThatSessionsWait(t *testing.T) {
	w := newWorld()
	w.sessions["other-orchestrator-1"] = domain.SessionRecord{ID: "other-orchestrator-1", ProjectID: "other", Kind: domain.KindOrchestrator}
	svc := w.service()
	for _, id := range []domain.SessionID{"mer-orchestrator-1", "other-orchestrator-1"} {
		if _, err := svc.Declare(ctx, id, "Ship it?"); err != nil {
			t.Fatal(err)
		}
	}
	svc.UserReplied(ctx, "mer-orchestrator-1")
	open := w.open()
	if len(open) != 1 || open[0].SessionID != "other-orchestrator-1" {
		t.Fatalf("open = %+v", open)
	}
	// Cheap and silent when nothing is open.
	svc.UserReplied(ctx, "mer-1")
	var nilSvc *Service
	nilSvc.UserReplied(ctx, "mer-orchestrator-1")
}

func TestRemindDue_AnnouncesOnceAfterTheDelay(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}

	w.now = w.now.Add(9 * time.Minute)
	svc.remindDue(ctx)
	if len(w.reminded) != 0 {
		t.Fatalf("reminded before the delay: %v", w.reminded)
	}

	w.now = w.now.Add(2 * time.Minute) // 11 minutes in
	svc.remindDue(ctx)
	if len(w.reminded) != 1 {
		t.Fatalf("reminders = %v, want one", w.reminded)
	}
	svc.remindDue(ctx)
	w.now = w.now.Add(time.Hour)
	svc.remindDue(ctx)
	if len(w.reminded) != 1 {
		t.Fatalf("a wait is reminded at most once, got %v", w.reminded)
	}
	if waiting, _ := svc.List(ctx); len(waiting) != 1 || !waiting[0].Reminded {
		t.Fatalf("list = %+v", waiting)
	}
}

func TestRemindDue_SkipsAnsweredOffAndZeroDelay(t *testing.T) {
	t.Run("answered before the delay", func(t *testing.T) {
		w := newWorld()
		svc := w.service()
		if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
			t.Fatal(err)
		}
		svc.UserReplied(ctx, "mer-orchestrator-1")
		w.now = w.now.Add(time.Hour)
		svc.remindDue(ctx)
		if len(w.reminded) != 0 {
			t.Fatalf("reminded an answered question: %v", w.reminded)
		}
	})
	t.Run("switched off after declaring", func(t *testing.T) {
		w := newWorld()
		svc := w.service()
		if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
			t.Fatal(err)
		}
		w.enabled = false
		w.now = w.now.Add(time.Hour)
		svc.remindDue(ctx)
		if len(w.reminded) != 0 {
			t.Fatalf("reminded while off: %v", w.reminded)
		}
	})
	t.Run("reminder delay of zero", func(t *testing.T) {
		w := newWorld()
		w.after = 0
		svc := w.service()
		if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(24 * time.Hour)
		svc.remindDue(ctx)
		if len(w.reminded) != 0 {
			t.Fatalf("reminded with the reminder off: %v", w.reminded)
		}
	})
}

// The reminder follows the setting as it is now, so a shorter delay applies to a
// wait that is already open.
func TestRemindDue_UsesTheCurrentDelay(t *testing.T) {
	w := newWorld()
	svc := w.service()
	if _, err := svc.Declare(ctx, "mer-orchestrator-1", "Ship it?"); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(3 * time.Minute)
	w.after = 2 * time.Minute
	svc.remindDue(ctx)
	if len(w.reminded) != 1 {
		t.Fatalf("reminders = %v", w.reminded)
	}
}

func TestNormalizeQuestion(t *testing.T) {
	long := strings.Repeat("é", MaxQuestionRunes+50)
	for _, tt := range []struct{ name, in, want string }{
		{"collapses whitespace and breaks", "  Ship\n\nit \t now?  ", "Ship it now?"},
		{"turns control characters into spaces", "a\x00b\x1b[31mc", "a b [31mc"},
		{"drops bidirectional controls", "pay\u202etxt.exe", "pay txt.exe"},
		{"line separators", "a\u2028b\u2029c", "a b c"},
		{"empty", " \n ", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeQuestion(tt.in); got != tt.want {
				t.Fatalf("NormalizeQuestion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	got := NormalizeQuestion(long)
	if n := len([]rune(got)); n != MaxQuestionRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("long question: %d runes, ends %q", n, got[len(got)-3:])
	}
}
