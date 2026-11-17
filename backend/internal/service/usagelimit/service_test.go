package usagelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const limitMessage = "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)"

// failedAt is 15:20 in Amsterdam, so the limit resets at 18:00 there: 17:00 UTC.
var (
	failedAt = time.Date(2026, 11, 16, 14, 20, 0, 0, time.UTC)
	resetAt  = time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
)

type fakeStore struct {
	mu       sync.Mutex
	turns    []domain.LimitStopTurn // the latest failed turns, as the database would list them
	sessions map[domain.SessionID]domain.SessionRecord
	stops    map[string]*domain.UsageLimitStop
	running  map[string]bool // resumed stops whose continued turn is still running
	resumed  map[string]time.Time
	lastAt   time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions: map[domain.SessionID]domain.SessionRecord{},
		stops:    map[string]*domain.UsageLimitStop{},
		running:  map[string]bool{},
		resumed:  map[string]time.Time{},
	}
}

// addStop registers a session whose latest turn failed on the limit.
func (f *fakeStore) addStop(id domain.SessionID, kind domain.SessionKind, message string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	turn := "turn-" + string(id)
	completed := failedAt
	f.turns = append(f.turns, domain.LimitStopTurn{
		SessionID: id, TurnID: turn, ErrorMessage: message, CompletedAt: &completed, RequestedAt: failedAt.Add(-time.Hour),
	})
	f.sessions[id] = domain.SessionRecord{
		ID: id, ProjectID: "proj", Kind: kind, Harness: domain.HarnessClaudeCode, DisplayName: string(id),
		Mode: domain.SessionModeChat, Activity: domain.Activity{State: domain.ActivityIdle},
	}
	return turn
}

func (f *fakeStore) dropTurn(turn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.turns {
		if t.TurnID == turn {
			f.turns = append(f.turns[:i], f.turns[i+1:]...)
			return
		}
	}
}

func (f *fakeStore) setSession(id domain.SessionID, edit func(*domain.SessionRecord)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.sessions[id]
	edit(&rec)
	f.sessions[id] = rec
}

func (f *fakeStore) ListLimitStopCandidates(_ context.Context, since time.Time) ([]domain.LimitStopTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.LimitStopTurn
	for _, turn := range f.turns {
		stop := f.stops[turn.TurnID]
		// Like the database: a pending stop is listed whatever its age.
		if turn.CompletedAt.Before(since) && (stop == nil || stop.ResumeState != domain.UsageLimitStopPending) {
			continue
		}
		if stop != nil {
			if stop.ResumeState != domain.UsageLimitStopPending {
				continue
			}
			turn.ResumeState, turn.SkipReason, turn.StopResetsAt, turn.RetryAt, turn.Attempts = stop.ResumeState, stop.SkipReason, stop.ResetsAt, stop.RetryAt, stop.Attempts
		}
		out = append(out, turn)
	}
	return out, nil
}

func (f *fakeStore) InsertUsageLimitStop(_ context.Context, stop domain.UsageLimitStop) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stops[stop.TurnID] != nil {
		return false, nil
	}
	stop.ResumeState = domain.UsageLimitStopPending
	f.stops[stop.TurnID] = &stop
	return true, nil
}

func (f *fakeStore) ListPendingUsageLimitStops(context.Context) ([]domain.UsageLimitStop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.UsageLimitStop
	for _, stop := range f.stops {
		if stop.ResumeState == domain.UsageLimitStopPending {
			out = append(out, *stop)
		}
	}
	return out, nil
}

func (f *fakeStore) MarkUsageLimitStopReported(_ context.Context, turnID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops[turnID].ReportedAt = &at
	return nil
}

func (f *fakeStore) SkipUsageLimitStop(_ context.Context, turnID, reason string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stop := f.stops[turnID]
	if stop == nil || stop.ResumeState != domain.UsageLimitStopPending {
		return false, nil
	}
	stop.ResumeState, stop.SkipReason = domain.UsageLimitStopSkipped, reason
	return true, nil
}

