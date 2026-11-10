package httpd

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakeSharedBrowserProfiles struct{ calls int }

func (f *fakeSharedBrowserProfiles) ListSharedBrowserProfiles(context.Context) (controllers.SharedBrowserProfilesResponse, error) {
	f.calls++
	return controllers.SharedBrowserProfilesResponse{Profiles: []controllers.SharedBrowserProfile{{
		ID: "0f8fad5b-d9cb-469f-a165-70867728950e", Name: "Brave", SourceBrowser: "Brave", CookieCount: 1, HistoryCount: 1, UpdatedAt: "2026-11-10T10:00:00Z",
	}}}, nil
}

func (f *fakeSharedBrowserProfiles) ExportSharedBrowserProfile(_ context.Context, profileID, _ string) (controllers.SharedBrowserProfileExportResponse, error) {
	f.calls++
	return controllers.SharedBrowserProfileExportResponse{
		Profile: controllers.SharedBrowserProfile{ID: profileID, Name: "Brave", UpdatedAt: "2026-11-10T10:00:00Z"},
		Cookies: []controllers.SharedBrowserCookie{{Name: "sid", Value: "dummy-cookie-value", Domain: "example.test", HostOnly: true, Path: "/"}},
	}, nil
}

// The shared-profile routes return cookie values, so the unauthenticated
// loopback listener must never serve them, and the remote listener only with
// the connection credential.
func TestSharedBrowserProfilesServedOnlyToAuthenticatedLinkedMachines(t *testing.T) {
	const profilePath = "/api/v1/shared-browser-profiles/0f8fad5b-d9cb-469f-a165-70867728950e/export"
	source := &fakeSharedBrowserProfiles{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouterWithControl(config.Config{}, log, nil, APIDeps{SharedBrowserProfiles: source}, ControlDeps{})
	lan := NewMobileLAN(router, "h_A", 0, log, nil)
	lan.SetPasswordHash(mobilebridge.HashPassword("pairing-secret"))

	serve := func(handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	code := func(recorder *httptest.ResponseRecorder) string {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return body.Code
	}

	for _, path := range []string{"/api/v1/shared-browser-profiles", profilePath} {
		// Loopback, even with a credential-looking header: no linked machine
		// reaches the daemon this way, only local processes.
		for _, headers := range []map[string]string{nil, {"Authorization": "Bearer pairing-secret", "X-Forwarded-For": "192.0.2.7"}} {
			got := serve(router, path, headers)
			if got.Code != http.StatusForbidden || code(got) != "REMOTE_LINK_REQUIRED" {
				t.Fatalf("loopback %s %v: status=%d body=%s", path, headers, got.Code, got.Body.String())
			}
		}
		for _, headers := range []map[string]string{nil, {"Authorization": "Bearer wrong-secret"}} {
			got := serve(lan.handler, path, headers)
			if got.Code != http.StatusUnauthorized || code(got) != "BAD_PASSWORD" {
				t.Fatalf("unauthenticated LAN %s: status=%d body=%s", path, got.Code, got.Body.String())
			}
		}
		got := serve(lan.handler, path, map[string]string{"Authorization": "Bearer pairing-secret", "X-AO-Expected-Host-ID": "h_other"})
		if got.Code != http.StatusMisdirectedRequest {
			t.Fatalf("other host LAN %s: status=%d", path, got.Code)
		}
	}
	if source.calls != 0 {
		t.Fatalf("source was read %d times by refused requests", source.calls)
	}

	list := serve(lan.handler, "/api/v1/shared-browser-profiles", map[string]string{"Authorization": "Bearer pairing-secret", "X-AO-Expected-Host-ID": "h_A"})
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"name":"Brave"`) {
		t.Fatalf("authenticated list: status=%d body=%s", list.Code, list.Body.String())
	}
	export := serve(lan.handler, profilePath, map[string]string{"Authorization": "Bearer pairing-secret", "X-AO-Expected-Host-ID": "h_A"})
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), "dummy-cookie-value") {
		t.Fatalf("authenticated export: status=%d body=%s", export.Code, export.Body.String())
	}
	if got := export.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("export Cache-Control = %q, want no-store", got)
	}
	if source.calls != 2 {
		t.Fatalf("source calls = %d, want 2", source.calls)
	}
}
