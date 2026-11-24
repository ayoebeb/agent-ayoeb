package httpd

import (
	"fmt"
	"log/slog"

	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

// TestLANAccountRoutesAllowlist pins which /api/v1/agents/accounts routes the
// LAN listener answers: the account list and one session's account (read and
// switch) only. Everything else under the prefix stays a 404 there.
func TestLANAccountRoutesAllowlist(t *testing.T) {
	const base = "/api/v1/agents/accounts"
	for _, tc := range []struct {
		method, path string
		blocked      bool
	}{
		{http.MethodGet, base, false},
		{http.MethodGet, base + "/", false},
		{http.MethodGet, base + "/sessions/orch-1", false},
		{http.MethodPut, base + "/sessions/orch-1", false},

		{http.MethodPost, base, true},
		{http.MethodPut, base, true},
		{http.MethodDelete, base, true},
		{http.MethodDelete, base + "/sessions/orch-1", true},
		{http.MethodPost, base + "/sessions/orch-1", true},
		{http.MethodGet, base + "/sessions", true},
		{http.MethodGet, base + "/sessions/", true},
		{http.MethodGet, base + "/sessions/a/b", true},
		{http.MethodGet, base + "/sessions/..", true},
		{http.MethodPut, base + "/sessions/a/default", true},
		{http.MethodGet, base + "/usage", true},
		{http.MethodGet, base + "/limit-prompts", true},
		{http.MethodPost, base + "/limit-prompts/p1/switch", true},
		{http.MethodPost, base + "/limit-prompts/p1/wait", true},
		{http.MethodPost, base + "/login", true},
		{http.MethodPost, base + "/login/op-1/verify", true},
		{http.MethodPost, base + "/login/op-1/cancel", true},
		{http.MethodPost, base + "/acct-1/login", true},
		{http.MethodPost, base + "/acct-1/default", true},
		{http.MethodPost, base + "/acct-1/reset-credit", true},
		{http.MethodDelete, base + "/acct-1", true},
		{http.MethodPost, base + "/sessions/default", true},
		{http.MethodPut, "/api/v1/agents/codex/accounts/x", true},
	} {
		if got := isLANControlBlocked(tc.method, tc.path); got != tc.blocked {
			t.Errorf("%s %s blocked = %v, want %v", tc.method, tc.path, got, tc.blocked)
		}
	}
}

type remoteAccountsService struct {
	controllers.AgentAccountService
	views []agentaccounts.AccountView
	calls int
}

func (s *remoteAccountsService) List(_ context.Context, refresh bool) ([]agentaccounts.AccountView, error) {
	s.calls++
	if refresh {
		panic("the LAN list must never refresh")
	}
	return s.views, nil
}

func (s *remoteAccountsService) SessionAccounts(context.Context, domain.SessionID) (map[domain.AgentAccountProvider]string, error) {
	s.calls++
	return map[domain.AgentAccountProvider]string{domain.AgentAccountProviderClaude: "acct-1"}, nil
}

type remoteSessions struct {
	controllers.SessionService
	moved int
}

func (s *remoteSessions) SwitchAccount(_ context.Context, id domain.SessionID, _ string, _ bool, _ *bool) (sessionsvc.AccountSwitchResult, error) {
	s.moved++
	return sessionsvc.AccountSwitchResult{Transition: &domain.SessionInterfaceTransition{ID: "t1", SessionID: id}}, nil
}

func TestAccountRoutesOnLANAdmitOnlyAPersonsClient(t *testing.T) {
	key := DeriveCommandRunKey("runtime-token")
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/agents/accounts", ""},
		{http.MethodGet, "/api/v1/agents/accounts/sessions/orch-1", ""},
		{http.MethodPut, "/api/v1/agents/accounts/sessions/orch-1", `{"accountId":"acct-2"}`},
	}
	asLAN := func(r *http.Request) *http.Request {
		return r.WithContext(requestscope.WithLAN(r.Context()))
	}
	withBearer := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), lanBearerContextKey{}, true))
	}
	for _, tc := range []struct {
		name    string
		prepare func(*http.Request) *http.Request
		allowed bool
	}{
		{name: "a LAN request that is not authenticated", prepare: asLAN},
		{name: "a LAN request with a forged renderer Origin but no key or pairing", prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			return asLAN(r)
		}},
		{name: "an agent's CLI naming its session over the LAN", prepare: func(r *http.Request) *http.Request {
			r.Header.Set(caller.Header, "s1")
			return withBearer(asLAN(r))
		}},
		{name: "a paired desktop authenticated on the LAN listener", allowed: true, prepare: func(r *http.Request) *http.Request {
			return withBearer(asLAN(r))
		}},
		// Loopback requests are not gated here: the ao CLI and the local
		// desktop use them, and the LAN listener never serves them.
		{name: "a loopback request", allowed: true, prepare: func(r *http.Request) *http.Request { return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range routes {
				svc := &remoteAccountsService{}
				sessions := &remoteSessions{}
				cfg := config.Config{AllowedOrigins: []string{testRendererOrigin}}
				router := NewRouterWithControl(cfg, nil, nil, APIDeps{AgentAccounts: svc, Sessions: sessions, CommandRunKey: key}, ControlDeps{})
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, tc.prepare(req))
				reached := svc.calls + sessions.moved
				if tc.allowed {
					if rec.Code >= 400 || reached != 1 {
						t.Fatalf("%s %s = %d reached=%d body=%s, want admitted", route.method, route.path, rec.Code, reached, rec.Body)
					}
					continue
				}
				if rec.Code != http.StatusForbidden || reached != 0 || !strings.Contains(rec.Body.String(), "ACCOUNT_USER_CLIENT_REQUIRED") {
					t.Fatalf("%s %s = %d reached=%d body=%s, want refused before the service", route.method, route.path, rec.Code, reached, rec.Body)
				}
			}
		})
	}
}

