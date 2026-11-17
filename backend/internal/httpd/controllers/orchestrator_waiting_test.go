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
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/orchestratorwait"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
)

type fakeWaitingService struct {
	declared  []string
	cleared   []domain.SessionID
	outcome   orchestratorwait.Outcome
	err       error
	list      []orchestratorwait.Waiting
	wasOpened bool
}

func (f *fakeWaitingService) Declare(_ context.Context, id domain.SessionID, question string) (orchestratorwait.Outcome, error) {
	f.declared = append(f.declared, string(id)+"|"+question)
	return f.outcome, f.err
}

func (f *fakeWaitingService) Clear(_ context.Context, id domain.SessionID) (bool, error) {
	f.cleared = append(f.cleared, id)
	return f.wasOpened, f.err
}

func (f *fakeWaitingService) List(context.Context) ([]orchestratorwait.Waiting, error) {
	return f.list, f.err
}

func waitingRouter(svc OrchestratorWaitingService) http.Handler {
	r := chi.NewRouter()
	(&OrchestratorWaitingController{Svc: svc}).Register(r)
	return r
}

func TestOrchestratorWaitingDeclareRoute(t *testing.T) {
	svc := &fakeWaitingService{outcome: orchestratorwait.OutcomeNotified}
	w := httptest.NewRecorder()
	waitingRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sessions/mer-orchestrator-1/waiting",
		strings.NewReader(`{"question":"Ship it?"}`)))
	var got DeclareOrchestratorWaitingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || got.Outcome != "notified" {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	if len(svc.declared) != 1 || svc.declared[0] != "mer-orchestrator-1|Ship it?" {
		t.Fatalf("declared = %v", svc.declared)
	}
}

func TestOrchestratorWaitingDeclareRefusalsKeepTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{"worker", apierr.Invalid("NOT_AN_ORCHESTRATOR", "worker", nil), http.StatusBadRequest},
		{"unknown", apierr.NotFound("SESSION_NOT_FOUND", "unknown"), http.StatusNotFound},
		{"ended", apierr.Conflict("SESSION_TERMINATED", "ended", nil), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeWaitingService{err: tc.err}
			w := httptest.NewRecorder()
			waitingRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sessions/x/waiting", strings.NewReader(`{"question":"q"}`)))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	// A body that is not JSON never reaches the service.
	svc := &fakeWaitingService{}
	w := httptest.NewRecorder()
	waitingRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sessions/x/waiting", strings.NewReader(`{`)))
	if w.Code != http.StatusBadRequest || len(svc.declared) != 0 {
		t.Fatalf("status = %d declared = %v", w.Code, svc.declared)
	}
}

func TestOrchestratorWaitingClearRoute(t *testing.T) {
	svc := &fakeWaitingService{wasOpened: true}
	w := httptest.NewRecorder()
	waitingRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/sessions/mer-orchestrator-1/waiting", nil))
	var got ClearOrchestratorWaitingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || !got.Cleared {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	if len(svc.cleared) != 1 || svc.cleared[0] != "mer-orchestrator-1" {
		t.Fatalf("cleared = %v", svc.cleared)
	}
}

func TestOrchestratorWaitingListRoute(t *testing.T) {
	since := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	svc := &fakeWaitingService{list: []orchestratorwait.Waiting{{
		NotificationID: "ntf_1", SessionID: "mer-orchestrator-1", ProjectID: "mer",
		Title: "Mercury: orchestrator is waiting for you", Question: "Ship it?", Since: since, Reminded: true,
	}}}
	w := httptest.NewRecorder()
	waitingRouter(svc).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/orchestrator-waiting", nil))
	var got ListOrchestratorWaitingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || len(got.Waiting) != 1 {
		t.Fatalf("status=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
	item := got.Waiting[0]
	if item.NotificationID != "ntf_1" || item.SessionID != "mer-orchestrator-1" || item.ProjectID != "mer" ||
		item.Question != "Ship it?" || !item.Since.Equal(since) || !item.Reminded {
		t.Fatalf("item = %+v", item)
	}

	// Nothing waiting is an empty list, not null.
	empty := httptest.NewRecorder()
	waitingRouter(&fakeWaitingService{}).ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/orchestrator-waiting", nil))
	if !strings.Contains(empty.Body.String(), `"waiting":[]`) {
		t.Fatalf("empty body = %s", empty.Body.String())
	}
}

func TestOrchestratorWaitingRoutesWithoutAServiceAreNotImplemented(t *testing.T) {
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/orchestrator-waiting", nil),
		httptest.NewRequest(http.MethodPost, "/sessions/x/waiting", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/sessions/x/waiting", nil),
	} {
		w := httptest.NewRecorder()
		waitingRouter(nil).ServeHTTP(w, req)
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s status = %d", req.Method, req.URL.Path, w.Code)
		}
	}
}

type orchestratorWaitingSettingsService struct {
	hibernationSettingsService
	enabled bool
	minutes int
}

func (s *orchestratorWaitingSettingsService) SetOrchestratorWaiting(_ context.Context, enabled bool, minutes int) (settingssvc.Snapshot, error) {
	s.enabled, s.minutes = enabled, minutes
	s.calls++
	return settingssvc.Snapshot{OrchestratorWaitingEnabled: enabled, OrchestratorWaitingRenotifyMinutes: minutes}, nil
}

func TestSettingsOrchestratorWaitingRequiresBothFields(t *testing.T) {
	svc := &orchestratorWaitingSettingsService{}
	c := &SettingsController{Svc: svc}
	for _, body := range []string{`{}`, `{"enabled":true}`, `{"renotifyMinutes":5}`, `{"enabled":null,"renotifyMinutes":5}`, `{"enabled":true,"renotifyMinutes":"5"}`} {
		w := httptest.NewRecorder()
		c.setOrchestratorWaiting(w, httptest.NewRequest(http.MethodPatch, "/api/v1/settings/orchestrator-waiting", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || svc.calls != 0 {
			t.Fatalf("body=%s status=%d calls=%d", body, w.Code, svc.calls)
		}
	}
	w := httptest.NewRecorder()
	c.setOrchestratorWaiting(w, httptest.NewRequest(http.MethodPatch, "/api/v1/settings/orchestrator-waiting",
		strings.NewReader(`{"enabled":false,"renotifyMinutes":25}`)))
	var got SettingsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK ||
		got.OrchestratorWaitingEnabled || got.OrchestratorWaitingRenotifyMinutes != 25 || svc.enabled || svc.minutes != 25 {
		t.Fatalf("status=%d response=%+v err=%v", w.Code, got, err)
	}
}

// The SSE stream names the reminder so a client can tell it from a new row.
func TestNotificationStreamNamesTheReminder(t *testing.T) {
	w := httptest.NewRecorder()
	err := writeNotificationSSE(w, w, domain.NotificationEvent{
		Kind:   domain.NotificationReminder,
		Record: domain.NotificationRecord{ID: "ntf_1", SessionID: "mer-orchestrator-1", ProjectID: "mer", Type: domain.NotificationOrchestratorWaiting, Title: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "event: notification_reminder\n") || !strings.Contains(body, `"type":"orchestrator_waiting"`) ||
		!strings.Contains(body, `"kind":"session"`) {
		t.Fatalf("sse = %q", body)
	}
}
