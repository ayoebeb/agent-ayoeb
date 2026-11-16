package cli

import "time"

// sessionUsageLimitDTO mirrors the daemon's usage-limit read model: present
// while the session's latest turn is stopped on the provider's quota.
type sessionUsageLimitDTO struct {
	TurnID          string     `json:"turnId"`
	Window          string     `json:"window,omitempty"`
	Message         string     `json:"message"`
	StoppedAt       time.Time  `json:"stoppedAt"`
	ResetsAt        *time.Time `json:"resetsAt,omitempty"`
	Resume          string     `json:"resume"`
	ResumeOffReason string     `json:"resumeOffReason,omitempty"`
}

// activityLabel is what the ACTIVITY column shows instead of a bare "idle".
func (u *sessionUsageLimitDTO) activityLabel() string {
	if u.ResetsAt == nil {
		return "usage limit, reset unknown"
	}
	return "usage limit, resets " + formatLimitReset(*u.ResetsAt, time.Now())
}

// detailLine is the `ao session get` description of the limit and what AO does about it.
func (u *sessionUsageLimitDTO) detailLine() string {
	limit := "usage limit"
	if u.Window != "" {
		limit = u.Window + " limit"
	}
	line := "stopped on the " + limit
	if u.ResetsAt != nil {
		line += ", resets " + u.ResetsAt.Local().Format(time.RFC3339)
	} else {
		line += ", reset time unknown"
	}
	switch u.Resume {
	case "scheduled":
		line += "; AO continues it automatically after the reset"
	case "waiting":
		line += "; the limit has reset, AO continues it when a resume slot is free"
	default:
		line += "; AO will not continue it automatically"
		if reason := usageLimitOffReason(u.ResumeOffReason); reason != "" {
			line += " (" + reason + ")"
		}
	}
	return line
}

func usageLimitOffReason(reason string) string {
	switch reason {
	case "disabled":
		return "automatic resume is off"
	case "unknown_reset":
		return "the provider gave no reset time"
	case "cancelled":
		return "you cancelled it"
	case "retry_scheduled":
		return "a retry of the turn is scheduled"
	case "repeated":
		return "it stopped again right after AO continued it"
	case "unavailable":
		return "the session could not take the message"
	case "expired":
		return "the stop is too old"
	default:
		return ""
	}
}

// formatLimitReset renders a reset time in the local zone: just the clock when
// it is within a day, with the weekday otherwise.
func formatLimitReset(reset, now time.Time) string {
	local := reset.Local()
	if reset.Sub(now) < 20*time.Hour {
		return local.Format("15:04")
	}
	return local.Format("Mon 15:04")
}

// sessionActivityColumn is the ACTIVITY cell: a limit-stopped session reads as
// that, not as idle.
func sessionActivityColumn(sess sessionDTO) string {
	if sess.UsageLimit != nil {
		return sess.UsageLimit.activityLabel()
	}
	return emptyDash(sess.Activity.State)
}
