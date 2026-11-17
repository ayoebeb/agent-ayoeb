package domain

import (
	"errors"
	"strings"
	"time"
)

// AutopilotState is where one Autopilot task run stands.
type AutopilotState string

const (
	// AutopilotRunning means AO keeps the agent on the task: at the end of every
	// turn that did not finish it, AO sends the task again.
	AutopilotRunning AutopilotState = "running"
	// AutopilotPausedUsageLimit means the account hit a usage limit. The loop
	// resumes by itself after the reset.
	AutopilotPausedUsageLimit AutopilotState = "paused_usage_limit"
	// AutopilotParked means the run stopped itself and waits for the user: it ran
	// out of iterations or stopped making progress. The user continues or stops it.
	AutopilotParked AutopilotState = "parked"
	// AutopilotDone means the agent declared the task done and every
	// verification gate passed.
	AutopilotDone AutopilotState = "done"
	// AutopilotStopped means the user (or the session ending) ended the run.
	AutopilotStopped AutopilotState = "stopped"
)

// Valid reports whether s is a known state.
func (s AutopilotState) Valid() bool {
	switch s {
	case AutopilotRunning, AutopilotPausedUsageLimit, AutopilotParked, AutopilotDone, AutopilotStopped:
		return true
	default:
		return false
	}
}

// Open reports whether the run still owns its session: it is running, paused or
// parked. At most one run per session is open.
func (s AutopilotState) Open() bool {
	return s == AutopilotRunning || s == AutopilotPausedUsageLimit || s == AutopilotParked
}

// AutopilotEndReason says why a run left the running state.
type AutopilotEndReason string

const (
	AutopilotEndNone          AutopilotEndReason = ""
	AutopilotEndUser          AutopilotEndReason = "user"
	AutopilotEndEmergencyStop AutopilotEndReason = "emergency_stop"
	AutopilotEndSessionEnded  AutopilotEndReason = "session_ended"
	AutopilotEndGoalDone      AutopilotEndReason = "goal_done"
	AutopilotEndMaxIterations AutopilotEndReason = "max_iterations"
	AutopilotEndStuck         AutopilotEndReason = "stuck"
	// AutopilotEndNeedsInput is a run parked because a session sat on a prompt
	// Autopilot cannot answer (a terminal prompt, a form, a request the policy has
	// no decision for), which would otherwise stall it silently.
	AutopilotEndNeedsInput AutopilotEndReason = "needs_input"
)

// AutopilotRun is one task the user gave a session to finish on its own.
type AutopilotRun struct {
	ID        string
	SessionID SessionID
	ProjectID ProjectID
	// Task is the user's text, re-sent at the end of every turn.
	Task string
	// Criteria is the "done when ..." list the agent drafted from the task in its
	// first turn and recorded with `ao autopilot criteria`.
	Criteria string
	State    AutopilotState
	// EndReason is why the run left running; empty while it runs.
	EndReason AutopilotEndReason
	// Iteration counts the completed turns the loop has judged.
	Iteration     int
	MaxIterations int
	// Stagnant counts consecutive iterations that changed neither the workspace
	// nor the recorded test result.
	Stagnant    int
	Fingerprint string
	// LastGap is what was still failing at the last judgement.
	LastGap string
	// Evidence is the agent's account of the tests it ran, from its last done claim.
	Evidence string
	// CIGap is the reason the agent gave for CI that cannot start. It is a recorded
	// gap in the return report, never a pass.
	CIGap              string
	PermissionsAllowed int
	PermissionsDenied  int
	StartedAt          time.Time
	UpdatedAt          time.Time
	LastNudgeAt        time.Time
	EndedAt            time.Time
}

// AutopilotEventKind is one kind of line in the run log.
type AutopilotEventKind string

