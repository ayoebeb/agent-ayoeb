package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func enrich(intent Intent) (domain.NotificationRecord, error) {
	rec := domain.NotificationRecord{
		SessionID: intent.SessionID,
		ProjectID: intent.ProjectID,
		PRURL:     strings.TrimSpace(intent.PRURL),
		Type:      intent.Type,
		Status:    domain.NotificationUnread,
		CreatedAt: intent.CreatedAt,
		SourceKey: strings.TrimSpace(intent.SourceKey),
	}
	if !intent.Type.Valid() {
		return domain.NotificationRecord{}, domain.ErrInvalidNotificationType
	}
	if intent.Type != domain.NotificationNeedsInput && intent.Type != domain.NotificationOrchestratorWaiting && intent.Type != domain.NotificationUsageLimit && rec.PRURL == "" {
		return domain.NotificationRecord{}, domain.ErrInvalidNotificationRecord
	}
	rec.Title = titleForIntent(intent)
	rec.Body = bodyForIntent(intent)
	if err := rec.Validate(); err != nil {
		return domain.NotificationRecord{}, err
	}
	return rec, nil
}

func titleForIntent(intent Intent) string {
	switch intent.Type {
	case domain.NotificationNeedsInput:
		return fmt.Sprintf("%s needs your input", sessionLabel(intent))
	case domain.NotificationReadyToMerge:
		if title := strings.TrimSpace(intent.PRTitle); title != "" {
			if label := prLabel(intent); label != "PR" {
				return fmt.Sprintf("%s · %s", title, label)
			}
			return title
		}
		return fmt.Sprintf("%s is ready to merge", prLabel(intent))
	case domain.NotificationPRMerged:
		return fmt.Sprintf("%s merged", prLabel(intent))
	case domain.NotificationPRClosedUnmerged:
		return fmt.Sprintf("%s closed", prLabel(intent))
	case domain.NotificationReviewCompleted:
		return fmt.Sprintf("Review complete for %s", prLabel(intent))
	case domain.NotificationReviewChangesRequested:
		return fmt.Sprintf("Review found changes for %s", prLabel(intent))
	case domain.NotificationUsageLimit:
		return usageLimitTitle(intent)
	case domain.NotificationOrchestratorWaiting:
		return OrchestratorWaitingTitle(intent.ProjectName, string(intent.ProjectID))
	default:
		return "Notification"
	}
}

// OrchestratorWaitingTitleSuffix follows the project name in the title of the
// notification that says a project's orchestrator is waiting for the user. The
// daemon has no UI language, so it writes English like every other notification
// it stores (phone push, CLI, API). The desktop app shows these in the user's own
// language instead, and finds the project name by this suffix. The renderer keeps
// a copy (ORCHESTRATOR_WAITING_TITLE_SUFFIX) and a test fails if they drift.
const OrchestratorWaitingTitleSuffix = ": orchestrator is waiting for you"

// OrchestratorWaitingFallbackBody is the body when the wait is a pending approval
// or input request, which has no declared one-line question. The desktop app
// replaces it with the user's language the same way (see the suffix above).
const OrchestratorWaitingFallbackBody = "The orchestrator is waiting for your answer or approval."

// OrchestratorWaitingTitle is the title of that notification:
// "<project>: orchestrator is waiting for you".
func OrchestratorWaitingTitle(projectName, projectID string) string {
	name := strings.TrimSpace(projectName)
	if name == "" {
		name = strings.TrimSpace(projectID)
	}
	if name == "" {
		name = "project"
	}
	return name + OrchestratorWaitingTitleSuffix
}

func bodyForIntent(intent Intent) string {
	switch intent.Type {
	case domain.NotificationNeedsInput:
		return "Your agent is waiting on you to continue."
	case domain.NotificationReadyToMerge:
		if session := sessionLabel(intent); session != "session" {
			return fmt.Sprintf("PR from session %s is ready to merge. CI passed with no blocking review feedback.", session)
		}
		return "CI passed with no blocking review feedback."
	case domain.NotificationPRMerged:
		title := strings.TrimSpace(intent.PRTitle)
		if target := strings.TrimSpace(intent.PRTargetBranch); title != "" && target != "" {
			return fmt.Sprintf("%s is now on %s.", title, target)
		}
		if title != "" {
			return fmt.Sprintf("%s was merged.", title)
		}
		return "The pull request was merged."
	case domain.NotificationPRClosedUnmerged:
		if title := strings.TrimSpace(intent.PRTitle); title != "" {
			return fmt.Sprintf("%s was closed without merging. Reopen it if this wasn't intended.", title)
		}
		return "Closed without merging. Reopen it if this wasn't intended."
	case domain.NotificationReviewCompleted:
		return "Orchestrator finished reviewing this pull request and approved it."
	case domain.NotificationReviewChangesRequested:
		return "Orchestrator finished reviewing this pull request and found changes to address."
	case domain.NotificationUsageLimit:
		return usageLimitBody(intent)
	case domain.NotificationOrchestratorWaiting:
		if detail := strings.TrimSpace(intent.Detail); detail != "" {
			return detail
		}
		return OrchestratorWaitingFallbackBody
	default:
		return ""
	}
}

func usageLimitTitle(intent Intent) string {
	provider := strings.TrimSpace(intent.UsageLimitProvider)
	if provider == "" {
		provider = "Agent"
	}
	if window := strings.TrimSpace(intent.UsageLimitWindow); window != "" {
		return fmt.Sprintf("%s %s limit reached", provider, window)
	}
	return fmt.Sprintf("%s usage limit reached", provider)
}

func usageLimitBody(intent Intent) string {
	var out strings.Builder
	out.WriteString("Sessions on this account stopped. ")
	switch {
	case intent.UsageLimitResetsAt.IsZero():
		out.WriteString("The provider gave no reset time. ")
		out.WriteString("Continue them yourself once the limit is lifted.")
	case intent.UsageLimitAutoResume:
		fmt.Fprintf(&out, "It resets at %s; AO continues them then, a few at a time.", FormatUsageLimitReset(intent.UsageLimitResetsAt, intent.CreatedAt))
	default:
		fmt.Fprintf(&out, "It resets at %s. Automatic resume is off, so continue them yourself.", FormatUsageLimitReset(intent.UsageLimitResetsAt, intent.CreatedAt))
	}
	return out.String()
}

// FormatUsageLimitReset renders a reset time in the machine's zone: a clock
// time when it is within a day of now, otherwise with the weekday.
func FormatUsageLimitReset(reset, now time.Time) string {
	local := reset.Local()
	if now.IsZero() || reset.Sub(now) < 20*time.Hour {
		return local.Format("15:04")
	}
	return local.Format("Mon 15:04")
}

func sessionLabel(intent Intent) string {
	if v := strings.TrimSpace(intent.SessionDisplayName); v != "" {
		return v
	}
	if intent.SessionID != "" {
		return string(intent.SessionID)
	}
	return "session"
}

func prLabel(intent Intent) string {
	if intent.PRNumber > 0 {
		return fmt.Sprintf("PR #%d", intent.PRNumber)
	}
	if title := strings.TrimSpace(intent.PRTitle); title != "" {
		return "PR " + title
	}
	return "PR"
}