func (f *fakeStore) SetPendingUsageLimitStopsDisabled(_ context.Context, disabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, stop := range f.stops {
		switch {
		case disabled && stop.ResumeState == domain.UsageLimitStopPending:
			stop.ResumeState, stop.SkipReason = domain.UsageLimitStopSkipped, domain.UsageLimitOffDisabled
		case !disabled && stop.ResumeState == domain.UsageLimitStopSkipped && stop.SkipReason == domain.UsageLimitOffDisabled:
			stop.ResumeState, stop.SkipReason = domain.UsageLimitStopPending, ""
		}
	}
	return nil
}

func (f *fakeStore) DeferUsageLimitStop(_ context.Context, turnID string, retryAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops[turnID].Attempts++
	f.stops[turnID].RetryAt = &retryAt
	return nil
}

func (f *fakeStore) MarkUsageLimitStopResumed(_ context.Context, turnID string, resumedAt, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stop := f.stops[turnID]
	stop.ResumeState = domain.UsageLimitStopResumed
	f.resumed[turnID] = resumedAt
	f.running[turnID] = true
	f.lastAt = resumedAt
	return nil
}

func (f *fakeStore) CountRunningUsageLimitResumes(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, running := range f.running {
		if running {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) LastUsageLimitResumeAt(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAt, !f.lastAt.IsZero(), nil
}

func (f *fakeStore) CountRecentUsageLimitResumes(_ context.Context, session domain.SessionID, since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for turn, at := range f.resumed {
		if f.stops[turn].SessionID == session && !at.Before(since) {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.sessions[id]
	return rec, ok, nil
}

type fakeSettings struct {
	mu            sync.Mutex
	enabled       bool
	maxConcurrent int
}

func (s *fakeSettings) UsageLimitResume(context.Context) (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled, s.maxConcurrent, nil
}

func (s *fakeSettings) set(enabled bool, maxConcurrent int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled, s.maxConcurrent = enabled, maxConcurrent
}

type fakeAccounts struct{ account string }

func (a fakeAccounts) SessionAccountID(context.Context, domain.SessionID, domain.AgentHarness) (string, error) {
	return a.account, nil
}

// fakeNotifier keeps one notification per source key, like the real store.
type fakeNotifier struct {
	mu      sync.Mutex
	stored  map[string]ports.NotificationIntent
	attempt int
}

func (n *fakeNotifier) Notify(_ context.Context, intent ports.NotificationIntent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attempt++
	if n.stored == nil {
		n.stored = map[string]ports.NotificationIntent{}
	}
	if _, ok := n.stored[intent.SourceKey]; !ok {
		n.stored[intent.SourceKey] = intent
	}
	return nil
}

type fakeReporter struct {
	mu    sync.Mutex
	notes map[domain.SessionID][]string
}

func (r *fakeReporter) ReportUsageLimit(_ context.Context, worker domain.SessionID, note string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.notes == nil {
		r.notes = map[domain.SessionID][]string{}
	}
	r.notes[worker] = append(r.notes[worker], note)
	return nil
}

type sent struct {
	id      domain.SessionID
	message string
	key     string
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
	err  error
}

func (s *fakeSender) SendSemantic(_ context.Context, id domain.SessionID, message, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, sent{id: id, message: message, key: key})
	return nil
}

func (s *fakeSender) ids() []domain.SessionID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.SessionID, 0, len(s.sent))
	for _, m := range s.sent {
		out = append(out, m.id)
	}
	return out
}

type harness struct {
	t        *testing.T
	svc      *Service
	store    *fakeStore
	settings *fakeSettings
	notifier *fakeNotifier
	reporter *fakeReporter
	sender   *fakeSender
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, store: newFakeStore(), settings: &fakeSettings{enabled: true, maxConcurrent: 3},
		notifier: &fakeNotifier{}, reporter: &fakeReporter{}, sender: &fakeSender{},
		now: failedAt.Add(time.Minute),
	}
	h.svc = New(Deps{
		Store: h.store, Settings: h.settings, Accounts: fakeAccounts{account: "acct-1"},
		Notifier: h.notifier, Reporter: h.reporter, Sender: h.sender,
		Now: func() time.Time { return h.now }, Stagger: 20 * time.Second,
	})
	return h
}

func (h *harness) sweep() {
	h.t.Helper()
	if err := h.svc.Sweep(context.Background()); err != nil {
		h.t.Fatalf("sweep: %v", err)
	}
}

