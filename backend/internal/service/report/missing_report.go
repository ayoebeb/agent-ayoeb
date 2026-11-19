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

// MissingReportDeps configures a MissingReportFallback.
type MissingReportDeps struct {
	Store   MissingReportStore
	Reports MissingReportCreator
	Turns   MissingReportTurns
	Logger  *slog.Logger
	// Synchronous files the report on the caller's goroutine. Tests use it; the
	// daemon leaves it off so the activity path never waits on a database read.
	Synchronous bool
}

// MissingReportFallback files one automatic report when a worker ends a turn
// without having run `ao report` since the message that started it.
//
// Whether a worker "reported" is read from the report table, not from memory,
// and the automatic report is itself a report. That is what makes it fire at
// most once per received message: a second idle finds the first report.
type MissingReportFallback struct {
	store   MissingReportStore
	reports MissingReportCreator
	turns   MissingReportTurns
	log     *slog.Logger
	sync    bool

	mu       sync.Mutex
	lastMsg  map[domain.SessionID]time.Time
	inflight map[domain.SessionID]bool
}

// NewMissingReportFallback builds the fallback.
func NewMissingReportFallback(d MissingReportDeps) *MissingReportFallback {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &MissingReportFallback{
		store: d.Store, reports: d.Reports, turns: d.Turns, log: log, sync: d.Synchronous,
		lastMsg: map[domain.SessionID]time.Time{}, inflight: map[domain.SessionID]bool{},
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
	f.mu.Lock()
	if f.inflight[rec.ID] {
		f.mu.Unlock()
		return
	}
	f.inflight[rec.ID] = true
	f.mu.Unlock()
	run := func() {
		defer func() {
			f.mu.Lock()
			delete(f.inflight, rec.ID)
			f.mu.Unlock()
		}()
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

func (f *MissingReportFallback) file(ctx context.Context, rec domain.SessionRecord, finalMessage string) error {
	f.mu.Lock()
	since := f.lastMsg[rec.ID]
	f.mu.Unlock()
	// With no message seen in this daemon run (it restarted mid-turn) fall back
	// to the latest human prompt and then the session's start: any report filed
	// since, including an earlier automatic one, still counts.
	if rec.Metadata.LatestUserPromptAt.After(since) {
		since = rec.Metadata.LatestUserPromptAt
	}
	if rec.CreatedAt.After(since) {
		since = rec.CreatedAt
	}
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
		Note: missingReportNote(message, prs),
	})
	return err
}

// missingReportNote words the automatic report. It opens with a fixed marker so
// the orchestrator can tell it from anything the worker wrote.
func missingReportNote(message string, prs []domain.PullRequest) string {
	var b strings.Builder
	b.WriteString("[AUTOMATIC REPORT from AO, not written by the worker] The worker stopped without running `ao report` after its last message.")
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
				fmt.Fprintf(&b, "\n(+%d more)", len(prs)-i)
				break
			}
			fmt.Fprintf(&b, "\n%s (%s)", p.URL, prState(p))
		}
	} else {
		b.WriteString("\nPRs AO tracks for it: none")
	}
	return truncateRunes(b.String(), domain.MaxReportTextCharacters)
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
