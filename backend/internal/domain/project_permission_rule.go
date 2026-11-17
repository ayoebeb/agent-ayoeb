package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// ProjectPermissionRuleKind says how wide a remembered permission rule is.
type ProjectPermissionRuleKind string

const (
	// ProjectPermissionRuleExact allows one exact command line.
	ProjectPermissionRuleExact ProjectPermissionRuleKind = "exact"
	// ProjectPermissionRuleCommand allows a command kind, such as `node -e *`.
	ProjectPermissionRuleCommand ProjectPermissionRuleKind = "command"
	// ProjectPermissionRuleTool allows one MCP tool.
	ProjectPermissionRuleTool ProjectPermissionRuleKind = "tool"
	// ProjectPermissionRuleServer allows every tool of one MCP server.
	ProjectPermissionRuleServer ProjectPermissionRuleKind = "server"
)

// ProjectPermissionRule is a tool permission the user chose "Always allow" for,
// remembered for every future session of the project. It lives in the daemon's
// own store; each new Claude Code session workspace gets it written into its
// .claude/settings.local.json, and revoking it removes it from there again.
type ProjectPermissionRule struct {
	// ID is derived from the project and rule, so remembering the same rule twice
	// is one rule.
	ID        string    `json:"id"`
	ProjectID ProjectID `json:"projectId"`
	// Tool is the Claude Code tool name: "Bash", an MCP tool such as
	// "mcp__atlassian__createJiraIssue", or an MCP server "mcp__atlassian".
	Tool string `json:"tool"`
	// Content is the rule's argument, such as the command line for Bash. Empty for
	// an MCP rule.
	Content string                    `json:"content,omitempty"`
	Kind    ProjectPermissionRuleKind `json:"kind"`
	// Label is the short text a person recognizes the rule by.
	Label string `json:"label"`
	// SourceSessionID is the session in which the user chose "Always allow".
	SourceSessionID SessionID `json:"sourceSessionId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// NewProjectPermissionRuleID returns the stable id of a project's rule.
func NewProjectPermissionRuleID(project ProjectID, tool, content string) string {
	sum := sha256.Sum256([]byte(string(project) + "\x00" + tool + "\x00" + content))
	return hex.EncodeToString(sum[:8])
}

// SettingsRule renders the rule the way Claude Code writes it into the
// permissions.allow list of a settings file: `Bash(node -e *)`, or just the tool
// name for an MCP rule. Parentheses and backslashes in the argument are escaped
// the way Claude Code escapes them.
func (r ProjectPermissionRule) SettingsRule() string {
	if r.Content == "" {
		return r.Tool
	}
	escaped := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(r.Content)
	return r.Tool + "(" + escaped + ")"
}
