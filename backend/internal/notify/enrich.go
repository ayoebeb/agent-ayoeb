package notify

import (
	"fmt"
	"strings"

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
	if intent.Type != domain.NotificationNeedsInput && intent.Type != domain.NotificationOrchestratorWaiting && rec.PRURL == "" {
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
	case domain.NotificationOrchestratorWaiting:
		return OrchestratorWaitingTitle(intent.ProjectName, string(intent.ProjectID))
	default:
		return "Notification"
	}
}

// OrchestratorWaitingTitle is the title of the notification that says a
// project's orchestrator is waiting for the user: "<project>: orchestrator wacht
// op u". It follows the user's own wording and is the same text on every
// surface (desktop toast, notification center, phone push).
func OrchestratorWaitingTitle(projectName, projectID string) string {
	name := strings.TrimSpace(projectName)
	if name == "" {
		name = strings.TrimSpace(projectID)
	}
	if name == "" {
		name = "project"
	}
	return name + ": orchestrator wacht op u"
}

// orchestratorWaitingFallbackBody is shown when the wait is a pending approval
// or input request, which has no declared one-line question.
const orchestratorWaitingFallbackBody = "De orchestrator wacht op uw antwoord of goedkeuring."

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
	case domain.NotificationOrchestratorWaiting:
		if detail := strings.TrimSpace(intent.Detail); detail != "" {
			return detail
		}
		return orchestratorWaitingFallbackBody
	default:
		return ""
	}
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