const (
	AutopilotEventStarted          AutopilotEventKind = "started"
	AutopilotEventIteration        AutopilotEventKind = "iteration"
	AutopilotEventDecision         AutopilotEventKind = "decision"
	AutopilotEventParked           AutopilotEventKind = "parked"
	AutopilotEventGateFailed       AutopilotEventKind = "gate_failed"
	AutopilotEventDone             AutopilotEventKind = "done"
	AutopilotEventStopped          AutopilotEventKind = "stopped"
	AutopilotEventRunParked        AutopilotEventKind = "run_parked"
	AutopilotEventPaused           AutopilotEventKind = "paused"
	AutopilotEventResumed          AutopilotEventKind = "resumed"
	AutopilotEventPermissionDenied AutopilotEventKind = "permission_denied"
	AutopilotEventWaitDiverted     AutopilotEventKind = "wait_diverted"
)

// AutopilotEvent is one line of the log the user reads.
type AutopilotEvent struct {
	ID        string
	RunID     string
	SessionID SessionID
	Kind      AutopilotEventKind
	Iteration int
	Summary   string
	// Detail is the longer text (the reason for a decision, the failing gate).
	Detail    string
	CreatedAt time.Time
	// ResolvedAt is when the user marked a parked item handled; zero while open.
	ResolvedAt time.Time
}

// HardLimit names something Autopilot never does. When the agent meets one it
// parks that single item and carries on with everything else.
type HardLimit string

const (
	HardLimitMoney       HardLimit = "money"
	HardLimitCI          HardLimit = "ci"
	HardLimitMainPush    HardLimit = "main_push"
	HardLimitDataDelete  HardLimit = "data_delete"
	HardLimitAdmin       HardLimit = "admin"
	HardLimitMessaging   HardLimit = "messaging"
	HardLimitCredentials HardLimit = "credentials"
	HardLimitOtherPCs    HardLimit = "other_pcs"
)

// HardLimits lists every hard limit in the order the prompt states them.
var HardLimits = []HardLimit{
	HardLimitMoney, HardLimitCI, HardLimitMainPush, HardLimitDataDelete,
	HardLimitAdmin, HardLimitMessaging, HardLimitCredentials, HardLimitOtherPCs,
}

// Valid reports whether l is a known hard limit.
func (l HardLimit) Valid() bool {
	for _, known := range HardLimits {
		if l == known {
			return true
		}
	}
	return false
}

// Description is the plain-words statement of the limit, used in the agent's
// instructions and the log.
func (l HardLimit) Description() string {
	switch l {
	case HardLimitMoney:
		return "spend money or change billing or payment settings"
	case HardLimitCI:
		return "disable, skip or weaken CI, or push to main without review"
	case HardLimitMainPush:
		return "push to the default branch (main) or merge without an approved AO review of the exact head and green checks"
	case HardLimitDataDelete:
		return "delete data outside the session's own workspace"
	case HardLimitAdmin:
		return "use administrator or elevated rights"
	case HardLimitMessaging:
		return "message people outside AO (email, chat, issue or PR comments to others)"
	case HardLimitCredentials:
		return "change logins, tokens, passwords or credentials"
	case HardLimitOtherPCs:
		return "act on another computer's credentials"
	default:
		return string(l)
	}
}

// Autopilot input bounds.
const (
	// AutopilotMaxTaskRunes caps the task text sent back at every iteration.
	AutopilotMaxTaskRunes = 8000
	// AutopilotMaxFieldRunes caps one recorded sentence (a gap, evidence, a reason).
	AutopilotMaxFieldRunes = 2000
	// AutopilotDefaultMaxIterations is used until the user changes the setting.
	AutopilotDefaultMaxIterations = 30
	// AutopilotMaxIterationsCeiling bounds the setting and a per-run override.
	AutopilotMaxIterationsCeiling = 500
	// AutopilotStuckIterations is how many consecutive iterations without any
	// change park a run.
	AutopilotStuckIterations = 3
)

// ErrAutopilotTaskRequired is returned when a run starts without a task.
var ErrAutopilotTaskRequired = errors.New("autopilot: a task is required")

// CleanAutopilotText trims s and drops control characters other than newlines
// and tabs, then cuts it to max runes. The text ends up in an agent prompt, a
// notification and the log, so it must not smuggle terminal control sequences.
func CleanAutopilotText(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			// Folded into the newline that follows.
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if max > 0 {
		if runes := []rune(out); len(runes) > max {
			out = strings.TrimSpace(string(runes[:max-1])) + "…"
		}
	}
	return out
}