// A limit stop is noticed once: the orchestrator is told, the person gets a
// notification, and nothing is sent before the window resets.
func TestLimitStopIsFollowedUpOnceAndNotResumedBeforeReset(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()
	h.sweep()
	if len(h.reporter.notes["w1"]) != 1 {
		t.Fatalf("reports = %d, want 1", len(h.reporter.notes["w1"]))
	}
	note := h.reporter.notes["w1"][0]
	if !strings.Contains(note, "5-hour usage limit") || !strings.Contains(note, "not stuck") || !strings.Contains(note, "continues it automatically") {
		t.Fatalf("note = %q", note)
	}
	if len(h.notifier.stored) != 1 {
		t.Fatalf("notifications = %d, want 1", len(h.notifier.stored))
	}
	for key, intent := range h.notifier.stored {
		if intent.Type != domain.NotificationUsageLimit || !strings.HasPrefix(key, "usage_limit:acct-1|") || !intent.UsageLimitAutoResume || !intent.UsageLimitResetsAt.Equal(resetAt) {
			t.Fatalf("intent = %+v key=%q", intent, key)
		}
	}
	if got := h.sender.ids(); len(got) != 0 {
		t.Fatalf("sent before reset: %v", got)
	}
	if h.store.stops["turn-w1"].ReportedAt == nil {
		t.Fatal("report was not recorded")
	}
}

// Sessions that hit the same account's window share one notification, while
// each worker still gets its own report to the orchestrator.
func TestOneNotificationPerAccountWindowButEachWorkerReported(t *testing.T) {
	h := newHarness(t)
	for _, id := range []domain.SessionID{"w1", "w2", "w3"} {
		h.store.addStop(id, domain.KindWorker, limitMessage)
	}
	h.sweep()
	if len(h.notifier.stored) != 1 {
		t.Fatalf("notifications = %d, want 1 for one account and window", len(h.notifier.stored))
	}
	if len(h.reporter.notes) != 3 {
		t.Fatalf("workers reported = %d, want 3", len(h.reporter.notes))
	}

	// A different account, or a later window, is a separate notification.
	h.svc.accounts = fakeAccounts{account: "acct-2"}
	h.store.addStop("w4", domain.KindWorker, limitMessage)
	h.sweep()
	if len(h.notifier.stored) != 2 {
		t.Fatalf("notifications after a second account = %d, want 2", len(h.notifier.stored))
	}
}

// An orchestrator that is itself stopped is notified about but is not reported
// to anyone.
func TestOrchestratorStopIsNotifiedNotReported(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("orch", domain.KindOrchestrator, limitMessage)
	h.sweep()
	if len(h.notifier.stored) != 1 || len(h.reporter.notes) != 0 {
		t.Fatalf("notifications=%d reports=%d", len(h.notifier.stored), len(h.reporter.notes))
	}
}

// limit hit -> limited -> reset -> auto-resume, with the continue message sent
// once to the stopped session and recorded as resumed.
func TestResumeAfterResetSendsContinueOnce(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()

	h.now = resetAt.Add(10 * time.Second) // inside the grace after the reset
	h.sweep()
	if got := h.sender.ids(); len(got) != 0 {
		t.Fatalf("sent inside the grace period: %v", got)
	}
	h.now = resetAt.Add(time.Minute)
	h.sweep()
	h.sweep()
	if len(h.sender.sent) != 1 {
		t.Fatalf("sent = %+v, want exactly one message", h.sender.sent)
	}
	m := h.sender.sent[0]
	if m.id != "w1" || m.key != "usage-limit-resume:turn-w1" || !strings.Contains(m.message, "5-hour usage limit") || !strings.Contains(m.message, "Continue where you left off") {
		t.Fatalf("message = %+v", m)
	}
	if h.store.stops["turn-w1"].ResumeState != domain.UsageLimitStopResumed {
		t.Fatalf("state = %q", h.store.stops["turn-w1"].ResumeState)
	}
}

