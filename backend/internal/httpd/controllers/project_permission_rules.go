package controllers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/permissionmemory"
)

// ProjectPermissionRules lists and revokes the tool permissions a project's
// users chose "Always allow" for.
type ProjectPermissionRules interface {
	List(ctx context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error)
	Revoke(ctx context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, error)
}

// ProjectPermissionRulesController owns the /projects/{id}/permission-rules
// routes. A nil service keeps the routes registered but answers OpenAPI-backed 501s.
type ProjectPermissionRulesController struct {
	Svc ProjectPermissionRules
}

// ProjectPermissionRuleIDParam identifies one remembered rule of a project.
type ProjectPermissionRuleIDParam struct {
	ID     string `path:"id" description:"Project identifier (registry key)."`
	RuleID string `path:"ruleId" description:"Remembered permission rule identifier."`
}

// ProjectPermissionRuleResponse is one remembered permission rule.
type ProjectPermissionRuleResponse struct {
	ID string `json:"id"`
	// Kind is how wide the rule is: one exact command, a command kind such as
	// `node -e *`, one MCP tool, or every tool of an MCP server.
	Kind string `json:"kind" enum:"exact,command,tool,server"`
	// Label is the short text a person recognizes the rule by.
	Label string `json:"label"`
	// Tool is the agent tool the rule covers, for example Bash.
	Tool string `json:"tool"`
	// SourceSessionID is the session in which "Always allow" was chosen.
	SourceSessionID string    `json:"sourceSessionId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// ListProjectPermissionRulesResponse is a project's remembered rules, oldest first.
type ListProjectPermissionRulesResponse struct {
	Rules []ProjectPermissionRuleResponse `json:"rules"`
}

// Register mounts the routes on the supplied router.
func (c *ProjectPermissionRulesController) Register(r chi.Router) {
	r.Get("/projects/{id}/permission-rules", c.list)
	r.Delete("/projects/{id}/permission-rules/{ruleId}", c.revoke)
}

func (c *ProjectPermissionRulesController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/projects/{id}/permission-rules")
		return
	}
	rules, err := c.Svc.List(r.Context(), domain.ProjectID(chi.URLParam(r, "id")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := ListProjectPermissionRulesResponse{Rules: make([]ProjectPermissionRuleResponse, 0, len(rules))}
	for _, rule := range rules {
		out.Rules = append(out.Rules, ProjectPermissionRuleResponse{
			ID: rule.ID, Kind: string(rule.Kind), Label: rule.Label, Tool: rule.Tool,
			SourceSessionID: string(rule.SourceSessionID), CreatedAt: rule.CreatedAt,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *ProjectPermissionRulesController) revoke(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/projects/{id}/permission-rules/{ruleId}")
		return
	}
	_, err := c.Svc.Revoke(r.Context(), domain.ProjectID(chi.URLParam(r, "id")), chi.URLParam(r, "ruleId"))
	if errors.Is(err, permissionmemory.ErrRuleNotFound) {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "PERMISSION_RULE_NOT_FOUND",
			"The project has no such remembered permission rule", nil)
		return
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
