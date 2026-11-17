package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func readSettings(t *testing.T, workspace string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(claudeSettingsPath(workspace))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, data)
	}
	return settings
}

func allowList(t *testing.T, workspace string) []any {
	t.Helper()
	permissions, _ := readSettings(t, workspace)["permissions"].(map[string]any)
	allow, _ := permissions["allow"].([]any)
	return allow
}

func TestAddPermissionRulesKeepsHooksAndOtherSettings(t *testing.T) {
	workspace := t.TempDir()
	// What a spawn leaves behind: AO's hooks, plus a rule the user chose in this
	// workspace and a setting AO knows nothing about.
	if err := (&Plugin{}).GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{WorkspacePath: workspace}); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	settings := readSettings(t, workspace)
	settings["permissions"] = map[string]any{"allow": []any{"Bash(git add *)"}, "defaultMode": "acceptEdits"}
	settings["model"] = "opus"
	encoded, _ := json.Marshal(settings)
	if err := os.WriteFile(claudeSettingsPath(workspace), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AddPermissionRules(workspace, []string{"Bash(node -e *)", "mcp__atlassian", "Bash(git add *)"}); err != nil {
		t.Fatalf("AddPermissionRules: %v", err)
	}
	// Twice is the same as once, so a restore does not pile rules up.
	if err := AddPermissionRules(workspace, []string{"Bash(node -e *)"}); err != nil {
		t.Fatalf("AddPermissionRules again: %v", err)
	}

	got := readSettings(t, workspace)
	want := []any{"Bash(git add *)", "Bash(node -e *)", "mcp__atlassian"}
	if allow := allowList(t, workspace); !reflect.DeepEqual(allow, want) {
		t.Fatalf("permissions.allow = %v, want %v", allow, want)
	}
	if got["model"] != "opus" || got["permissions"].(map[string]any)["defaultMode"] != "acceptEdits" {
		t.Fatalf("other settings were not kept: %v", got)
	}
	if _, ok := got["hooks"].(map[string]any)["SessionStart"]; !ok {
		t.Fatalf("AO's hooks were dropped: %v", got["hooks"])
	}
}

func TestAddPermissionRulesCreatesTheFileForAFreshWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := AddPermissionRules(workspace, []string{"Bash(node -e *)"}); err != nil {
		t.Fatalf("AddPermissionRules: %v", err)
	}
	if allow := allowList(t, workspace); !reflect.DeepEqual(allow, []any{"Bash(node -e *)"}) {
		t.Fatalf("permissions.allow = %v", allow)
	}
	// The file is kept out of the repository, like AO's hooks file.
	ignore, err := os.ReadFile(filepath.Join(workspace, ".claude", ".gitignore"))
	if err != nil || len(ignore) == 0 {
		t.Fatalf("settings file is not ignored by git: %v", err)
	}
}

func TestRemovePermissionRulesTakesOutOnlyThoseRules(t *testing.T) {
	workspace := t.TempDir()
	if err := AddPermissionRules(workspace, []string{"Bash(node -e *)", "mcp__atlassian", "Bash(git add *)"}); err != nil {
		t.Fatal(err)
	}
	if err := RemovePermissionRules(workspace, []string{"Bash(node -e *)", "Bash(never added)"}); err != nil {
		t.Fatalf("RemovePermissionRules: %v", err)
	}
	if allow := allowList(t, workspace); !reflect.DeepEqual(allow, []any{"mcp__atlassian", "Bash(git add *)"}) {
		t.Fatalf("permissions.allow = %v, want the other two kept in order", allow)
	}

	// Removing the last rule leaves no empty permissions block behind.
	if err := RemovePermissionRules(workspace, []string{"mcp__atlassian", "Bash(git add *)"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSettings(t, workspace)["permissions"]; ok {
		t.Fatalf("an empty permissions block was left: %v", readSettings(t, workspace))
	}
}

func TestRemovePermissionRulesIgnoresAWorkspaceWithoutSettings(t *testing.T) {
	workspace := t.TempDir()
	if err := RemovePermissionRules(workspace, []string{"Bash(node -e *)"}); err != nil {
		t.Fatalf("RemovePermissionRules: %v", err)
	}
	if _, err := os.Stat(claudeSettingsPath(workspace)); !os.IsNotExist(err) {
		t.Fatalf("a revoke created a settings file: %v", err)
	}
}

func TestPermissionRulesRefuseUnreadableSettingsInsteadOfOverwritingThem(t *testing.T) {
	workspace := t.TempDir()
	path := claudeSettingsPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddPermissionRules(workspace, []string{"Bash(node -e *)"}); err == nil {
		t.Fatal("AddPermissionRules rewrote a settings file it could not parse")
	}
	if data, _ := os.ReadFile(path); string(data) != `{"permissions": [` {
		t.Fatalf("settings file was modified: %q", data)
	}
	if err := AddPermissionRules("  ", []string{"x"}); err == nil {
		t.Fatal("a blank workspace path must be refused")
	}
}
