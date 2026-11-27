package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// DeliverReviewResult sends an AO review pass's result to its worker, once per
// pass. It reports false when the worker could not take the message now
// (terminated, or waiting on the user), so the caller keeps it pending.
//
// A posted review's inline comments already reach the worker from the pull
// request, so only an unposted result's body carries findings.
func (m *Manager) DeliverReviewResult(ctx context.Context, run domain.ReviewRun) (bool, error) {
	if m.guard == nil {
		return false, nil
	}
	outcome, err := m.sendOnce(ctx, run.SessionID, run.PRURL, "aoreview:"+run.PRURL+":"+run.ID, string(run.Verdict), formatReviewResultMessage(run), 0, false)
	if err != nil {
		return false, err
	}
	return outcome == sendOnceAccounted, nil
}

func formatReviewResultMessage(run domain.ReviewRun) string {
	var msg strings.Builder
	verdict := "changes requested"
	if run.Verdict == domain.VerdictApproved {
		verdict = "approved"
	}
	fmt.Fprintf(&msg, "Orchestrator review result: %s.\nPR: %s", verdict, domain.SanitizeControlChars(run.PRURL))
	if run.TargetSHA != "" {
		fmt.Fprintf(&msg, "\nReviewed commit: %s", domain.SanitizeControlChars(run.TargetSHA))
	}
	if run.GithubReviewID != "" {
		fmt.Fprintf(&msg, "\nThe review is on the pull request (review id %s). Its inline comments reach you separately from there.", domain.SanitizeControlChars(run.GithubReviewID))
	} else {
		msg.WriteString("\nThe review is NOT on the pull request, so this message is its only copy. Address every inline finding below.")
	}
	if run.Verdict == domain.VerdictApproved {
		msg.WriteString("\nThis approval is not a GitHub approval and never authorizes merging.")
	}
	// The body is written by an agent that read untrusted PR content and gets
	// pasted into the worker's live pane.
	if body := strings.TrimSpace(domain.SanitizeControlChars(run.Body)); body != "" {
		msg.WriteString("\n\nReviewer's text:\n" + body)
	}
	return msg.String()
}
