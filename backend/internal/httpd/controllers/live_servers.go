package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/liveserver"
)

// LiveServerService is the controller-facing view of a project's running dev
// servers.
type LiveServerService interface {
	List(ctx context.Context, project domain.ProjectID) ([]liveserver.Server, error)
	Stop(ctx context.Context, project domain.ProjectID, key string) error
	Restart(ctx context.Context, project domain.ProjectID, key string) (liveserver.Server, error)
}

// LiveServersController owns the /projects/{projectId}/live-servers routes.
type LiveServersController struct {
	Svc LiveServerService
	// UserClientGate admits only a person's client (the desktop renderer or a
	// paired remote client). Without a gate every request is refused.
	UserClientGate func(http.Handler) http.Handler
}

func (c *LiveServersController) gate() func(http.Handler) http.Handler {
	if c.UserClientGate != nil {
		return c.UserClientGate
	}
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "LIVE_SERVER_USER_CLIENT_REQUIRED",
				"Running servers can only be managed from the Orchestrator app", nil)
		})
	}
}

// LiveServerProjectIDParam is the {projectId} path parameter of the live
// server routes.
type LiveServerProjectIDParam struct {
	ProjectID string `path:"projectId" description:"Project whose running dev servers are listed."`
}

// LiveServerResponse is one running dev server.
type LiveServerResponse struct {
	Key             string     `json:"key" description:"Stable identifier within the project: managed:<sessionId> or detected:<port>."`
	Source          string     `json:"source" enum:"managed,detected" description:"managed servers were started by AO from .ao/launch.json; detected ones by a worker on its own."`
	Name            string     `json:"name"`
	Command         string     `json:"command,omitempty"`
	Port            int        `json:"port"`
	URL             string     `json:"url"`
	SessionID       string     `json:"sessionId"`
	SessionName     string     `json:"sessionName,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	State           string     `json:"state" description:"Lifecycle state: starting, ready, stopping or failed."`
	Health          string     `json:"health" enum:"up,down" description:"up when the server answered an HTTP request within a few seconds."`
	HealthMs        int64      `json:"healthMs"`
	HealthNote      string     `json:"healthNote,omitempty"`
	TerminalID      string     `json:"terminalId,omitempty" description:"PTY handle the terminal mux attaches to; present only for managed servers."`
	WorkingDir      string     `json:"workingDir,omitempty"`
	PID             int        `json:"pid,omitempty"`
	Logs            []string   `json:"logs"`
	OutputAvailable bool       `json:"outputAvailable"`
	CanRestart      bool       `json:"canRestart"`
	Error           string     `json:"error,omitempty"`
}

// ListLiveServersResponse lists a project's running dev servers.
type ListLiveServersResponse struct {
	Servers []LiveServerResponse `json:"servers"`
}

// LiveServerActionRequest names the server an action applies to.
type LiveServerActionRequest struct {
	Key string `json:"key" description:"The key of a server from the list."`
}

// LiveServerEnvelope wraps one live server.
type LiveServerEnvelope struct {
	Server LiveServerResponse `json:"server"`
}

// Register mounts the live server routes.
func (c *LiveServersController) Register(r chi.Router) {
	gated := r.With(c.gate())
	gated.Get("/projects/{projectId}/live-servers", c.list)
	gated.Post("/projects/{projectId}/live-servers/stop", c.stop)
	gated.Post("/projects/{projectId}/live-servers/restart", c.restart)
}

func liveServerResponse(s liveserver.Server) LiveServerResponse {
	logs := s.Logs
	if logs == nil {
		logs = []string{}
	}
	out := LiveServerResponse{
		Key: s.Key, Source: string(s.Source), Name: s.Name, Command: s.Command, Port: s.Port, URL: s.URL,
		SessionID: string(s.SessionID), SessionName: s.SessionName, State: s.State,
		Health: string(s.Health), HealthMs: s.HealthMs, HealthNote: s.HealthNote,
		TerminalID: s.TerminalID, WorkingDir: s.WorkingDir, PID: s.PID, Logs: logs,
		OutputAvailable: s.OutputAvailable, CanRestart: s.CanRestart, Error: s.Error,
	}
	if !s.StartedAt.IsZero() {
		started := s.StartedAt
		out.StartedAt = &started
	}
	return out
}

func (c *LiveServersController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/projects/{projectId}/live-servers")
		return
	}
	servers, err := c.Svc.List(r.Context(), domain.ProjectID(chi.URLParam(r, "projectId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]LiveServerResponse, 0, len(servers))
	for _, s := range servers {
		out = append(out, liveServerResponse(s))
	}
	envelope.WriteJSON(w, http.StatusOK, ListLiveServersResponse{Servers: out})
}

func (c *LiveServersController) decodeKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in LiveServerActionRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Key) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "A server key is required", nil)
		return "", false
	}
	return strings.TrimSpace(in.Key), true
}

func (c *LiveServersController) stop(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/projects/{projectId}/live-servers/stop")
		return
	}
	key, ok := c.decodeKey(w, r)
	if !ok {
		return
	}
	if err := c.Svc.Stop(r.Context(), domain.ProjectID(chi.URLParam(r, "projectId")), key); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *LiveServersController) restart(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/projects/{projectId}/live-servers/restart")
		return
	}
	key, ok := c.decodeKey(w, r)
	if !ok {
		return
	}
	server, err := c.Svc.Restart(r.Context(), domain.ProjectID(chi.URLParam(r, "projectId")), key)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, LiveServerEnvelope{Server: liveServerResponse(server)})
}
