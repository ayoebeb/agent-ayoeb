package report

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// missingReportMessageRunes is how much of the worker's last message the
// automatic report carries.
const missingReportMessageRunes = 600

// missingReportMaxPRs caps the pull requests listed, to stay inside the note limit.
const missingReportMaxPRs = 3

// MissingReportStore is what the fallback reads to decide and to describe.
type MissingReportStore interface {
	ListReportsBySession(context.Context, domain.SessionID) ([]domain.ReportRecord, error)
	ListPRsBySession(context.Context, domain.SessionID) ([]domain.PullRequest, error)
}

// MissingReportCreator files the report; *Service is the production one.
type MissingReportCreator interface {
	Create(context.Context, CreateInput) (domain.ReportRecord, error)
}

// MissingReportTurns reads a session's newest turn, which holds the agent's last
// message for Chat and terminal sessions alike. Optional.
type MissingReportTurns interface {
	SessionHistory(ctx context.Context, id domain.SessionID, offset, limit int) ([]domain.HandoffTurn, int, error)
}

// MissingReportSessions re-reads a session after the grace delay. Optional.
type MissingReportSessions interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// MissingReportBusy reports whether the provider still has background work
// running for a session (Claude background shells and tasks, which keep the
// agent going after its turn ends). Optional; only harnesses that report it
// implement it, and the others rely on the grace delay alone.
type MissingReportBusy interface {
	BackgroundWorkRunning(domain.SessionID) bool
}

const (
	// missingReportGrace is how long a worker must stay idle before it counts as
	// stopped. It covers harnesses with no background-work signal: a worker that
	// ended its turn while a command runs resumes by itself and is not filed.
	missingReportGrace = 2 * time.Minute
	// missingReportMaxBusyChecks bounds how long a provider-reported background
	// task is waited on (30 x 2 minutes). Past it nothing is filed; the worker's
	// next idle starts a new check.
	missingReportMaxBusyChecks = 30
)

// MissingReportDeps configures a MissingReportFallback.
type MissingReportDeps struct {
	Store    MissingReportStore
	Reports  MissingReportCreator
	Turns    MissingReportTurns
	Sessions MissingReportSessions
	Busy     MissingReportBusy
	Logger   *slog.Logger
	// Synchronous files the report on the caller's goroutine with no grace delay.
	// Tests use it; the daemon leaves it off so the activity path never waits on
	// a database read.
	Synchronous bool
	// Grace and After override the grace delay and its timer. Tests only.
	Grace time.Duration
	After func(time.Duration) <-chan time.Time
}

// MissingReportFallback files one automatic report when a worker ends a turn
// without having run `ao report` since the message that started it.
//
// Whether a worker "reported" is read from the report table, not from memory,
// and the automatic report is itself a report. That is what makes it fire at
// most once per received message: a second idle finds the first report.
//
// A turn that ends while background work still runs is not a stopped worker. The
// fallback waits a grace period, re-reads the session, and files only if it is
// still idle and the provider reports no background work. Chat workers on a
// provider that reports it (Claude's session_state_changed) use that signal;
// terminal workers and other harnesses use the grace re-check alone.
type MissingReportFallback struct {
	store    MissingReportStore
	reports  MissingReportCreator
	turns    MissingReportTurns
	sessions MissingReportSessions
	busy     MissingReportBusy
	log      *slog.Logger
	sync     bool
	grace    time.Duration
	after    func(time.Duration) <-chan time.Time

	mu      sync.Mutex
	lastMsg map[domain.SessionID]time.Time
	// gen is the newest idle check per session; an older check gives up.
	gen map[domain.SessionID]uint64
	// fileMu serializes the final check and filing so two checks cannot both file.
	fileMu sync.Mutex
}

// NewMissingReportFallback builds the fallback.
func NewMissingReportFallback(d MissingReportDeps) *MissingReportFallback {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	grace := d.Grace
	if grace == 0 && !d.Synchronous {
		grace = missingReportGrace
	}
	after := d.After
	if after == nil {
		after = time.After
	}
	return &MissingReportFallback{
		store: d.Store, reports: d.Reports, turns: d.Turns, sessions: d.Sessions, busy: d.Busy,
		log: log, sync: d.Synchronous, grace: grace, after: after,
		lastMsg: map[domain.SessionID]time.Time{}, gen: map[domain.SessionID]uint64{},
	}
}

