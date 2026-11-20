package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
)

// fakeLimitsAccounts serves one signed-in Claude account and records whether a
// refresh was asked for, which the read-only route must never do.
type fakeLimitsAccounts struct {
	controllers.AgentAccountService
	refreshed bool
}

func (f *fakeLimitsAccounts) List(_ context.Context, refresh bool) ([]agentaccounts.AccountView, error) {
	f.refreshed = f.refreshed || refresh
	return []agentaccounts.AccountView{{
		Account:  domain.AgentAccount{ID: "claude-main", Provider: domain.AgentAccountProviderClaude, Email: "me@example.com"},
		Default:  true,
		SignedIn: true,
		Limits: &domain.AgentAccountLimits{
			FetchedAt: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
			Windows:   []domain.AgentAccountLimitWindow{{ID: "five_hour", Label: "5-hour", UsedPercent: 37}},
		},
	}}, nil
}

func (f *fakeLimitsAccounts) SessionAccounts(_ context.Context, id domain.SessionID) (map[domain.AgentAccountProvider]string, error) {
	return map[domain.AgentAccountProvider]string{domain.AgentAccountProviderClaude: "claude-for-" + string(id)}, nil
}

// Another computer's desktop reads limits through the LAN listener, which 404s
// everything under /api/v1/agents/accounts, so the route lives under /usage.
func TestAccountLimitsAreReadOnlyAndReachableOnTheLAN(t *testing.T) {
	if httpd.IsLANControlBlockedPathForTest("/api/v1/usage/account-limits") {
		t.Fatal("/api/v1/usage/account-limits must stay reachable on the LAN listener")
	}
	svc := &fakeLimitsAccounts{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{AgentAccounts: svc}, httpd.ControlDeps{}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/v1/usage/account-limits?sessionId=s-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body controllers.AccountLimitsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Accounts) != 1 || body.Accounts[0].Limits == nil || body.Accounts[0].Limits.Windows[0].UsedPercent != 37 {
		t.Fatalf("accounts = %+v, want the Claude account with its 5-hour window", body.Accounts)
	}
	if body.Session == nil || body.Session.Claude != "claude-for-s-1" {
		t.Fatalf("session = %+v, want the session's own Claude account", body.Session)
	}
	if svc.refreshed {
		t.Fatal("the read-only route must reuse the daemon's reading, not refresh it")
	}
}

func TestAccountLimitsWithoutSessionOmitsIt(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{AgentAccounts: &fakeLimitsAccounts{}}, httpd.ControlDeps{}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/v1/usage/account-limits")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var body controllers.AccountLimitsResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Session != nil {
		t.Fatalf("session = %+v, want none when no sessionId was given", body.Session)
	}
}
