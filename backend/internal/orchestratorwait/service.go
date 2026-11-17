// Package orchestratorwait tracks when a project's orchestrator is waiting for
// the user, and makes sure the user is told.
//
// The orchestrator is the user's main contact: a question it asks that nobody
// sees stalls every worker behind it. A wait comes from two places:
//
//   - a pending approval or input request, which lifecycle already observes as
//     the session entering the needs-input family and reports as an
//     orchestrator_waiting notification;
//   - a turn that ended with a plain-text question. Nothing observes that, so
//     the orchestrator declares it with `ao notify-user --waiting "<question>"`,
//     which reaches Declare. An explicit command, not a guess from the text of
//     its last message, is what keeps this from misfiring.
//
// Either way the wait is one open orchestrator_waiting notification row. It
// closes when the user responds (a human message, or the approval being
// answered), when the orchestrator withdraws it, or when the session ends. The
// package announces a wait that is still open after the configured delay once
// more, and never again.
package orchestratorwait

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// MaxQuestionRunes caps the one-line question. It is shown in a toast and a
	// phone banner, so a long one would only be cut off there.
	MaxQuestionRunes = 240

	// reminderTick is how often open waits are checked against the reminder
	// delay. The delay is minutes, so this only has to be finer than a minute.
	reminderTick = 30 * time.Second
)

// Store is the persistence the service needs. *store.Store satisfies it.
type Store interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	ListOpenOrchestratorWaiting(ctx context.Context) ([]domain.NotificationRecord, error)
	ListDueOrchestratorWaitingReminders(ctx context.Context, cutoff time.Time) ([]domain.NotificationRecord, error)
	MarkNotificationReminded(ctx context.Context, id string, at time.Time) (domain.NotificationRecord, bool, error)
}

// Notifier produces the notification side effects. *notify.Manager satisfies it.
type Notifier interface {
	Notify(ctx context.Context, intent ports.NotificationIntent) error
	Resolve(ctx context.Context, res ports.NotificationResolution) error
	Remind(ctx context.Context, rec domain.NotificationRecord) error
}

// Settings is the user's preference for this feature.
type Settings interface {
	// OrchestratorWaiting reports whether the notification is on and how long a
	// wait stays unanswered before it is announced a second time (0: never).
	OrchestratorWaiting(ctx context.Context) (enabled bool, renotifyAfter time.Duration, err error)
}

// Service declares, clears and reminds about orchestrator waits.
type Service struct {
	store    Store
	notifier Notifier
	settings Settings
	clock    func() time.Time
	newKey   func() string
}

// Deps configures a Service.
type Deps struct {
	Store    Store
	Notifier Notifier
	Settings Settings
	Clock    func() time.Time
}

// New constructs the service.
func New(d Deps) *Service {
	s := &Service{store: d.Store, notifier: d.Notifier, settings: d.Settings, clock: d.Clock}
	if s.clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
	}
	s.newKey = func() string { return domain.OrchestratorWaitingSourceKeyPrefix + uuid.NewString() }
	return s
}

// Outcome reports what Declare did.
type Outcome string

const (
	// OutcomeNotified means a new wait was recorded and the user was notified.
	OutcomeNotified Outcome = "notified"
	// OutcomeUnchanged means the same question was already open: nothing new was
	// announced.
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeDisabled means the user switched the notification off.
	OutcomeDisabled Outcome = "disabled"
)

// Declare records that the orchestrator's turn ended waiting on the user, with a
// one-line question. Declaring the same question again while it is still open is
// a no-op, so an orchestrator that declares at the end of every turn does not
// re-notify. A different question replaces the old one.
func (s *Service) Declare(ctx context.Context, id domain.SessionID, question string) (Outcome, error) {
	question = NormalizeQuestion(question)
	if question == "" {
		return "", apierr.Invalid("WAITING_QUESTION_REQUIRED", "A one-line question for the user is required", nil)
	}
	rec, err := s.orchestrator(ctx, id)
	if err != nil {
		return "", err
	}
	enabled, _, err := s.settings.OrchestratorWaiting(ctx)
	if err != nil {
		return "", fmt.Errorf("orchestrator waiting: read settings: %w", err)
	}
	if !enabled {
		return OutcomeDisabled, nil
	}
	open, err := s.open(ctx, id)
	if err != nil {
		return "", err
	}
	if len(open) > 0 && open[0].Body == question {
		return OutcomeUnchanged, nil
	}
	if len(open) > 0 {
		// The orchestrator is asking something else now: the old question is moot.
		if err := s.notifier.Resolve(ctx, ports.NotificationResolution{
			Type: domain.NotificationOrchestratorWaiting, SessionID: id, ResolvedAt: s.clock(),
		}); err != nil {
			return "", fmt.Errorf("orchestrator waiting: replace previous question: %w", err)
		}
	}
	if err := s.notifier.Notify(ctx, ports.NotificationIntent{
		Type:               domain.NotificationOrchestratorWaiting,
		SessionID:          id,
		ProjectID:          rec.ProjectID,
		SessionDisplayName: rec.DisplayName,
		Detail:             question,
		SourceKey:          s.newKey(),
		CreatedAt:          s.clock(),
	}); err != nil {
		return "", fmt.Errorf("orchestrator waiting: notify: %w", err)
	}
	return OutcomeNotified, nil
}