// Resumes are staggered and capped: one at a time, 20 seconds apart, and never
// more continued turns running at once than the setting allows.
func TestResumesAreStaggeredAndCapped(t *testing.T) {
	h := newHarness(t)
	h.settings.set(true, 2)
	for i := 1; i <= 4; i++ {
		h.store.addStop(domain.SessionID(fmt.Sprintf("w%d", i)), domain.KindWorker, limitMessage)
	}
	h.sweep()
	h.now = resetAt.Add(time.Minute)

	h.sweep()
	h.sweep() // same instant: inside the stagger
	if len(h.sender.sent) != 1 {
		t.Fatalf("after two sweeps at one instant sent = %d, want 1", len(h.sender.sent))
	}
	h.now = h.now.Add(25 * time.Second)
	h.sweep()
	if len(h.sender.sent) != 2 {
		t.Fatalf("after the stagger sent = %d, want 2", len(h.sender.sent))
	}
	h.now = h.now.Add(25 * time.Second)
	h.sweep()
	if len(h.sender.sent) != 2 {
		t.Fatalf("with two turns running and a cap of 2, sent = %d, want 2", len(h.sender.sent))
	}

	// One continued turn finishes: the next stopped session may go.
	h.store.running["turn-w1"] = false
	h.sweep()
	if len(h.sender.sent) != 3 {
		t.Fatalf("after a slot freed sent = %d, want 3", len(h.sender.sent))
	}
}

// Sessions a person stopped or killed on purpose are never resumed.
func TestManuallyStoppedSessionsAreNeverResumed(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("killed", domain.KindWorker, limitMessage)
	h.store.addStop("exited", domain.KindWorker, limitMessage)
	h.store.addStop("moved-on", domain.KindWorker, limitMessage)
	h.store.addStop("alive", domain.KindWorker, limitMessage)
	h.sweep()

	h.store.setSession("killed", func(r *domain.SessionRecord) { r.IsTerminated = true })
	h.store.setSession("exited", func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityExited })
	h.store.dropTurn("turn-moved-on") // the person sent a new message: the failed turn is no longer the latest

	h.now = resetAt.Add(time.Minute)
	for i := 0; i < 4; i++ {
		h.sweep()
		h.now = h.now.Add(30 * time.Second)
	}
	if got := h.sender.ids(); len(got) != 1 || got[0] != "alive" {
		t.Fatalf("resumed %v, want only [alive]", got)
	}
	for turn, want := range map[string]string{
		"turn-killed": domain.UsageLimitSkipStopped, "turn-exited": domain.UsageLimitSkipStopped, "turn-moved-on": domain.UsageLimitSkipSuperseded,
	} {
		if stop := h.store.stops[turn]; stop.ResumeState != domain.UsageLimitStopSkipped || stop.SkipReason != want {
			t.Fatalf("%s = %q/%q, want skipped/%q", turn, stop.ResumeState, stop.SkipReason, want)
		}
	}
}

// A session that was already stopped on purpose when AO first sees the limit
// is recorded without a notification or a report.
func TestAlreadyStoppedSessionGetsNoFollowUp(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("gone", domain.KindWorker, limitMessage)
	h.store.setSession("gone", func(r *domain.SessionRecord) { r.IsTerminated = true })
	h.sweep()
	h.now = resetAt.Add(time.Minute)
	h.sweep()
	if len(h.notifier.stored) != 0 || len(h.reporter.notes) != 0 || len(h.sender.sent) != 0 {
		t.Fatalf("notifications=%d reports=%d sent=%d", len(h.notifier.stored), len(h.reporter.notes), len(h.sender.sent))
	}
}

// With the setting off nothing is sent, and turning it back on resumes the
// sessions that were parked.
func TestSettingOffParksStopsAndOnReopensThem(t *testing.T) {
	h := newHarness(t)
	h.settings.set(false, 3)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()
	if note := h.reporter.notes["w1"][0]; !strings.Contains(note, "Automatic resume is off") {
		t.Fatalf("note = %q", note)
	}
	h.now = resetAt.Add(time.Minute)
	h.sweep()
	if len(h.sender.sent) != 0 {
		t.Fatalf("sent with the setting off: %v", h.sender.ids())
	}
	h.settings.set(true, 3)
	h.sweep()
	if got := h.sender.ids(); len(got) != 1 || got[0] != "w1" {
		t.Fatalf("after turning the setting on, sent %v", got)
	}
}

