package permissionmemory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// option builds the raw encoding claude-agent-acp gives an "always" option: one
// policy_rule change per rule, with the lifetime Claude Code stores it for.
func option(kind, scope string, rules ...map[string]any) []byte {
	targets := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		target := map[string]any{"type": "tool", "toolName": rule["tool"]}
		if content, ok := rule["content"]; ok {
			target["matcher"] = map[string]any{"type": "provider_rule", "provider": "claudeCode", "value": content}
		}
		targets = append(targets, target)
	}
	raw, _ := json.Marshal(map[string]any{
		"kind": kind, "optionId": "allow_always", "name": "Always allow",
		"_meta": map[string]any{"permission": map[string]any{"version": 1, "changes": []map[string]any{{
			"type": "policy_rule", "operation": "add", "ruleBehavior": "allow",
			"lifetime": map[string]any{"scope": scope, "storage": "project_local"}, "targets": targets,
		}}}},
	})
	return raw
}

func bash(content string) map[string]any { return map[string]any{"tool": "Bash", "content": content} }

func TestClassifyRemembersOnlyWhatMayBeProjectWide(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		content  string
		wantKind domain.ProjectPermissionRuleKind
		wantOK   bool
	}{
		// Opt-in command kinds for an interpreter, and Claude Code's own class rules.
		{"inline script kind", "Bash", "node -e *", domain.ProjectPermissionRuleCommand, true},
		{"python script kind", "Bash", "python script.py *", domain.ProjectPermissionRuleCommand, true},
		{"claude subcommand rule", "Bash", "npm run *", domain.ProjectPermissionRuleCommand, true},
		{"claude git stage rule", "Bash", "git add *", domain.ProjectPermissionRuleCommand, true},
		{"claude make target rule", "Bash", "make build *", domain.ProjectPermissionRuleCommand, true},
		// An exact command is Claude Code's own narrow rule.
		{"exact command", "Bash", `node -e "console.log(1)"`, domain.ProjectPermissionRuleExact, true},
		{"exact destructive command", "Bash", "rm -rf node_modules", domain.ProjectPermissionRuleExact, true},
		{"mcp tool", "mcp__atlassian__createJiraIssue", "", domain.ProjectPermissionRuleTool, true},
		{"mcp server", "mcp__atlassian", "", domain.ProjectPermissionRuleServer, true},
		// Never a wildcard for a program that deletes, publishes, reaches the network
		// or runs another command line, whoever suggested it.
		{"rm wildcard", "Bash", "rm *", "", false},
		{"rm -rf wildcard", "Bash", "rm -rf *", "", false},
		{"git push wildcard", "Bash", "git push *", "", false},
		{"git no-pager wildcard", "Bash", "git --no-pager *", "", false},
		{"git reset wildcard", "Bash", "git reset *", "", false},
		{"chmod wildcard", "Bash", "chmod -R *", "", false},
		{"curl wildcard", "Bash", "curl -s *", "", false},
		{"npx wildcard", "Bash", "npx --no-install *", "", false},
		{"shell wildcard", "Bash", "bash -c *", "", false},
		{"sudo wildcard", "Bash", "sudo *", "", false},
		{"bare interpreter wildcard", "Bash", "node *", "", false},
		{"bare tool wildcard", "Bash", "*", "", false},
		{"whole Bash tool", "Bash", "", "", false},
		{"inner wildcard", "Bash", "node * -e *", "", false},
		{"backslash", "Bash", `node -e "a\"b"`, "", false},
		{"multi-line command", "Bash", "node -e x\nrm y", "", false},
		{"mcp rule with an argument", "mcp__atlassian__createJiraIssue", "x", "", false},
		{"edit tool", "Edit", "", "", false},
		{"web fetch", "WebFetch", "domain:example.com", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, label, ok := classify(candidate{tool: tc.tool, content: tc.content})
			if ok != tc.wantOK || kind != tc.wantKind {
				t.Fatalf("classify(%q, %q) = (%q, %q, %v), want (%q, _, %v)", tc.tool, tc.content, kind, label, ok, tc.wantKind, tc.wantOK)
			}
			if ok && label == "" {
				t.Fatal("a remembered rule needs a label")
			}
		})
	}
}

