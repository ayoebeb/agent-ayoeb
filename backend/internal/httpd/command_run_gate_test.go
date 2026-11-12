package httpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// The desktop derives the same key (frontend/src/main/command-run-key.test.ts
// asserts this exact vector); change both together.
func TestDeriveCommandRunKeyVector(t *testing.T) {
	if got := DeriveCommandRunKey("test-runtime-token"); got != "qrOL0IYLaW6w7XNg5pRjypUW6XPECINUGsUeljoIa0w" {
		t.Fatalf("DeriveCommandRunKey = %q", got)
	}
	if DeriveCommandRunKey("") != "" {
		t.Fatal("an empty token must not yield a usable key")
	}
}

type recordingCommandRuns struct {
	runs  int
	stops int
	opens int
}

func (f *recordingCommandRuns) RunCommand(_ context.Context, in shelltermsvc.RunCommandInput, onStart func(shelltermsvc.CommandRunStart), onOutput func(string)) (shelltermsvc.CommandRunExit, error) {
	f.runs++
	onStart(shelltermsvc.CommandRunStart{RunID: "cmdrun-1", Shell: "PowerShell", WorkingDir: "/ws"})
	onOutput("ran " + in.Command)
	return shelltermsvc.CommandRunExit{ExitCode: 0, DurationMs: 5}, nil
}

func (f *recordingCommandRuns) StopCommandRun(context.Context, domain.SessionID, string) error {
	f.stops++
	return nil
}

func (f *recordingCommandRuns) OpenCommandInTerminal(_ context.Context, in shelltermsvc.OpenCommandInTerminalInput) (shelltermsvc.ShellTerminal, error) {
	f.opens++
	return shelltermsvc.ShellTerminal{HandleID: "shellterm-1", SessionID: in.SessionID}, nil
}

const testRendererOrigin = "app://renderer"

func commandRunRouter(t *testing.T, svc *recordingCommandRuns, key string) http.Handler {
	t.Helper()
	cfg := config.Config{AllowedOrigins: []string{testRendererOrigin}}
	return NewRouterWithControl(cfg, nil, nil, APIDeps{CommandRuns: svc, CommandRunKey: key}, ControlDeps{})
}

func TestCommandRunRoutesAdmitOnlyAPersonsClient(t *testing.T) {
	key := DeriveCommandRunKey("runtime-token")
	body := `{"command":"echo hi","shell":"auto"}`
	type request struct {
		method, path, body string
	}
	routes := []request{
		{http.MethodPost, "/api/v1/sessions/s1/command-runs", body},
		{http.MethodPost, "/api/v1/sessions/s1/command-runs/terminal", body},
		{http.MethodDelete, "/api/v1/sessions/s1/command-runs/cmdrun-1", ""},
	}
	for _, tc := range []struct {
		name    string
		key     string
		prepare func(*http.Request) *http.Request
		allowed bool
	}{
		{name: "ao CLI or a script (native, no Origin)", key: key, prepare: func(r *http.Request) *http.Request { return r }},
		{name: "native client presenting the key without a renderer Origin", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set(CommandRunKeyHeader, key)
			return r
		}},
		{name: "forged renderer Origin without the key", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			return r
		}},
		{name: "renderer Origin with a wrong key", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", testRendererOrigin)
			r.Header.Set(CommandRunKeyHeader, key+"x")
			return r
		}},
		{name: "a local web page with the key", key: key, prepare: func(r *http.Request) *http.Request {
			r.Header.Set("Origin", "http://localhost:5173")
			r.Header.Set(CommandRunKeyHeader, key)
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
			r.Header.Set(CommandRunKeyHeader, "")
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
				svc := &recordingCommandRuns{}
				router := commandRunRouter(t, svc, tc.key)
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
				req.Header.Set("Content-Type", "application/json")
				req = tc.prepare(req)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				called := svc.runs + svc.stops + svc.opens
				if tc.allowed {
					if rec.Code >= 400 || called != 1 {
						t.Fatalf("%s %s: status %d, calls %d; want admitted. body=%s", route.method, route.path, rec.Code, called, rec.Body)
					}
					continue
				}
				if rec.Code != http.StatusForbidden || called != 0 {
					t.Fatalf("%s %s: status %d, calls %d; want 403 before the service. body=%s", route.method, route.path, rec.Code, called, rec.Body)
				}
				var apiErr struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &apiErr)
				if !strings.HasPrefix(apiErr.Code, "COMMAND_RUN_USER_CLIENT_REQUIRED") && apiErr.Code != "ORIGIN_FORBIDDEN" {
					t.Fatalf("%s %s: code %q", route.method, route.path, apiErr.Code)
				}
			}
		})
	}
}

func TestCommandRunStreamsServerSentEvents(t *testing.T) {
	key := DeriveCommandRunKey("runtime-token")
	svc := &recordingCommandRuns{}
	router := commandRunRouter(t, svc, key)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/command-runs", strings.NewReader(`{"command":"Get-Date"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testRendererOrigin)
	req.Header.Set(CommandRunKeyHeader, key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var events []map[string]json.RawMessage
	for _, frame := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		data, ok := strings.CutPrefix(frame, "data: ")
		if !ok {
			t.Fatalf("frame %q", frame)
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 3 || events[0]["started"] == nil || string(events[1]["output"]) != `"ran Get-Date"` || events[2]["exit"] == nil {
		t.Fatalf("events = %s", rec.Body)
	}
}