// A stop with no reset time cannot be scheduled: it is reported as such and left alone.
func TestUnknownResetIsNotScheduled(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, "You've hit your usage limit.")
	h.sweep()
	h.now = resetAt.Add(24 * time.Hour)
	h.sweep()
	if len(h.sender.sent) != 0 {
		t.Fatalf("sent %v", h.sender.ids())
	}
	if stop := h.store.stops["turn-w1"]; stop.SkipReason != domain.UsageLimitOffUnknownReset {
		t.Fatalf("stop = %+v", stop)
	}
	if note := h.reporter.notes["w1"][0]; !strings.Contains(note, "no reset time") {
		t.Fatalf("note = %q", note)
	}
}

// A person who armed a retry of the turn itself keeps that, and AO sends nothing.
func TestArmedRetryIsLeftToTheRetry(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()
	h.store.mu.Lock()
	h.store.turns[0].RetryArmed = true
	h.store.mu.Unlock()
	h.now = resetAt.Add(time.Minute)
	h.sweep()
	if len(h.sender.sent) != 0 || h.store.stops["turn-w1"].SkipReason != domain.UsageLimitOffRetryArmed {
		t.Fatalf("sent=%d stop=%+v", len(h.sender.sent), h.store.stops["turn-w1"])
	}
}

// A session that stops again right after being continued is not continued in a loop.
func TestRepeatedStopRightAfterResumeIsNotContinuedAgain(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()
	h.now = resetAt.Add(time.Minute)
	h.sweep()
	if len(h.sender.sent) != 1 {
		t.Fatalf("sent = %d", len(h.sender.sent))
	}
	// The continued turn failed on the limit again within minutes, and the
	// provider says it resets almost at once: the limit is not really lifted.
	h.store.dropTurn("turn-w1")
	h.store.running["turn-w1"] = false
	again := resetAt.Add(3 * time.Minute)
	soon := again.Add(time.Minute)
	h.store.mu.Lock()
	h.store.turns = append(h.store.turns, domain.LimitStopTurn{
		SessionID: "w1", TurnID: "turn-w1-again", ErrorMessage: limitMessage,
		CompletedAt: &again, RequestedAt: again.Add(-time.Minute), StoredResetsAt: &soon,
	})
	h.store.mu.Unlock()
	h.now = again.Add(3 * time.Minute)
	h.sweep()
	h.now = h.now.Add(time.Minute)
	h.sweep()
	if len(h.sender.sent) != 1 {
		t.Fatalf("sent = %d, want the second stop left alone", len(h.sender.sent))
	}
	if stop := h.store.stops["turn-w1-again"]; stop.SkipReason != domain.UsageLimitOffRepeated {
		t.Fatalf("second stop = %+v", stop)
	}
}

func TestSendFailures(t *testing.T) {
	t.Run("a session that is gone is skipped", func(t *testing.T) {
		h := newHarness(t)
		h.store.addStop("w1", domain.KindWorker, limitMessage)
		h.sweep()
		h.sender.err = errors.Join(ErrSessionGone, errors.New("terminated"))
		h.now = resetAt.Add(time.Minute)
		h.sweep()
		if stop := h.store.stops["turn-w1"]; stop.ResumeState != domain.UsageLimitStopSkipped || stop.SkipReason != domain.UsageLimitSkipStopped {
			t.Fatalf("stop = %+v", stop)
		}
	})
	t.Run("a busy session is retried later and finally given up on", func(t *testing.T) {
		h := newHarness(t)
		h.store.addStop("w1", domain.KindWorker, limitMessage)
		h.sweep()
		h.sender.err = errors.New("composer busy")
		h.now = resetAt.Add(time.Minute)
		h.sweep()
		stop := h.store.stops["turn-w1"]
		if stop.ResumeState != domain.UsageLimitStopPending || stop.Attempts != 1 || stop.RetryAt == nil {
			t.Fatalf("after one failure stop = %+v", stop)
		}
		h.sweep() // still inside the retry delay
		if h.store.stops["turn-w1"].Attempts != 1 {
			t.Fatalf("retried before its delay: %+v", h.store.stops["turn-w1"])
		}
		for i := 0; i < maxAttempts; i++ {
			h.now = h.now.Add(2 * retryDelay)
			h.sweep()
		}
		if stop := h.store.stops["turn-w1"]; stop.ResumeState != domain.UsageLimitStopSkipped || stop.SkipReason != domain.UsageLimitOffUnavailable {
			t.Fatalf("after the last attempt stop = %+v", stop)
		}
	})
}