func TestCandidatesFromDecisionReadsOnlyPersistentAllowRules(t *testing.T) {
	t.Run("persistent allow rules", func(t *testing.T) {
		got := candidatesFromDecision(option("allow_always", "persistent", bash("node -e *")))
		if want := []candidate{{tool: "Bash", content: "node -e *"}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("candidates = %+v, want %+v", got, want)
		}
	})
	t.Run("an mcp tool has no argument", func(t *testing.T) {
		got := candidatesFromDecision(option("allow_always", "persistent", map[string]any{"tool": "mcp__a__b"}))
		if want := []candidate{{tool: "mcp__a__b"}}; !reflect.DeepEqual(got, want) {
			t.Fatalf("candidates = %+v, want %+v", got, want)
		}
	})
	t.Run("an in-memory rule dies with its process", func(t *testing.T) {
		if got := candidatesFromDecision(option("allow_always", "session", bash("node -e *"))); got != nil {
			t.Fatalf("session-lifetime rule remembered: %+v", got)
		}
	})
	t.Run("only an always option", func(t *testing.T) {
		if got := candidatesFromDecision(option("allow_once", "persistent", bash("node -e *"))); got != nil {
			t.Fatalf("allow_once option remembered: %+v", got)
		}
	})
	t.Run("a mode switch is not a rule", func(t *testing.T) {
		raw := []byte(`{"kind":"allow_always","_meta":{"permission":{"changes":[{"type":"permission_mode","mode":"acceptEdits","lifetime":{"scope":"session"}}]}}}`)
		if got := candidatesFromDecision(raw); got != nil {
			t.Fatalf("mode change remembered: %+v", got)
		}
	})
	t.Run("another provider's matcher", func(t *testing.T) {
		raw := []byte(`{"kind":"allow_always","_meta":{"permission":{"changes":[{"type":"policy_rule","operation":"add","ruleBehavior":"allow","lifetime":{"scope":"persistent"},"targets":[{"type":"tool","toolName":"Bash","matcher":{"provider":"other","value":"x"}}]}]}}}`)
		if got := candidatesFromDecision(raw); got != nil {
			t.Fatalf("foreign matcher remembered: %+v", got)
		}
	})
	t.Run("not json", func(t *testing.T) {
		if got := candidatesFromDecision([]byte("allow_always")); got != nil {
			t.Fatalf("garbage remembered: %+v", got)
		}
	})
}

type fakeStore struct {
	rules map[string]domain.ProjectPermissionRule
}

func (f *fakeStore) UpsertProjectPermissionRule(_ context.Context, rule domain.ProjectPermissionRule) (bool, error) {
	if _, exists := f.rules[rule.ID]; exists {
		return false, nil
	}
	if f.rules == nil {
		f.rules = map[string]domain.ProjectPermissionRule{}
	}
	f.rules[rule.ID] = rule
	return true, nil
}

