package domain

import (
	"errors"
	"fmt"
	"time"
)

// NotificationType identifies a user-facing notification kind persisted for the dashboard.
type NotificationType string

const (
	// NotificationNeedsInput means an agent session is waiting for user input.
	NotificationNeedsInput NotificationType = "needs_input"
	// NotificationReadyToMerge means a PR has no known merge blockers.
	NotificationReadyToMerge NotificationType = "ready_to_merge"
	// NotificationPRMerged means a tracked PR was merged.
	NotificationPRMerged NotificationType = "pr_merged"
	// NotificationPRClosedUnmerged means a tracked PR closed without merging.
	NotificationPRClosedUnmerged NotificationType = "pr_closed_unmerged"
	// NotificationReviewCompleted means an AO reviewer approved a pull request.
	NotificationReviewCompleted NotificationType = "review_completed"
	// NotificationReviewChangesRequested means an AO reviewer found changes to make.
	NotificationReviewChangesRequested NotificationType = "review_changes_requested"
	// NotificationOrchestratorWaiting means a project's orchestrator, the user's
	// main contact, is waiting for the user: a pending approval or question, or a
	// turn that ended with a question the orchestrator declared with
	// `ao notify-user --waiting`. It stays open until the user responds.
	NotificationOrchestratorWaiting NotificationType = "orchestrator_waiting"
	// NotificationAutopilotParked means Autopilot parked one item for the user: a
	// hard limit it must not cross, or a task run it could not finish on its own.
	// It is sent once per parked item and stays open until the user handles it.
	NotificationAutopilotParked NotificationType = "autopilot_parked"
)

// Valid reports whether t is one of the v1 notification kinds.
func (t NotificationType) Valid() bool {
	switch t {
	case NotificationNeedsInput, NotificationReadyToMerge, NotificationPRMerged, NotificationPRClosedUnmerged,
		NotificationReviewCompleted, NotificationReviewChangesRequested, NotificationOrchestratorWaiting,
		NotificationAutopilotParked:
		return true
	default:
		return false
	}
}

// OrchestratorWaitingSourceKeyPrefix starts the source key of every
// orchestrator_waiting row. The notification table de-duplicates an open row per
// session and type by also counting unread rows, so a wait the user answered but
// never opened would swallow the next one. A key of its own per wait gives each
// its own row.
const OrchestratorWaitingSourceKeyPrefix = "orchestrator_waiting:"

// OrchestratorWaitingSourceKey is the source key of a wait derived from a
// session entering the needs-input family at the given time. The same transition
// always yields the same key, so replaying it cannot notify twice, while a later
// wait gets a row of its own.
func OrchestratorWaitingSourceKey(id SessionID, at time.Time) string {
	return fmt.Sprintf("%s%s:%d", OrchestratorWaitingSourceKeyPrefix, id, at.UnixNano())
}

// NeedsResolution reports whether t describes an issue that stays open until
// something changes (an agent waiting on the user, a PR waiting on a merge).
// Terminal facts — a PR that merged or closed — describe something that already
// happened, so they are surfaced once as unseen and never held as unresolved.
func (t NotificationType) NeedsResolution() bool {
	return t == NotificationNeedsInput || t == NotificationReadyToMerge || t == NotificationOrchestratorWaiting ||
		t == NotificationAutopilotParked
}

// AutopilotParkedSourceKeyPrefix starts the source key of every autopilot_parked
// row. Each parked item has a key of its own (the id of its log line), so two
// parked items are two rows and one item can never notify twice.
const AutopilotParkedSourceKeyPrefix = "autopilot_parked:"

// NotificationStatus is the seen state for a stored notification. The stored
// values remain "unread"/"read" for wire and schema compatibility; "unread"
// means the user has not opened the notification panel since it arrived.
type NotificationStatus string

const (
	// NotificationUnread marks a notification that has not been acknowledged.
	NotificationUnread NotificationStatus = "unread"
	// NotificationRead marks a notification that has been acknowledged.
	NotificationRead NotificationStatus = "read"
)