// SessionMessageReceived records when the session's latest message arrived.
func (f *MissingReportFallback) SessionMessageReceived(id domain.SessionID, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at.After(f.lastMsg[id]) {
		f.lastMsg[id] = at
	}
}

// SessionTurnIdle runs when a running turn ended and the session went idle.
// event is the activity event that ended it.
func (f *MissingReportFallback) SessionTurnIdle(rec domain.SessionRecord, event, finalMessage string) {
	// A person's interrupt is not a worker that forgot to report. Terminal and
	// Chat reaching idle after an approval or a question never get here: those
	// states are needs-input, not a turn end.
	if rec.Kind != domain.KindWorker || rec.IsTerminated || rec.Activity.State != domain.ActivityIdle ||
		strings.Contains(event, "interrupt") {
		return
	}
	// A newer idle supersedes any check still waiting: the worker ran again in
	// between, so the older check's view of "idle" is stale.
	f.mu.Lock()
	f.gen[rec.ID]++
	gen := f.gen[rec.ID]
	f.mu.Unlock()
	run := func() {
		if !f.settle(rec, gen) {
			return
		}
		f.fileMu.Lock()
		defer f.fileMu.Unlock()
		if !f.current(rec.ID, gen) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := f.file(ctx, rec, finalMessage); err != nil {
			f.log.Warn("automatic missing-report failed", "session", rec.ID, "error", err)
		}
	}
	if f.sync {
		run()
		return
	}
	go run()
}

func (f *MissingReportFallback) current(id domain.SessionID, gen uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen[id] == gen
}

// settle reports whether the session is still stopped after the grace delay:
// idle, not terminated, and with no background work the provider knows of.
func (f *MissingReportFallback) settle(rec domain.SessionRecord, gen uint64) bool {
	for checks := 0; ; checks++ {
		if f.grace > 0 {
			<-f.after(f.grace)
		}
		if !f.current(rec.ID, gen) {
			return false
		}
		if f.sessions != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cur, ok, err := f.sessions.GetSession(ctx, rec.ID)
			cancel()
			if err == nil && (!ok || cur.IsTerminated || cur.Activity.State != domain.ActivityIdle) {
				return false // it resumed; its next idle starts a new check
			}
		}
		if f.busy == nil || !f.busy.BackgroundWorkRunning(rec.ID) {
			return true
		}
		if checks+1 >= missingReportMaxBusyChecks || f.grace == 0 {
			return false
		}
	}
}

func (f *MissingReportFallback) file(ctx context.Context, rec domain.SessionRecord, finalMessage string) error {
	return f.fileOnce(ctx, rec, finalMessage, missingReportNote)
}

// since is when the worker last received a message: the point a report has to
// follow to count as its answer. With no message seen in this daemon run (it
// restarted mid-turn) it falls back to the latest human prompt, the latest
// deliberate direction, and then the session's start.
func (f *MissingReportFallback) since(rec domain.SessionRecord) time.Time {
	f.mu.Lock()
	since := f.lastMsg[rec.ID]
	f.mu.Unlock()
	for _, at := range []time.Time{rec.Metadata.LatestUserPromptAt, rec.Metadata.LatestInteractionAt, rec.CreatedAt} {
		if at.After(since) {
			since = at
		}
	}
	return since
}

// Unfinished reports whether the worker has not run `ao report --done` since the
// message it last received.
func (f *MissingReportFallback) Unfinished(ctx context.Context, rec domain.SessionRecord) (bool, error) {
	since := f.since(rec)
	existing, err := f.store.ListReportsBySession(ctx, rec.ID)
	if err != nil {
		return false, err
	}
	for _, r := range existing {
		if r.State == domain.ReportDone && !r.CreatedAt.Before(since) {
			return false, nil
		}
	}
	return true, nil
}