func (f *fakeStore) ListProjectPermissionRules(_ context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error) {
	var out []domain.ProjectPermissionRule
	for _, rule := range f.rules {
		if rule.ProjectID == project {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteProjectPermissionRule(_ context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, bool, error) {
	rule, ok := f.rules[id]
	if !ok || rule.ProjectID != project {
		return domain.ProjectPermissionRule{}, false, nil
	}
	delete(f.rules, id)
	return rule, true, nil
}

type fakeSessions struct{ records []domain.SessionRecord }

func (f fakeSessions) GetSession(_ context.Context, id domain.SessionID) (domain.SessionRecord, bool, error) {
	for _, record := range f.records {
		if record.ID == id {
			return record, true, nil
		}
	}
	return domain.SessionRecord{}, false, nil
}

func (f fakeSessions) ListSessions(_ context.Context, project domain.ProjectID) ([]domain.SessionRecord, error) {
	var out []domain.SessionRecord
	for _, record := range f.records {
		if record.ProjectID == project {
			out = append(out, record)
		}
	}
	return out, nil
}

func session(id string, harness domain.AgentHarness, workspace string, terminated bool) domain.SessionRecord {
	return domain.SessionRecord{
		ID: domain.SessionID(id), ProjectID: "mer", Harness: harness, IsTerminated: terminated,
		Metadata: domain.SessionMetadata{WorkspacePath: workspace},
	}
}

type edits struct{ added, removed map[string][]string }

func newService(store *fakeStore, sessions fakeSessions) (*Service, *edits) {
	e := &edits{added: map[string][]string{}, removed: map[string][]string{}}
	return New(Options{
		Store: store, Sessions: sessions,
		AddRules: func(path string, rules []string) error { e.added[path] = append(e.added[path], rules...); return nil },
		RemoveRules: func(path string, rules []string) error {
			e.removed[path] = append(e.removed[path], rules...)
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) },
	}), e
}

func TestRememberDecisionStoresOnlyAllowedRulesOnce(t *testing.T) {
	store := &fakeStore{}
	svc, _ := newService(store, fakeSessions{})
	ctx := context.Background()

	// An opt-in command kind and a broad rm wildcard in one choice: only the
	// first may be remembered for the project.
	raw := option("allow_always", "persistent", bash("node -e *"), bash("rm -rf *"))
	if err := svc.RememberDecision(ctx, "mer", "mer-3", raw); err != nil {
		t.Fatalf("RememberDecision: %v", err)
	}
	if err := svc.RememberDecision(ctx, "mer", "mer-4", raw); err != nil {
		t.Fatalf("RememberDecision again: %v", err)
	}
	rules, _ := svc.List(ctx, "mer")
	if len(rules) != 1 {
		t.Fatalf("remembered %+v, want only node -e * once", rules)
	}
	got := rules[0]
	if got.SettingsRule() != "Bash(node -e *)" || got.Kind != domain.ProjectPermissionRuleCommand ||
		got.SourceSessionID != "mer-3" || got.CreatedAt.IsZero() {
		t.Fatalf("remembered rule = %+v, want the first choice's source and time kept", got)
	}

	if err := svc.RememberDecision(ctx, "", "mer-3", raw); err != nil {
		t.Fatalf("a session without a project must be ignored, got %v", err)
	}
	if rules, _ := svc.List(ctx, ""); len(rules) != 0 {
		t.Fatalf("rule stored without a project: %+v", rules)
	}
}

func TestApplyToWorkspaceWritesTheProjectsRulesForClaudeSessionsOnly(t *testing.T) {
	store := &fakeStore{}
	sessions := fakeSessions{records: []domain.SessionRecord{
		session("mer-5", domain.HarnessClaudeCode, "/ws/mer-5", false),
		session("mer-6", domain.HarnessCodex, "/ws/mer-6", false),
	}}
	svc, e := newService(store, sessions)
	ctx := context.Background()
	if err := svc.RememberDecision(ctx, "mer", "mer-3", option("allow_always", "persistent", bash("node -e *"))); err != nil {
		t.Fatal(err)
	}
	if err := svc.RememberDecision(ctx, "mer", "mer-3", option("allow_always", "persistent", map[string]any{"tool": "mcp__atlassian"})); err != nil {
		t.Fatal(err)
	}

	if err := svc.ApplyToWorkspace(ctx, "mer-5", "/ws/mer-5"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	wantRules := map[string]bool{"Bash(node -e *)": true, "mcp__atlassian": true}
	if len(e.added["/ws/mer-5"]) != 2 || !wantRules[e.added["/ws/mer-5"][0]] || !wantRules[e.added["/ws/mer-5"][1]] {
		t.Fatalf("claude workspace got %v, want both remembered rules", e.added["/ws/mer-5"])
	}

	for _, other := range []struct{ id, path string }{{"mer-6", "/ws/mer-6"}, {"missing", "/ws/missing"}, {"mer-5", ""}} {
		if err := svc.ApplyToWorkspace(ctx, domain.SessionID(other.id), other.path); err != nil {
			t.Fatalf("apply %s: %v", other.id, err)
		}
	}
	if len(e.added) != 1 {
		t.Fatalf("rules written to %v, want only the Claude session's workspace", e.added)
	}
}

func TestRevokeForgetsTheRuleAndTakesItOutOfLiveClaudeWorkspaces(t *testing.T) {
	store := &fakeStore{}
	sessions := fakeSessions{records: []domain.SessionRecord{
		session("mer-1", domain.HarnessClaudeCode, "/ws/mer-1", false),
		session("mer-2", domain.HarnessClaudeCode, "/ws/mer-2", true), // terminated: workspace is gone or going
		session("mer-3", domain.HarnessCodex, "/ws/mer-3", false),
		session("mer-4", domain.HarnessClaudeCode, "", false),
	}}
	svc, e := newService(store, sessions)
	ctx := context.Background()
	if err := svc.RememberDecision(ctx, "mer", "mer-1", option("allow_always", "persistent", bash("node -e *"))); err != nil {
		t.Fatal(err)
	}
	rules, _ := svc.List(ctx, "mer")

	revoked, err := svc.Revoke(ctx, "mer", rules[0].ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if revoked.SettingsRule() != "Bash(node -e *)" {
		t.Fatalf("revoked %q", revoked.SettingsRule())
	}
	if left, _ := svc.List(ctx, "mer"); len(left) != 0 {
		t.Fatalf("future sessions still get %+v", left)
	}
	if !reflect.DeepEqual(e.removed, map[string][]string{"/ws/mer-1": {"Bash(node -e *)"}}) {
		t.Fatalf("removed from %v, want only the live Claude session's workspace", e.removed)
	}

	// A new session after the revoke starts without it.
	if err := svc.ApplyToWorkspace(ctx, "mer-1", "/ws/new"); err != nil || len(e.added) != 0 {
		t.Fatalf("a revoked rule was applied again: added=%v err=%v", e.added, err)
	}
	if _, err := svc.Revoke(ctx, "mer", rules[0].ID); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("second revoke err = %v, want ErrRuleNotFound", err)
	}
}
