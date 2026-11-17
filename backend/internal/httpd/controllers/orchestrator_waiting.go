package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/orchestratorwait"
)

// OrchestratorWaitingService is the controller-facing contract for the
// "orchestrator is waiting for the user" state.
type OrchestratorWaitingService interface {
	Declare(ctx context.Context, id domain.SessionID, question string) (orchestratorwait.Outcome, error)
	Clear(ctx context.Context, id domain.SessionID) (bool, error)
	List(ctx context.Context) ([]orchestratorwait.Waiting, error)
}

// OrchestratorWaitingController owns the routes an orchestrator uses to tell the
// user it is waiting, and the list clients read to mark it.
type OrchestratorWaitingController struct {
	Svc OrchestratorWaitingService
}

// Register mounts the routes.
func (c *OrchestratorWaitingController) Register(r chi.Router) {
	r.Get("/orchestrator-waiting", c.list)
	r.Post("/sessions/{sessionId}/waiting", c.declare)
	r.Delete("/sessions/{sessionId}/waiting", c.clear)
}

func (c *OrchestratorWaitingController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/orchestrator-waiting")
		return
	}
	waiting, err := c.Svc.List(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]OrchestratorWaitingResponse, 0, len(waiting))
	for _, item := range waiting {
		out = append(out, OrchestratorWaitingResponse{
			NotificationID: item.NotificationID,
			SessionID:      string(item.SessionID),
			ProjectID:      string(item.ProjectID),
			Title:          item.Title,
			Question:       item.Question,
			Since:          item.Since,
			Reminded:       item.Reminded,
		})
	}
	envelope.WriteJSON(w, http.StatusOK, ListOrchestratorWaitingResponse{Waiting: out})
}

func (c *OrchestratorWaitingController) declare(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/waiting")
		return
	}
	var req DeclareOrchestratorWaitingRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	outcome, err := c.Svc.Declare(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")), req.Question)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, DeclareOrchestratorWaitingResponse{Outcome: string(outcome)})
}

func (c *OrchestratorWaitingController) clear(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/sessions/{sessionId}/waiting")
		return
	}
	cleared, err := c.Svc.Clear(r.Context(), domain.SessionID(chi.URLParam(r, "sessionId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ClearOrchestratorWaitingResponse{Cleared: cleared})
}