// Clear withdraws an open wait, for example because the orchestrator found the
// answer itself. It reports whether one was open.
func (s *Service) Clear(ctx context.Context, id domain.SessionID) (bool, error) {
	if _, err := s.orchestrator(ctx, id); err != nil {
		return false, err
	}
	open, err := s.open(ctx, id)
	if err != nil {
		return false, err
	}
	if len(open) == 0 {
		return false, nil
	}
	if err := s.notifier.Resolve(ctx, ports.NotificationResolution{
		Type: domain.NotificationOrchestratorWaiting, SessionID: id, ResolvedAt: s.clock(),
	}); err != nil {
		return false, fmt.Errorf("orchestrator waiting: clear: %w", err)
	}
	return true, nil
}

// UserReplied closes the wait because the human answered the orchestrator. It is
// best-effort and cheap when nothing is open, so message paths can call it for
// every human message.
func (s *Service) UserReplied(ctx context.Context, id domain.SessionID) {
	if s == nil {
		return
	}
	open, err := s.open(ctx, id)
	if err != nil || len(open) == 0 {
		if err != nil {
			slog.Default().Warn("orchestrator waiting: read open waits on reply", "session", id, "err", err)
		}
		return
	}
	if err := s.notifier.Resolve(ctx, ports.NotificationResolution{
		Type: domain.NotificationOrchestratorWaiting, SessionID: id, ResolvedAt: s.clock(),
	}); err != nil {
		slog.Default().Warn("orchestrator waiting: clear on reply", "session", id, "err", err)
	}
}

// Waiting is one orchestrator that is waiting for the user.
type Waiting struct {
	NotificationID string
	SessionID      domain.SessionID
	ProjectID      domain.ProjectID
	Title          string
	Question       string
	Since          time.Time
	Reminded       bool
}

// List returns every orchestrator waiting for the user, newest first. It
// includes waits whose notification the user already dismissed.
func (s *Service) List(ctx context.Context) ([]Waiting, error) {
	rows, err := s.store.ListOpenOrchestratorWaiting(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Waiting, 0, len(rows))
	for _, row := range rows {
		out = append(out, Waiting{
			NotificationID: row.ID,
			SessionID:      row.SessionID,
			ProjectID:      row.ProjectID,
			Title:          row.Title,
			Question:       row.Body,
			Since:          row.CreatedAt,
			Reminded:       !row.RemindedAt.IsZero(),
		})
	}
	return out, nil
}

// RunReminders announces still-open waits a second time once they have waited
// longer than the configured delay, until ctx is cancelled. It blocks, so
// callers run it in a goroutine.
func (s *Service) RunReminders(ctx context.Context) {
	ticker := time.NewTicker(reminderTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.remindDue(ctx)
		}
	}
}

// remindDue sends the one reminder for every wait that is due. The store hands
// the reminder to exactly one caller per row, so a daemon restart or two
// overlapping ticks cannot announce a wait twice.
func (s *Service) remindDue(ctx context.Context) {
	enabled, after, err := s.settings.OrchestratorWaiting(ctx)
	if err != nil {
		slog.Default().Warn("orchestrator waiting: read settings for reminders", "err", err)
		return
	}
	if !enabled || after <= 0 {
		return
	}
	now := s.clock()
	due, err := s.store.ListDueOrchestratorWaitingReminders(ctx, now.Add(-after))
	if err != nil {
		slog.Default().Warn("orchestrator waiting: list due reminders", "err", err)
		return
	}
	for _, row := range due {
		claimed, ok, err := s.store.MarkNotificationReminded(ctx, row.ID, now)
		if err != nil {
			slog.Default().Warn("orchestrator waiting: claim reminder", "notification", row.ID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		if err := s.notifier.Remind(ctx, claimed); err != nil {
			slog.Default().Warn("orchestrator waiting: publish reminder", "notification", row.ID, "err", err)
		}
	}
}

// orchestrator loads the session and refuses anything that is not a live
// project orchestrator: workers have the orchestrator to ask, not the user.
func (s *Service) orchestrator(ctx context.Context, id domain.SessionID) (domain.SessionRecord, error) {
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("orchestrator waiting: read session %s: %w", id, err)
	}
	if !ok {
		return domain.SessionRecord{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	if rec.Kind != domain.KindOrchestrator {
		return domain.SessionRecord{}, apierr.Invalid(
			"NOT_AN_ORCHESTRATOR",
			"Only a project orchestrator can tell the user it is waiting; a worker asks its orchestrator",
			nil,
		)
	}
	if rec.IsTerminated {
		return domain.SessionRecord{}, apierr.Conflict("SESSION_TERMINATED", "The orchestrator session has ended", nil)
	}
	return rec, nil
}

// open returns the session's open waits, newest first.
func (s *Service) open(ctx context.Context, id domain.SessionID) ([]domain.NotificationRecord, error) {
	rows, err := s.store.ListOpenOrchestratorWaiting(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator waiting: list open waits: %w", err)
	}
	var out []domain.NotificationRecord
	for _, row := range rows {
		if row.SessionID == id {
			out = append(out, row)
		}
	}
	return out, nil
}

// NormalizeQuestion turns the text the orchestrator passed into one safe line:
// control and invisible characters become spaces, whitespace collapses, and the
// result is cut to MaxQuestionRunes. Text the orchestrator wrote ends up in an OS
// banner and a phone push, so it must not be able to carry line breaks or
// bidirectional controls that change what the user reads.
func NormalizeQuestion(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, text)
	line := strings.Join(strings.Fields(cleaned), " ")
	if utf8.RuneCountInString(line) > MaxQuestionRunes {
		runes := []rune(line)
		line = strings.TrimSpace(string(runes[:MaxQuestionRunes-1])) + "…"
	}
	return line
}