// A stop old enough that nobody is waiting on it is recorded, never announced or continued.
func TestStaleStopIsNotActedOn(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	// Seen for the first time long after its reset: nobody is waiting on it.
	h.now = resetAt.Add(domain.UsageLimitResumeHorizon + time.Hour)
	h.store.mu.Lock()
	stale := h.store.turns[0]
	h.store.mu.Unlock()
	if err := h.svc.detect(context.Background(), stale, stale.Limit(), true, h.now); err != nil {
		t.Fatal(err)
	}
	if len(h.notifier.stored) != 0 || len(h.reporter.notes) != 0 {
		t.Fatalf("notifications=%d reports=%d", len(h.notifier.stored), len(h.reporter.notes))
	}
	if stop := h.store.stops["turn-w1"]; stop.SkipReason != domain.UsageLimitOffExpired {
		t.Fatalf("stop = %+v", stop)
	}
}

// weeklyStop is a worker that stopped on a weekly limit resetting nine days
// after it stopped: far past both the old twelve-hour window and the scan window.
func weeklyStop(h *harness) (turn string, reset time.Time) {
	turn = h.store.addStop("w1", domain.KindWorker, "You've hit your weekly limit")
	reset = failedAt.Add(9 * 24 * time.Hour)
	h.store.mu.Lock()
	h.store.turns[0].StoredResetsAt = &reset
	h.store.mu.Unlock()
	return turn, reset
}

// A stop still waiting for a reset that is days away is not superseded or
// expired because it grew old: it is resumed once the reset arrives.
func TestPendingStopOlderThanTheScanWindowIsStillResumedAtItsReset(t *testing.T) {
	h := newHarness(t)
	turn, reset := weeklyStop(h)
	h.sweep()
	if stop := h.store.stops[turn]; stop == nil || stop.ResumeState != domain.UsageLimitStopPending {
		t.Fatalf("stop = %+v, want pending", stop)
	}

	// A day later, then past the scan window: still waiting, neither superseded nor expired.
	for _, at := range []time.Time{failedAt.Add(24 * time.Hour), failedAt.Add(domain.UsageLimitScanWindow + time.Hour)} {
		h.now = at
		h.sweep()
		if stop := h.store.stops[turn]; stop.ResumeState != domain.UsageLimitStopPending {
			t.Fatalf("at %v the stop was closed: %+v", at, stop)
		}
	}
	if len(h.sender.sent) != 0 {
		t.Fatalf("sent before the reset: %v", h.sender.ids())
	}

	h.now = reset.Add(time.Minute)
	h.sweep()
	if got := h.sender.ids(); len(got) != 1 || got[0] != "w1" {
		t.Fatalf("sent %v at the reset, want [w1]", got)
	}
}

// An old pending stop whose turn really was superseded is still closed as that.
func TestOldPendingStopIsSupersededWhenItsTurnIsNoLongerTheLatest(t *testing.T) {
	h := newHarness(t)
	turn, _ := weeklyStop(h)
	h.sweep()
	h.store.dropTurn(turn)
	h.now = failedAt.Add(domain.UsageLimitScanWindow + time.Hour)
	h.sweep()
	if stop := h.store.stops[turn]; stop.ResumeState != domain.UsageLimitStopSkipped || stop.SkipReason != domain.UsageLimitSkipSuperseded {
		t.Fatalf("stop = %+v, want skipped/superseded", stop)
	}
}

// A pending stop that outlives its reset by more than the horizon (no slot ever
// freed, or the daemon was off) is closed as expired, not resumed and not
// reported as superseded.
func TestPendingStopTooLongPastItsResetIsExpired(t *testing.T) {
	h := newHarness(t)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	h.sweep()
	h.now = resetAt.Add(domain.UsageLimitResumeHorizon + time.Hour)
	h.sweep()
	if len(h.sender.sent) != 0 {
		t.Fatalf("an expired stop was resumed: %v", h.sender.ids())
	}
	if stop := h.store.stops["turn-w1"]; stop.ResumeState != domain.UsageLimitStopSkipped || stop.SkipReason != domain.UsageLimitOffExpired {
		t.Fatalf("stop = %+v, want skipped/expired", stop)
	}
}