// Valid reports whether s is a supported notification read state.
func (s NotificationStatus) Valid() bool {
	switch s {
	case NotificationUnread, NotificationRead:
		return true
	default:
		return false
	}
}

// NotificationListStatus selects which stored notifications are returned.
type NotificationListStatus string

const (
	// NotificationListUnread returns only notifications that still need acknowledgement.
	NotificationListUnread NotificationListStatus = "unread"
	// NotificationListAll returns both read and unread notifications.
	NotificationListAll NotificationListStatus = "all"
	// NotificationListUnresolved returns notifications whose underlying issue is
	// still open, regardless of whether the user has already seen them.
	NotificationListUnresolved NotificationListStatus = "unresolved"
)

// Valid reports whether s is a supported notification list filter.
func (s NotificationListStatus) Valid() bool {
	switch s {
	case NotificationListUnread, NotificationListAll, NotificationListUnresolved:
		return true
	default:
		return false
	}
}

// NotificationRecord is the durable notification persistence shape.
type NotificationRecord struct {
	ID        string
	SessionID SessionID
	ProjectID ProjectID
	PRURL     string
	Type      NotificationType
	Title     string
	Body      string
	Status    NotificationStatus
	CreatedAt time.Time
	// ResolvedAt is when the underlying issue went away — the session received
	// its input, or the PR stopped waiting on a merge. Zero means still open.
	// Only AO writes it; there is no user-facing "resolve" action.
	ResolvedAt time.Time
	// RemindedAt is when AO re-notified the user about a still-open
	// orchestrator_waiting row. Zero means no reminder was sent. It makes the
	// single reminder survive a daemon restart.
	RemindedAt time.Time
	// SourceKey identifies the durable transition that produced a terminal
	// notification. It is internal dedupe metadata and is not exposed over HTTP.
	SourceKey string
}

// Resolved reports whether the issue behind this notification is closed.
func (r NotificationRecord) Resolved() bool { return !r.ResolvedAt.IsZero() }

// NotificationEventKind distinguishes the live notification stream events.
type NotificationEventKind string

const (
	// NotificationCreated announces a newly persisted notification.
	NotificationCreated NotificationEventKind = "created"
	// NotificationResolved announces that a stored notification's underlying
	// issue went away, so open dashboards can drop it from the unresolved list.
	NotificationResolved NotificationEventKind = "resolved"
	// NotificationReminder announces that a still-open orchestrator_waiting row
	// is being re-notified once. It carries the existing row: clients alert the
	// user again (toast, sound, push) but do not insert a second row.
	NotificationReminder NotificationEventKind = "reminder"
	// NotificationDeleted announces that one stored notification was deleted.
	NotificationDeleted NotificationEventKind = "deleted"
	// NotificationCleared announces that notification history was deleted.
	NotificationCleared NotificationEventKind = "cleared"
)

// NotificationEvent is one live notification-stream message.
type NotificationEvent struct {
	Kind          NotificationEventKind
	Record        NotificationRecord
	ClearID       string
	ClearEpoch    string
	ClearSequence int64
}

var (
	// ErrInvalidNotificationType reports an unknown notification type.
	ErrInvalidNotificationType = errors.New("invalid notification type")
	// ErrInvalidNotificationStatus reports an unknown notification status.
	ErrInvalidNotificationStatus = errors.New("invalid notification status")
	// ErrInvalidNotificationRecord reports a missing required notification field.
	ErrInvalidNotificationRecord = errors.New("invalid notification record")
)

// Validate checks the required fields and enum values for a stored notification.
func (r NotificationRecord) Validate() error {
	if r.SessionID == "" || r.Title == "" || r.CreatedAt.IsZero() {
		return ErrInvalidNotificationRecord
	}
	if !r.Type.Valid() {
		return ErrInvalidNotificationType
	}
	if !r.Status.Valid() {
		return ErrInvalidNotificationStatus
	}
	return nil
}