// FileStopped files the one automatic report for a worker whose agent stopped
// and could not be kept running. Unlike the missing report it does not yield to
// an earlier report: the orchestrator must hear that the worker is gone.
func (f *MissingReportFallback) FileStopped(ctx context.Context, rec domain.SessionRecord, reason string) error {
	text := fmt.Sprintf("[AUTOMATIC REPORT from AO, not written by the worker] worker %s stopped and could not be resumed: %s",
		rec.ID, cmp.Or(strings.TrimSpace(reason), "no error recorded"))
	_, err := f.reports.Create(ctx, CreateInput{
		SessionID: rec.ID, State: domain.ReportStuck, Immediate: true,
		Note: truncateRunes(text, domain.MaxReportTextCharacters),
	})
	return err
}

// FileStalled files the watchdog's one automatic report for a worker with
// unfinished work that has sat in state for idleFor without any report. It
// shares the missing report's "already reported" check, so it files at most one
// report per received message.
func (f *MissingReportFallback) FileStalled(ctx context.Context, rec domain.SessionRecord, state string, idleFor time.Duration) error {
	return f.fileOnce(ctx, rec, "", func(message string, prs []domain.PullRequest) string {
		return stalledNote(state, idleFor, message, prs)
	})
}

// fileOnce files an automatic checkpoint unless the worker already reported
// since its last message; an earlier automatic report counts.
func (f *MissingReportFallback) fileOnce(ctx context.Context, rec domain.SessionRecord, finalMessage string, note func(string, []domain.PullRequest) string) error {
	since := f.since(rec)
	existing, err := f.store.ListReportsBySession(ctx, rec.ID)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if !r.CreatedAt.Before(since) {
			return nil
		}
	}
	prs, err := f.store.ListPRsBySession(ctx, rec.ID)
	if err != nil {
		f.log.Warn("automatic missing-report: list PRs", "session", rec.ID, "error", err)
	}
	message := strings.TrimSpace(finalMessage)
	if message == "" && f.turns != nil {
		if turns, _, err := f.turns.SessionHistory(ctx, rec.ID, 0, 1); err == nil && len(turns) > 0 {
			message = strings.TrimSpace(turns[0].AgentResponse)
		}
	}
	message = cmp.Or(message, strings.TrimSpace(rec.Metadata.LatestAssistantUpdate))
	_, err = f.reports.Create(ctx, CreateInput{
		SessionID: rec.ID, State: domain.ReportCheckpoint, Immediate: true,
		Note: note(message, prs),
	})
	return err
}

// stalledNote words the watchdog's report with the missing report's marker and
// layout, so the orchestrator reads both alike.
func stalledNote(state string, idleFor time.Duration, message string, prs []domain.PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[AUTOMATIC REPORT from AO, not written by the worker] The worker has been %s for %d minutes with unfinished work and has not run `ao report`.",
		state, int(idleFor.Minutes()))
	writeReportTail(&b, message, prs)
	return truncateRunes(b.String(), domain.MaxReportTextCharacters)
}

// missingReportNote words the automatic report. It opens with a fixed marker so
// the orchestrator can tell it from anything the worker wrote.
func missingReportNote(message string, prs []domain.PullRequest) string {
	var b strings.Builder
	b.WriteString("[AUTOMATIC REPORT from AO, not written by the worker] The worker stopped without running `ao report` after its last message.")
	writeReportTail(&b, message, prs)
	return truncateRunes(b.String(), domain.MaxReportTextCharacters)
}

// writeReportTail adds the worker's last message and the PRs AO tracks for it.
func writeReportTail(b *strings.Builder, message string, prs []domain.PullRequest) {
	b.WriteString("\nLast message: ")
	if message == "" {
		b.WriteString("(none available)")
	} else {
		b.WriteString(truncateRunes(message, missingReportMessageRunes))
	}
	if len(prs) > 0 {
		b.WriteString("\nPRs AO tracks for it:")
		for i, p := range prs {
			if i == missingReportMaxPRs {
				fmt.Fprintf(b, "\n(+%d more)", len(prs)-i)
				break
			}
			fmt.Fprintf(b, "\n%s (%s)", p.URL, prState(p))
		}
	} else {
		b.WriteString("\nPRs AO tracks for it: none")
	}
}

func prState(p domain.PullRequest) string {
	switch {
	case p.Merged:
		return "merged"
	case p.Closed:
		return "closed"
	case p.Draft:
		return "draft"
	}
	return "open"
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:limit-1]), " \n") + "…"
}
