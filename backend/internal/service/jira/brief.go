package jira

import (
	"strings"

	jiraclient "github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// maxBriefSection bounds one pasted section so a huge description cannot
// bury the instructions around it.
const maxBriefSection = 12000

// IssueRef is how a worker session is tied back to a ticket: sessions spawned
// with `ao spawn --issue jira:KEY` carry this id, and the Jira board lists them
// on the ticket.
func IssueRef(key string) string { return "jira:" + key }

// BuildBrief is the task text the orchestrator receives for a ticket: the
// ticket itself, then how to take it on. It is written for an agent, so it is
// plain text with the ticket's own wording kept intact.
func BuildBrief(issue jiraclient.Issue, link string) string {
	var b strings.Builder
	b.WriteString("Start work on Jira ticket " + issue.Key + ": " + clean(issue.Summary) + "\n\n")
	b.WriteString("Link: " + link + "\n")
	writeField(&b, "Type", issue.IssueType)
	writeField(&b, "Priority", issue.Priority)
	writeField(&b, "Status", issue.Status.Name)
	if issue.Assignee != nil {
		writeField(&b, "Assignee", issue.Assignee.DisplayName)
	}
	if len(issue.Labels) > 0 {
		writeField(&b, "Labels", strings.Join(issue.Labels, ", "))
	}
	b.WriteString("\n## Description\n")
	b.WriteString(section(issue.Description, "(The ticket has no description.)"))
	b.WriteString("\n\n## Acceptance criteria\n")
	b.WriteString(section(issue.AcceptanceCriteria, "(The ticket lists no acceptance criteria. Work out what done means from the description, and say so in the pull request.)"))
	b.WriteString("\n\n## How to take this on\n")
	b.WriteString("- This ticket is the task. Plan it, then start a worker for it: `ao spawn --issue " + IssueRef(issue.Key) + " --name \"" + issue.Key + "\" --prompt \"...\"`. The `--issue` value links the worker to the ticket on the Jira board.\n")
	b.WriteString("- Put the ticket key in the branch name and the pull request title so the ticket and the change can be found from each other.\n")
	b.WriteString("- Do not change the ticket's status and do not comment on it in Jira; Ayo does not write to Jira. Report progress here instead.\n")
	return b.String()
}

func writeField(b *strings.Builder, label, value string) {
	if value = clean(value); value != "" {
		b.WriteString(label + ": " + value + "\n")
	}
}

// clean makes a one-line value safe to embed: no control characters, no line breaks.
func clean(s string) string {
	return strings.Join(strings.Fields(domain.SanitizeControlChars(s)), " ")
}

func section(text, empty string) string {
	text = strings.TrimSpace(domain.SanitizeControlChars(text))
	if text == "" {
		return empty
	}
	if r := []rune(text); len(r) > maxBriefSection {
		return string(r[:maxBriefSection]) + "\n\n[Truncated: open the ticket link for the rest.]"
	}
	return text
}