// TestLANAccountListIsSanitized is the privacy boundary of the list another
// computer reads: no folder path, no full address, no free-text problem and no
// creation time; and a refresh is never honoured.
func TestLANAccountListIsSanitized(t *testing.T) {
	svc := &remoteAccountsService{views: []agentaccounts.AccountView{{
		Account: domain.AgentAccount{
			ID: "acct-1", Provider: domain.AgentAccountProviderClaude, Home: `C:\Users\secret-owner\.ao\accounts\acct-1`,
			Email: "owner.name@example.com", Plan: "max",
		},
		Default: true, SignedIn: true, LoggedIn: true,
		IdentityErr: "token sk-ant-oat01-SECRET expired", LimitsErr: "bearer abc123",
	}}}
	router := NewRouterWithControl(config.Config{}, nil, nil, APIDeps{AgentAccounts: svc, CommandRunKey: "k"}, ControlDeps{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/accounts?refresh=true", nil)
	req = req.WithContext(context.WithValue(requestscope.WithLAN(req.Context()), lanBearerContextKey{}, true))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, secret := range []string{"owner.name", "secret-owner", "sk-ant", "SECRET", "abc123", "createdAt", "home", "problem"} {
		if strings.Contains(body, secret) {
			t.Fatalf("LAN account list leaks %q: %s", secret, body)
		}
	}
	for _, want := range []string{`"id":"acct-1"`, `"provider":"claude"`, `"email":"o***@example.com"`, `"plan":"max"`, `"default":true`, `"signedIn":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("LAN account list lacks %s: %s", want, body)
		}
	}
}

// TestLANListenerServesOnlyTheAllowedAccountRoutes drives the real listener: a
// paired client reaches the account list and a session's account, and nothing
// else under /api/v1/agents/accounts.
func TestLANListenerServesOnlyTheAllowedAccountRoutes(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(inner, st, 0, slog.Default(), nil)
	port, err := startLANForTest(m, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/v1/agents/accounts", http.StatusOK},
		{http.MethodGet, "/api/v1/agents/accounts/sessions/orch-1", http.StatusOK},
		{http.MethodPut, "/api/v1/agents/accounts/sessions/orch-1", http.StatusOK},
		{http.MethodPost, "/api/v1/agents/accounts/login", http.StatusNotFound},
		{http.MethodPost, "/api/v1/agents/accounts/acct-1/default", http.StatusNotFound},
		{http.MethodPost, "/api/v1/agents/accounts/login/op-1/verify", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/agents/accounts/acct-1", http.StatusNotFound},
	} {
		req, _ := http.NewRequest(tc.method, fmt.Sprintf("http://127.0.0.1:%d%s", port, tc.path), nil)
		req.Header.Set("Authorization", "Bearer secret12")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}
