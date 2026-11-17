package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/permissionmemory"
)

type fakePermissionRules struct {
	rules   []domain.ProjectPermissionRule
	revoked []string
}

func (f *fakePermissionRules) List(_ context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error) {
	var out []domain.ProjectPermissionRule
	for _, rule := range f.rules {
		if rule.ProjectID == project {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (f *fakePermissionRules) Revoke(_ context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, error) {
	for _, rule := range f.rules {
		if rule.ProjectID == project && rule.ID == id {
			f.revoked = append(f.revoked, string(project)+"/"+id)
			return rule, nil
		}
	}
	return domain.ProjectPermissionRule{}, permissionmemory.ErrRuleNotFound
}

func permissionRulesRouter(svc ProjectPermissionRules) http.Handler {
	r := chi.NewRouter()
	(&ProjectPermissionRulesController{Svc: svc}).Register(r)
	return r
}

func TestProjectPermissionRulesListAndRevoke(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	svc := &fakePermissionRules{rules: []domain.ProjectPermissionRule{{
		ID: "r1", ProjectID: "mer", Tool: "Bash", Content: "node -e *", Kind: domain.ProjectPermissionRuleCommand,
		Label: "node -e ...", SourceSessionID: "mer-3", CreatedAt: at,
	}}}
	router := permissionRulesRouter(svc)

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/projects/mer/permission-rules", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", list.Code, list.Body)
	}
	var body struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil || len(body.Rules) != 1 {
		t.Fatalf("list body = %s (%v)", list.Body, err)
	}
	rule := body.Rules[0]
	if rule["id"] != "r1" || rule["kind"] != "command" || rule["label"] != "node -e ..." ||
		rule["tool"] != "Bash" || rule["sourceSessionId"] != "mer-3" || rule["createdAt"] != "2026-11-17T09:00:00Z" {
		t.Fatalf("rule = %v", rule)
	}
	// The command line itself is not part of the API: the label is what a person needs.
	if _, leaked := rule["content"]; leaked {
		t.Fatalf("rule exposes its raw content: %v", rule)
	}

	other := httptest.NewRecorder()
	router.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/projects/other/permission-rules", nil))
	if !strings.Contains(other.Body.String(), `"rules":[]`) {
		t.Fatalf("a project without rules must list an empty array, got %s", other.Body)
	}

	revoke := httptest.NewRecorder()
	router.ServeHTTP(revoke, httptest.NewRequest(http.MethodDelete, "/projects/mer/permission-rules/r1", nil))
	if revoke.Code != http.StatusNoContent || len(svc.revoked) != 1 || svc.revoked[0] != "mer/r1" {
		t.Fatalf("revoke status = %d, revoked = %v", revoke.Code, svc.revoked)
	}

	missing := httptest.NewRecorder()
	router.ServeHTTP(missing, httptest.NewRequest(http.MethodDelete, "/projects/mer/permission-rules/nope", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "PERMISSION_RULE_NOT_FOUND") {
		t.Fatalf("missing rule: status = %d body = %s", missing.Code, missing.Body)
	}
}

func TestProjectPermissionRulesWithoutAServiceAnswer501(t *testing.T) {
	router := permissionRulesRouter(nil)
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/projects/mer/permission-rules", nil),
		httptest.NewRequest(http.MethodDelete, "/projects/mer/permission-rules/r1", nil),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s status = %d, want 501", req.Method, req.URL.Path, rec.Code)
		}
	}
}
