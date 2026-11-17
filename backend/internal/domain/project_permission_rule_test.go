package domain

import "testing"

func TestProjectPermissionRuleSettingsRule(t *testing.T) {
	tests := []struct {
		name string
		rule ProjectPermissionRule
		want string
	}{
		{"command kind", ProjectPermissionRule{Tool: "Bash", Content: "node -e *"}, "Bash(node -e *)"},
		// The spelling Claude Code itself writes for an exact command with parentheses.
		{"exact command", ProjectPermissionRule{Tool: "Bash", Content: `node -e "console.log(1)"`}, `Bash(node -e "console.log\(1\)")`},
		{"mcp tool", ProjectPermissionRule{Tool: "mcp__atlassian__createJiraIssue"}, "mcp__atlassian__createJiraIssue"},
		{"mcp server", ProjectPermissionRule{Tool: "mcp__atlassian"}, "mcp__atlassian"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.SettingsRule(); got != tc.want {
				t.Fatalf("SettingsRule() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewProjectPermissionRuleIDIsStablePerProjectRule(t *testing.T) {
	a := NewProjectPermissionRuleID("mer", "Bash", "node -e *")
	if a != NewProjectPermissionRuleID("mer", "Bash", "node -e *") {
		t.Fatal("the same rule must keep one id")
	}
	if a == NewProjectPermissionRuleID("other", "Bash", "node -e *") || a == NewProjectPermissionRuleID("mer", "Bash", "node -p *") {
		t.Fatal("a different project or rule must have a different id")
	}
}
