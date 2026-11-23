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
	"github.com/aoagents/agent-orchestrator/backend/internal/service/usagelimit"
)

type fakePrompts struct {
	switched  []string
	waited    []string
	switchErr error
	waitErr   error
}

func (f *fakePrompts) ListPrompts(context.Context) ([]usagelimit.Prompt, error) {
	reset := time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)
	five := 10.0
	return []usagelimit.Prompt{{
		ID: "p1", Provider: "claude", WindowLabel: "5-hour", AccountID: "acct-1", AccountEmail: "limited@example.com", ResetsAt: &reset,
		Sessions: []usagelimit.PromptSession{{ID: "orch", DisplayName: "Main", Kind: domain.KindOrchestrator, ProjectID: "proj"}},
		Options:  []usagelimit.PromptOption{{AccountID: "acct-2", Email: "two@example.com", FiveHourUsedPercent: &five}},
	}}, nil
}

func (f *fakePrompts) Switch(_ context.Context, id, accountID string) (usagelimit.SwitchResult, error) {
	f.switched = append(f.switched, id+"->"+accountID)
	return usagelimit.SwitchResult{Moved: []domain.SessionID{"orch"}}, f.switchErr
}

func (f *fakePrompts) Wait(_ context.Context, id string) error {
	f.waited = append(f.waited, id)
	return f.waitErr
}

func promptRouter(p *fakePrompts) chi.Router {
	r := chi.NewRouter()
	(&AgentAccountsController{Prompts: p}).Register(r)
	return r
}

func TestLimitPromptsAreListedWithSessionsAndOptions(t *testing.T) {
	rec := httptest.NewRecorder()
	promptRouter(&fakePrompts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents/accounts/limit-prompts", nil))
	var out UsageLimitPromptsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status = %d, err = %v, body = %s", rec.Code, err, rec.Body)
	}
	if len(out.Prompts) != 1 || out.Prompts[0].AccountEmail != "limited@example.com" || out.Prompts[0].Sessions[0].Kind != "orchestrator" ||
		out.Prompts[0].Options[0].AccountID != "acct-2" || out.Prompts[0].Options[0].FiveHourUsedPercent == nil {
		t.Fatalf("prompts = %+v", out.Prompts)
	}
}

func TestSwitchAndWaitAnswerThePrompt(t *testing.T) {
	p := &fakePrompts{}
	r := promptRouter(p)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agents/accounts/limit-prompts/p1/switch", strings.NewReader(`{"accountId":"acct-2"}`)))
	var out SwitchUsageLimitPromptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusAccepted || len(out.Moved) != 1 || len(p.switched) != 1 || p.switched[0] != "p1->acct-2" {
		t.Fatalf("switch: status = %d, err = %v, body = %s, calls = %v", rec.Code, err, rec.Body, p.switched)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agents/accounts/limit-prompts/p1/wait", nil))
	if rec.Code != http.StatusOK || len(p.waited) != 1 {
		t.Fatalf("wait: status = %d, calls = %v", rec.Code, p.waited)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agents/accounts/limit-prompts/p1/switch", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("switch without an account: status = %d, want 400", rec.Code)
	}
}

func TestAnsweredPromptIsAConflictAndUnknownIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{usagelimit.ErrPromptAnswered, http.StatusConflict}, {usagelimit.ErrPromptNotFound, http.StatusNotFound}, {usagelimit.ErrPromptTargetInvalid, http.StatusBadRequest}} {
		rec := httptest.NewRecorder()
		promptRouter(&fakePrompts{waitErr: tc.err}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/agents/accounts/limit-prompts/p1/wait", nil))
		if rec.Code != tc.want {
			t.Fatalf("%v: status = %d, want %d", tc.err, rec.Code, tc.want)
		}
	}
}
