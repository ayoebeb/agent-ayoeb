package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func sampleRule(project, tool, content string, kind domain.ProjectPermissionRuleKind, at time.Time) domain.ProjectPermissionRule {
	return domain.ProjectPermissionRule{
		ID:        domain.NewProjectPermissionRuleID(domain.ProjectID(project), tool, content),
		ProjectID: domain.ProjectID(project), Tool: tool, Content: content, Kind: kind,
		Label: tool + " " + content, SourceSessionID: "mer-3", CreatedAt: at,
	}
}

func TestProjectPermissionRulesRoundTripAndDedupe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	at := time.Now().UTC().Truncate(time.Second)

	first := sampleRule("mer", "Bash", "node -e *", domain.ProjectPermissionRuleCommand, at)
	second := sampleRule("mer", "mcp__atlassian", "", domain.ProjectPermissionRuleServer, at.Add(time.Minute))
	for _, rule := range []domain.ProjectPermissionRule{second, first} {
		added, err := s.UpsertProjectPermissionRule(ctx, rule)
		if err != nil || !added {
			t.Fatalf("remember %s: added=%v err=%v", rule.Tool, added, err)
		}
	}

	// The same rule again keeps the first row: when and from which session it
	// was first chosen is what the list shows.
	again := first
	again.SourceSessionID, again.CreatedAt = "mer-9", at.Add(time.Hour)
	if added, err := s.UpsertProjectPermissionRule(ctx, again); err != nil || added {
		t.Fatalf("remember again: added=%v err=%v, want not added", added, err)
	}

	got, err := s.ListProjectPermissionRules(ctx, "mer")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("list = %+v, want the two rules oldest first", got)
	}
	if got[0].Tool != "Bash" || got[0].Content != "node -e *" || got[0].Kind != domain.ProjectPermissionRuleCommand ||
		got[0].SourceSessionID != "mer-3" || !got[0].CreatedAt.Equal(at) {
		t.Fatalf("round trip lost data: %+v", got[0])
	}
}

func TestProjectPermissionRulesAreScopedToTheirProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "other")
	at := time.Now().UTC().Truncate(time.Second)
	rule := sampleRule("mer", "Bash", "node -e *", domain.ProjectPermissionRuleCommand, at)
	if _, err := s.UpsertProjectPermissionRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	if other, err := s.ListProjectPermissionRules(ctx, "other"); err != nil || len(other) != 0 {
		t.Fatalf("another project sees %+v (err %v)", other, err)
	}
	// Revoking by the right id in the wrong project forgets nothing.
	if _, ok, err := s.DeleteProjectPermissionRule(ctx, "other", rule.ID); err != nil || ok {
		t.Fatalf("delete from another project: ok=%v err=%v", ok, err)
	}
	if kept, _ := s.ListProjectPermissionRules(ctx, "mer"); len(kept) != 1 {
		t.Fatalf("rule lost by a revoke in another project: %+v", kept)
	}
}

func TestDeleteProjectPermissionRuleReturnsTheForgottenRule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	at := time.Now().UTC().Truncate(time.Second)
	rule := sampleRule("mer", "Bash", "node -e *", domain.ProjectPermissionRuleCommand, at)
	if _, err := s.UpsertProjectPermissionRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	forgotten, ok, err := s.DeleteProjectPermissionRule(ctx, "mer", rule.ID)
	if err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if forgotten.SettingsRule() != "Bash(node -e *)" {
		t.Fatalf("returned rule = %q, want the one to take out of workspaces", forgotten.SettingsRule())
	}
	if left, _ := s.ListProjectPermissionRules(ctx, "mer"); len(left) != 0 {
		t.Fatalf("rule still listed: %+v", left)
	}
	if _, ok, err := s.DeleteProjectPermissionRule(ctx, "mer", rule.ID); err != nil || ok {
		t.Fatalf("second delete: ok=%v err=%v, want not found", ok, err)
	}
}
