package controllers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/usagelimit"
)

func (c *AgentAccountsController) listLimitPrompts(w http.ResponseWriter, r *http.Request) {
	if c.Prompts == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/accounts/limit-prompts")
		return
	}
	prompts, err := c.Prompts.ListPrompts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := UsageLimitPromptsResponse{Prompts: make([]UsageLimitPromptResponse, 0, len(prompts))}
	for _, p := range prompts {
		item := UsageLimitPromptResponse{
			ID: p.ID, Provider: p.Provider, WindowLabel: p.WindowLabel, AccountID: p.AccountID,
			AccountEmail: p.AccountEmail, ResetsAt: p.ResetsAt, CreatedAt: p.CreatedAt,
			Sessions: make([]UsageLimitPromptSession, 0, len(p.Sessions)),
			Options:  make([]UsageLimitPromptOption, 0, len(p.Options)),
		}
		for _, s := range p.Sessions {
			item.Sessions = append(item.Sessions, UsageLimitPromptSession{
				SessionID: s.ID, DisplayName: s.DisplayName, Kind: string(s.Kind), ProjectID: s.ProjectID,
			})
		}
		for _, o := range p.Options {
			item.Options = append(item.Options, UsageLimitPromptOption{
				AccountID: o.AccountID, Email: o.Email, Plan: o.Plan, Device: o.Device,
				FiveHourUsedPercent: o.FiveHourUsedPercent, FiveHourResetsAt: o.FiveHourResetsAt,
				WeeklyUsedPercent: o.WeeklyUsedPercent, WeeklyResetsAt: o.WeeklyResetsAt, Full: o.Full,
			})
		}
		out.Prompts = append(out.Prompts, item)
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *AgentAccountsController) switchLimitPrompt(w http.ResponseWriter, r *http.Request) {
	if c.Prompts == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/accounts/limit-prompts/{promptId}/switch")
		return
	}
	var in SwitchUsageLimitPromptRequest
	if err := decodeJSONStrict(r, &in); err != nil || strings.TrimSpace(in.AccountID) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "accountId is required", nil)
		return
	}
	result, err := c.Prompts.Switch(r.Context(), strings.TrimSpace(chi.URLParam(r, "promptId")), strings.TrimSpace(in.AccountID))
	if err != nil {
		writeUsageLimitPromptError(w, r, err)
		return
	}
	out := SwitchUsageLimitPromptResponse{OK: true, Moved: result.Moved, Failed: make([]UsageLimitPromptFailure, 0, len(result.Failed))}
	if out.Moved == nil {
		out.Moved = []domain.SessionID{}
	}
	for _, f := range result.Failed {
		out.Failed = append(out.Failed, UsageLimitPromptFailure{SessionID: f.SessionID, Reason: f.Reason})
	}
	envelope.WriteJSON(w, http.StatusAccepted, out)
}

func (c *AgentAccountsController) waitLimitPrompt(w http.ResponseWriter, r *http.Request) {
	if c.Prompts == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/accounts/limit-prompts/{promptId}/wait")
		return
	}
	if err := c.Prompts.Wait(r.Context(), strings.TrimSpace(chi.URLParam(r, "promptId"))); err != nil {
		writeUsageLimitPromptError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, WaitUsageLimitPromptResponse{OK: true})
}

func writeUsageLimitPromptError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, usagelimit.ErrPromptNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "USAGE_LIMIT_PROMPT_NOT_FOUND", "Unknown usage-limit prompt", nil)
	case errors.Is(err, usagelimit.ErrPromptAnswered):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "USAGE_LIMIT_PROMPT_ANSWERED", "This question was already answered", nil)
	case errors.Is(err, usagelimit.ErrPromptTargetInvalid):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "USAGE_LIMIT_TARGET_INVALID", "That account cannot take these sessions: it is signed out, of another provider, or at its own limit", nil)
	case errors.Is(err, usagelimit.ErrPromptSwitchFailed):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "USAGE_LIMIT_SWITCH_FAILED", err.Error(), nil)
	default:
		writeAgentAccountError(w, r, err)
	}
}
