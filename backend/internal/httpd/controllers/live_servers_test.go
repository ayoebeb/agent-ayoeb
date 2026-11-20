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
	"github.com/aoagents/agent-orchestrator/backend/internal/service/liveserver"
)

type fakeLiveServers struct {
	listed    domain.ProjectID
	stopped   string
	restarted string
}

func (f *fakeLiveServers) List(_ context.Context, p domain.ProjectID) ([]liveserver.Server, error) {
	f.listed = p
	return []liveserver.Server{{
		Key: "managed:s1", Source: liveserver.SourceManaged, Name: "vite", Port: 5173, URL: "http://127.0.0.1:5173/",
		SessionID: "s1", StartedAt: time.Unix(100, 0).UTC(), State: "ready", Health: liveserver.HealthUp,
		TerminalID: "term-1", OutputAvailable: true, CanRestart: true,
	}}, nil
}
func (f *fakeLiveServers) Stop(_ context.Context, _ domain.ProjectID, key string) error {
	f.stopped = key
	return nil
}
func (f *fakeLiveServers) Restart(_ context.Context, _ domain.ProjectID, key string) (liveserver.Server, error) {
	f.restarted = key
	return liveserver.Server{Key: key, Source: liveserver.SourceManaged, Name: "vite", State: "ready", Health: liveserver.HealthUp}, nil
}

func TestLiveServersRoutes(t *testing.T) {
	svc := &fakeLiveServers{}
	r := chi.NewRouter()
	(&LiveServersController{Svc: svc}).Register(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/p1/live-servers", nil))
	if rec.Code != http.StatusOK || svc.listed != "p1" {
		t.Fatalf("list = %d listed=%q", rec.Code, svc.listed)
	}
	var list ListLiveServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Servers) != 1 || list.Servers[0].Health != "up" || list.Servers[0].TerminalID != "term-1" || list.Servers[0].Logs == nil {
		t.Fatalf("servers = %+v", list.Servers)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/p1/live-servers/stop", strings.NewReader(`{"key":"managed:s1"}`)))
	if rec.Code != http.StatusNoContent || svc.stopped != "managed:s1" {
		t.Fatalf("stop = %d stopped=%q", rec.Code, svc.stopped)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/p1/live-servers/restart", strings.NewReader(`{"key":"managed:s1"}`)))
	if rec.Code != http.StatusOK || svc.restarted != "managed:s1" {
		t.Fatalf("restart = %d restarted=%q", rec.Code, svc.restarted)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/p1/live-servers/stop", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stop without key = %d, want 400", rec.Code)
	}
}
