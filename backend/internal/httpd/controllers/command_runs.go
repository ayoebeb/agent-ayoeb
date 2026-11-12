package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// CommandRunService runs commands the user clicked Run on in Chat.
type CommandRunService interface {
	RunCommand(ctx context.Context, in shelltermsvc.RunCommandInput, onStart func(shelltermsvc.CommandRunStart), onOutput func(string)) (shelltermsvc.CommandRunExit, error)
	StopCommandRun(ctx context.Context, sessionID domain.SessionID, runID string) error
	OpenCommandInTerminal(ctx context.Context, in shelltermsvc.OpenCommandInTerminalInput) (shelltermsvc.ShellTerminal, error)
}

// CommandRunsController owns /sessions/{sessionId}/command-runs: Chat's Run
// button. Every route runs or types an arbitrary command as the user, so each
// sits behind UserClientGate, which admits only a person's client (the desktop
// renderer, or a paired remote client) and never an agent, the ao CLI or an
// automation. Without a gate every request is refused.
type CommandRunsController struct {
	Svc            CommandRunService
	UserClientGate func(http.Handler) http.Handler
}

func (c *CommandRunsController) gate() func(http.Handler) http.Handler {
	if c.UserClientGate != nil {
		return c.UserClientGate
	}
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "COMMAND_RUN_USER_CLIENT_REQUIRED",
				"Commands can only be run from the Orchestrator app", nil)
		})
	}
}

// Register mounts the bounded command-run routes.
func (c *CommandRunsController) Register(r chi.Router) {
	gated := r.With(c.gate())
	gated.Post("/sessions/{sessionId}/command-runs/terminal", c.openInTerminal)
	gated.Delete("/sessions/{sessionId}/command-runs/{runId}", c.stop)
}

// RegisterStream mounts the run route, which streams for as long as the
// command runs and so stays outside the REST timeout.
func (c *CommandRunsController) RegisterStream(r chi.Router) {
	r.With(c.gate()).Post("/sessions/{sessionId}/command-runs", c.run)
}

func commandRunSessionID(w http.ResponseWriter, r *http.Request) (domain.SessionID, bool) {
	id, err := url.PathUnescape(chi.URLParam(r, "sessionId"))
	if err != nil || id == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "SESSION_ID_INVALID", "Invalid session id", nil)
		return "", false
	}
	return domain.SessionID(id), true
}

func decodeRunCommandRequest(w http.ResponseWriter, r *http.Request) (RunCommandRequest, bool) {
	var req RunCommandRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return req, false
	}
	return req, true
}

// run streams one command as server-sent events: started, output chunks, exit.
// A command that cannot start answers a plain error envelope instead.
// Closing the request stops the command.
func (c *CommandRunsController) run(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/command-runs")
		return
	}
	sessionID, ok := commandRunSessionID(w, r)
	if !ok {
		return
	}
	req, ok := decodeRunCommandRequest(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED", "Streaming is not supported by this server", nil)
		return
	}
	var mu sync.Mutex
	streaming := false
	send := func(event CommandRunStreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		if !streaming {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			streaming = true
		}
		data, err := json.Marshal(event)
		if err != nil {
			return
		}
		// A client that left cancels the request context, which stops the
		// command; a failed write needs nothing more.
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
	}
	exit, err := c.Svc.RunCommand(r.Context(), shelltermsvc.RunCommandInput{
		SessionID: sessionID,
		Shell:     req.Shell,
		Command:   req.Command,
	}, func(start shelltermsvc.CommandRunStart) {
		send(CommandRunStreamEvent{Started: &CommandRunStarted{
			RunID:      start.RunID,
			Shell:      start.Shell,
			ShellPath:  start.ShellPath,
			WorkingDir: start.WorkingDir,
			StartedAt:  start.StartedAt,
		}})
	}, func(text string) {
		send(CommandRunStreamEvent{Output: &text})
	})
	if err != nil {
		mu.Lock()
		started := streaming
		mu.Unlock()
		if !started {
			envelope.WriteError(w, r, err)
			return
		}
		apiErr := envelope.StreamError(r, err)
		send(CommandRunStreamEvent{Error: &apiErr})
		return
	}
	send(CommandRunStreamEvent{Exit: &CommandRunExited{
		ExitCode:     exit.ExitCode,
		DurationMs:   exit.DurationMs,
		Stopped:      exit.Stopped,
		TimedOut:     exit.TimedOut,
		Truncated:    exit.Truncated,
		DroppedBytes: exit.DroppedBytes,
	}})
}

func (c *CommandRunsController) stop(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/sessions/{sessionId}/command-runs/{runId}")
		return
	}
	sessionID, ok := commandRunSessionID(w, r)
	if !ok {
		return
	}
	runID, err := url.PathUnescape(chi.URLParam(r, "runId"))
	if err != nil || runID == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "COMMAND_RUN_ID_INVALID", "Invalid command run id", nil)
		return
	}
	if err := c.Svc.StopCommandRun(r.Context(), sessionID, runID); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *CommandRunsController) openInTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/command-runs/terminal")
		return
	}
	sessionID, ok := commandRunSessionID(w, r)
	if !ok {
		return
	}
	req, ok := decodeRunCommandRequest(w, r)
	if !ok {
		return
	}
	terminal, err := c.Svc.OpenCommandInTerminal(r.Context(), shelltermsvc.OpenCommandInTerminalInput{
		SessionID: sessionID,
		Shell:     req.Shell,
		Command:   req.Command,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, ShellTerminalEnvelope{ShellTerminal: shellTerminalResponse(terminal)})
}
