package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Provider IANA zones also work in packaged Windows builds.
)

// ConversationUsageLimit describes a settled provider quota refusal. A missing
// deadline means the provider did not give AO a reset time it can safely use.
type ConversationUsageLimit struct {
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
	AutoResume  bool       `json:"autoResume"`
	ResumeError string     `json:"resumeError,omitempty"`
}

// UsageLimitResume says what AO will do about a session stopped on a usage limit.
type UsageLimitResume string

// The usage-limit resume states a client can show.
const (
	// UsageLimitResumeScheduled means AO will continue the session once the
	// limit window resets.
	UsageLimitResumeScheduled UsageLimitResume = "scheduled"
	// UsageLimitResumeWaiting means the window has reset and the session waits
	// for a free resume slot, so resumes do not all hit the new window at once.
	UsageLimitResumeWaiting UsageLimitResume = "waiting"
	// UsageLimitResumeOff means AO will not continue the session by itself.
	UsageLimitResumeOff UsageLimitResume = "off"
)

// SessionUsageLimit is the read-model fact behind the "usage limit" session
// status: the session's latest turn stopped on the provider's quota. It is
// derived at read time from the failed turn and never stored as a status.
type SessionUsageLimit struct {
	TurnID string `json:"turnId" description:"The turn that stopped on the limit."`
	// Window is the provider's own name for the limit, such as "5-hour" or "weekly".
	Window    string    `json:"window,omitempty" description:"The limit that was hit, in the provider's words, such as 5-hour or weekly."`
	Message   string    `json:"message" description:"The provider's message for the stop."`
	StoppedAt time.Time `json:"stoppedAt" description:"When the turn stopped."`
	// ResetsAt is absent when the provider gave no reset time AO can rely on.
	ResetsAt *time.Time       `json:"resetsAt,omitempty" description:"When the limit window resets; absent when unknown."`
	Resume   UsageLimitResume `json:"resume" enum:"scheduled,waiting,off" description:"What AO will do: continue after the reset (scheduled), wait for a free resume slot (waiting), or leave it alone (off)."`
	// ResumeOffReason explains an off resume.
	ResumeOffReason string `json:"resumeOffReason,omitempty" enum:"disabled,unknown_reset,cancelled,retry_scheduled,repeated,unavailable" description:"Why AO will not continue the session by itself."`
}

// UsageLimitStopMessage reports whether a failed turn's error is a provider
// usage-limit refusal, and returns the provider's window name when it names one.
func UsageLimitStopMessage(message string) (window string, ok bool) {
	if ParseConversationUsageLimit(message, nil) == nil {
		return "", false
	}
	if match := usageLimitWindowName.FindStringSubmatch(strings.ReplaceAll(message, "’", "'")); match != nil {
		return strings.TrimSpace(match[1]), true
	}
	return "", true
}

var usageLimitWindowName = regexp.MustCompile(`(?i)(?:hit|reached) your ([A-Za-z0-9][A-Za-z0-9 .-]*?) limit`)

var claudeUsageLimitPrefixes = []string{
	"You've hit your",
	"You've reached your",
	"You're out of usage credits",
	"Your org is out of usage · add funds to continue",
	"Your org is out of usage · contact your admin",
	"Your seat type doesn't include usage credits",
	"Your seat type doesn't include usage",
	"Your usage allocation has been disabled by your admin",
	"Your group's usage limit is set to $0",
	"Fable 5 requires usage credits",
	"You're out of extra usage",
	"Your seat type doesn't include extra usage",
}

var usageLimitMessage = regexp.MustCompile(`(?i)^(?:usage limit (?:reached|exceeded)\b|(?:session|weekly|daily) usage limit (?:reached|exceeded)\b|the Claude account has no available quota\b)`)
var usageLimitResetClock = regexp.MustCompile(`(?i)\bresets?(?:\s+at)?\s+(?:(tomorrow)\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)\s*\(([^)]+)\)`)
var usageLimitResetISO = regexp.MustCompile(`(?i)\bresets?(?:\s+at)?\s+(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2}))`)

// ParseConversationUsageLimit recognizes only known quota refusals, never
// assistant prose. Clock-only deadlines require the provider's explicit IANA
// zone and are anchored to the failure, so rereading history cannot move them.
func ParseConversationUsageLimit(message string, failedAt *time.Time) *ConversationUsageLimit {
	message = strings.TrimSpace(strings.ReplaceAll(message, "’", "'"))
	if !usageLimitMessage.MatchString(message) && !slices.ContainsFunc(claudeUsageLimitPrefixes, func(prefix string) bool {
		return strings.HasPrefix(message, prefix)
	}) {
		return nil
	}
	limit := &ConversationUsageLimit{}
	if match := usageLimitResetISO.FindStringSubmatch(message); match != nil {
		if reset, err := time.Parse(time.RFC3339, match[1]); err == nil {
			reset = reset.UTC()
			limit.ResetsAt = &reset
		}
		return limit
	}
	match := usageLimitResetClock.FindStringSubmatch(message)
	if match == nil || failedAt == nil {
		return limit
	}
	zoneName := strings.TrimSpace(match[5])
	if zoneName != "UTC" && !strings.Contains(zoneName, "/") {
		return limit
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return limit
	}
	hour, _ := strconv.Atoi(match[2])
	minute, _ := strconv.Atoi(match[3])
	if hour < 1 || hour > 12 || minute > 59 {
		return limit
	}
	hour %= 12
	if strings.EqualFold(match[4], "pm") {
		hour += 12
	}
	local := failedAt.In(zone)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, zone)
	if match[1] == "" && usageLimitClockRepeated(reset) {
		return limit
	}
	if match[1] != "" || !reset.After(*failedAt) {
		reset = reset.AddDate(0, 0, 1)
	}
	// Nonexistent or repeated wall clocks must not become guessed deadlines.
	if reset.Hour() != hour || reset.Minute() != minute || usageLimitClockRepeated(reset) {
		return limit
	}
	reset = reset.UTC()
	limit.ResetsAt = &reset
	return limit
}

func usageLimitClockRepeated(reset time.Time) bool {
	start, end := reset.ZoneBounds()
	_, offset := reset.Zone()
	clock := reset.Format(time.DateTime)
	for i, boundary := range []time.Time{start, end} {
		if boundary.IsZero() {
			continue
		}
		if i == 0 {
			boundary = boundary.Add(-time.Nanosecond)
		}
		_, adjacentOffset := boundary.Zone()
		if adjacentOffset == offset {
			continue
		}
		// Both neighboring offsets can represent this clock during a backward jump.
		other := reset.Add(time.Duration(offset-adjacentOffset) * time.Second)
		if other.Format(time.DateTime) == clock {
			return true
		}
	}
	return false
}
