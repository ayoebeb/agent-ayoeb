package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

type switchMover struct {
	id      domain.SessionID
	account string
	include *bool
	result  sessionsvc.AccountSwitchResult
}

func (m *switchMover) SwitchAccount(_ context.Context, id domain.SessionID, accountID string, _ bool, include *bool) (sessionsvc.AccountSwitchResult, error) {
	m.id, m.account, m.include = id, accountID, include
	return m.result, nil
}

func TestSwitchSessionAccountAnswersWithTheMovedWorkers(t *testing.T) {
	mover := &switchMover{result: sessionsvc.AccountSwitchResult{
		Transition: &domain.SessionInterfaceTransition{ID: "t-orch", SessionID: "orch", TargetAccountID: "acct-b"},
		Workers: []sessionsvc.WorkerAccountMove{
			{SessionID: "w-1", Transition: &domain.SessionInterfaceTransition{ID: "t-1", SessionID: "w-1", TargetAccountID: "acct-b"}},
			{SessionID: "w-2", Skipped: "already_on_account"},
			{SessionID: "w-3", Error: "account is signed out"},
		},
	}}
	r := chi.NewRouter()
	(&AgentAccountsController{Sessions: mover}).Register(r)

	req := httptest.NewRequest(http.MethodPut, "/agents/accounts/sessions/orch", strings.NewReader(`{"accountId":"acct-b","includeWorkers":false}`))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if mover.id != "orch" || mover.account != "acct-b" || mover.include == nil || *mover.include {
		t.Fatalf("mover got id=%s account=%s include=%v, want orch/acct-b/false", mover.id, mover.account, mover.include)
	}
	var out SwitchSessionAgentAccountResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Transition == nil || out.Transition.ID != "t-orch" || out.MovedWorkers != 1 || len(out.Workers) != 3 {
		t.Fatalf("response = %+v, want the move, 1 moved worker and 3 worker rows", out)
	}
	if out.Workers[0].Status != "moving" || out.Workers[1].Status != "skipped" || out.Workers[1].Reason != "already_on_account" ||
		out.Workers[2].Status != "failed" || out.Workers[2].Reason != "account is signed out" {
		t.Fatalf("workers = %+v", out.Workers)
	}
}

// An orchestrator that already sits on the account answers without its own
// transition, only with the workers it brought over.
func TestSwitchSessionAccountWithoutOwnTransition(t *testing.T) {
	mover := &switchMover{result: sessionsvc.AccountSwitchResult{
		Workers: []sessionsvc.WorkerAccountMove{{SessionID: "w-1", Transition: &domain.SessionInterfaceTransition{ID: "t-1", SessionID: "w-1"}}},
	}}
	r := chi.NewRouter()
	(&AgentAccountsController{Sessions: mover}).Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/agents/accounts/sessions/orch", strings.NewReader(`{"accountId":"acct-b"}`)))

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, err = %v, body = %s", rec.Code, err, rec.Body)
	}
	if _, has := out["transition"]; has || out["movedWorkers"] != float64(1) {
		t.Fatalf("response = %v, want movedWorkers 1 and no transition", out)
	}
	if mover.include != nil {
		t.Fatalf("include = %v, want the daemon's default (nil) when the client omits it", *mover.include)
	}
}
