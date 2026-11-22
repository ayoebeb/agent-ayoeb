package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/liveserver"
)

type recordingLiveServers struct{ calls int }

func (f *recordingLiveServers) List(context.Context, domain.ProjectID) ([]liveserver.Server, error) {
	f.calls++
	return nil, nil
}

func (f *recordingLiveServers) Stop(context.Context, domain.ProjectID, string) error {
	f.calls++
	return nil
}

func (f *recordingLiveServers) Restart(context.Context, domain.ProjectID, string) (liveserver.Server, error) {
	f.calls++
	return liveserver.Server{}, nil
}

func TestLiveServerRoutesAdmitOnlyAPersonsClient(t *testing.T) {
	key := DeriveCommandRunKey("runtime-token")
	body := `{"key":"managed:other-session"}`
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/projects/p/live-servers", ""},
		{http.MethodPost, "/api/v1/projects/p/live-servers/stop", body},
		{http.MethodPost, "/api/v1/projects/p/live-servers/restart", body},
	}
	for _, tc := range []struct {
		name    string
		key     string
		prepare func(*http.Request) *http.Request
		allowed bool
	}{
		{name: "native loopback client without Origin or key", key: key, prepare: func(r *http.Request) *http.Request { return r }},
		{name: "forged renderer Origin without the key", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			return r
		}},
		{name: "an agent's CLI naming its session", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			r.Header.Set(CommandRunKeyHeader, key)
			r.Header.Set(caller.Header, "s1")
			return r
		}},
		{name: "a daemon with no desktop key refuses everyone local", key: "", prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			return r
		}},
		{name: "the desktop renderer with its key", key: key, allowed: true, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			r.Header.Set(CommandRunKeyHeader, key)
			return r
		}},
		{name: "a paired remote client authenticated on the LAN listener", key: key, allowed: true, prepare: func(r *http.Request) *http.Request {
			return r.WithContext(context.WithValue(r.Context(), lanBearerContextKey{}, true))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range routes {
				svc := &recordingLiveServers{}
				cfg := config.Config{AllowedOrigins: []string{testRendererOrigin}}
				router := NewRouterWithControl(cfg, nil, nil, APIDeps{LiveServers: svc, CommandRunKey: tc.key}, ControlDeps{})
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, tc.prepare(req))
				if tc.allowed {
					if rec.Code >= 400 || svc.calls != 1 {
						t.Fatalf("%s %s = %d calls=%d, want admitted", route.method, route.path, rec.Code, svc.calls)
					}
					continue
				}
				if rec.Code != http.StatusForbidden || svc.calls != 0 || !strings.Contains(rec.Body.String(), "LIVE_SERVER_USER_CLIENT_REQUIRED") {
					t.Fatalf("%s %s = %d calls=%d body=%s, want refused before the service", route.method, route.path, rec.Code, svc.calls, rec.Body.String())
				}
			}
		})
	}
}
