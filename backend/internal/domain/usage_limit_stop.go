package domain

import (
	"fmt"
	"time"
)

// UsageLimitResumeHorizon is how long after a turn stopped on a usage limit AO
// still acts on it by itself. A stop older than this is shown, but AO neither
// notifies about it nor continues it: nobody is waiting on work that went
// quiet days ago, and continuing it would spend the next window on it.
const UsageLimitResumeHorizon = 12 * time.Hour

// Resume states stored for a usage-limit stop.
const (
	UsageLimitStopPending = "pending"
	UsageLimitStopResumed = "resumed"
	UsageLimitStopSkipped = "skipped"
)

// Skip reasons AO records that no automatic resume will happen. The ones also
// named in SessionUsageLimit.ResumeOffReason are shown to people.
const (
	UsageLimitOffDisabled     = "disabled"        // the automatic resume setting is off
	UsageLimitOffUnknownReset = "unknown_reset"   // the provider gave no reset time
	UsageLimitOffCancelled    = "cancelled"       // the person cancelled the automatic resume
	UsageLimitOffRetryArmed   = "retry_scheduled" // the person armed a retry of the turn itself
	UsageLimitOffRepeated     = "repeated"        // it stopped again right after an automatic resume
	UsageLimitOffUnavailable  = "unavailable"     // the session could not take the message
	UsageLimitOffExpired      = "expired"         // the stop is too old for AO to act on
	// UsageLimitSkipStopped and UsageLimitSkipSuperseded are never shown: the
	// person stopped the session, or already moved it on.
	UsageLimitSkipStopped    = "stopped"
	UsageLimitSkipSuperseded = "superseded"
)

// LimitStopTurn is a session's latest turn when it failed, with what AO already
// recorded about it. Whether the failure is a usage limit is decided by
// parsing ErrorMessage; the store cannot tell.
type LimitStopTurn struct {
	SessionID    SessionID
	TurnID       string
	ErrorMessage string
	CompletedAt  *time.Time
	RequestedAt  time.Time
	// StoredResetsAt is the reset time the provider driver reported for the
	// turn; it is preferred over the time parsed from the message.
	StoredResetsAt *time.Time
	// RetryArmed is the person's own opt-in to retry this exact turn at reset.
	RetryArmed bool
	// ResumeState is empty before AO has followed the stop up.
	ResumeState string
	SkipReason  string
	// StopResetsAt, RetryAt and Attempts are the stop row's own values.
	StopResetsAt *time.Time
	RetryAt      *time.Time
	Attempts     int
}

// Limit is the usage limit the turn stopped on, or nil when it failed for
// another reason.
func (t LimitStopTurn) Limit() *ConversationUsageLimit {
	limit := ParseConversationUsageLimit(t.ErrorMessage, t.CompletedAt)
	if limit == nil {
		return nil
	}
	if t.StoredResetsAt != nil {
		reset := t.StoredResetsAt.UTC()
		limit.ResetsAt = &reset
	}
	return limit
}

// StoppedAt is when the turn stopped.
func (t LimitStopTurn) StoppedAt() time.Time {
	if t.CompletedAt != nil {
		return *t.CompletedAt
	}
	return t.RequestedAt
}

// UsageLimitStop is the stored follow-up record of one limit-stopped turn.
type UsageLimitStop struct {
	TurnID      string
	SessionID   SessionID
	ProjectID   ProjectID
	AccountKey  string
	WindowKey   string
	WindowLabel string
	ResetsAt    *time.Time
	DetectedAt  time.Time
	ReportedAt  *time.Time
	ResumeState string
	SkipReason  string
	Attempts    int
	RetryAt     *time.Time
}

// UsageLimitWindowKey identifies one limit window of one account: the account
// and the reset time to the minute, so every session that hit the same window
// shares it even when their reset times differ by seconds. A stop with no reset
// time is keyed by the hour it happened in.
func UsageLimitWindowKey(accountKey string, resetsAt *time.Time, stoppedAt time.Time) string {
	if resetsAt != nil {
		return fmt.Sprintf("%s|%s", accountKey, resetsAt.UTC().Truncate(time.Minute).Format(time.RFC3339))
	}
	return fmt.Sprintf("%s|unknown|%s", accountKey, stoppedAt.UTC().Truncate(time.Hour).Format(time.RFC3339))
}

// BuildSessionUsageLimit is the read-model view of a limit-stopped turn, or nil
// when the turn did not stop on a usage limit.
func BuildSessionUsageLimit(turn LimitStopTurn, now time.Time) *SessionUsageLimit {
	limit := turn.Limit()
	if limit == nil {
		return nil
	}
	window, _ := UsageLimitStopMessage(turn.ErrorMessage)
	out := &SessionUsageLimit{
		TurnID:    turn.TurnID,
		Window:    window,
		Message:   turn.ErrorMessage,
		StoppedAt: turn.StoppedAt(),
		ResetsAt:  limit.ResetsAt,
		Resume:    UsageLimitResumeScheduled,
	}
	off := func(reason string) {
		out.Resume = UsageLimitResumeOff
		out.ResumeOffReason = reason
	}
	switch {
	case turn.ResumeState == UsageLimitStopSkipped && turn.SkipReason != UsageLimitSkipStopped && turn.SkipReason != UsageLimitSkipSuperseded:
		off(turn.SkipReason)
	case turn.RetryArmed:
		off(UsageLimitOffRetryArmed)
	case limit.ResetsAt == nil:
		off(UsageLimitOffUnknownReset)
	case turn.ResumeState == "" && now.Sub(out.StoppedAt) > UsageLimitResumeHorizon:
		off(UsageLimitOffExpired)
	case !limit.ResetsAt.After(now):
		out.Resume = UsageLimitResumeWaiting
	}
	return out
}
