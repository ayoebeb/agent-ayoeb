package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

// These tests drive the real router, shell terminal service and terminal mux
// over the loopback listener and over the LAN listener (started on loopback,
// as a managed tunnel does), so the local-desktop-only rule is proven where it
// is enforced: in the daemon, not the UI.

type elevatedFakeRuntime struct {
	mu      sync.Mutex
	created []ports.RuntimeConfig
}

func (r *elevatedFakeRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, cfg)
	return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
}
func (r *elevatedFakeRuntime) Destroy(context.Context, ports.RuntimeHandle) error { return nil }
func (r *elevatedFakeRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", nil
}
func (r *elevatedFakeRuntime) SendInput(context.Context, ports.RuntimeHandle, string) error {
	return nil
}
func (r *elevatedFakeRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	return nil
}
func (r *elevatedFakeRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}
func (r *elevatedFakeRuntime) IsChildAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

func (r *elevatedFakeRuntime) elevatedCreates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, cfg := range r.created {
		if cfg.Elevated {
			n++
		}
	}
	return n
}

type elevatedFakeStore struct {
	mu   sync.Mutex
	rows []shelltermsvc.ShellTerminalRecord
}

func (s *elevatedFakeStore) SelectRestorableShellTerminals(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shelltermsvc.ShellTerminalRecord(nil), s.rows...), nil
}
func (s *elevatedFakeStore) InsertShellTerminal(_ context.Context, rec shelltermsvc.ShellTerminalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rec)
	return nil
}
func (s *elevatedFakeStore) UpdateShellTerminalTitle(context.Context, string, string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	return shelltermsvc.ShellTerminalRecord{}, false, nil
}
func (s *elevatedFakeStore) SelectShellTerminalByHandleID(_ context.Context, id string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.rows {
		if rec.HandleID == id {
			return rec, true, nil
		}
	}
	return shelltermsvc.ShellTerminalRecord{}, false, nil
}
func (s *elevatedFakeStore) SelectShellTerminalsByAppRunID(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *elevatedFakeStore) SelectShellTerminalsBySessionID(context.Context, domain.SessionID) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *elevatedFakeStore) SelectShellTerminalsFromPreviousAppRuns(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *elevatedFakeStore) DeleteShellTerminalByHandleID(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, rec := range s.rows {
		if rec.HandleID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (s *elevatedFakeStore) DeleteShellTerminalsFromPreviousAppRuns(context.Context, string) (int64, error) {
	return 0, nil
}

// attachRecorder is the mux's runtime: it records every handle a client got
// as far as attaching, and serves an idle stream.
type attachRecorder struct {
	mu       sync.Mutex
	attached []string
}

func (a *attachRecorder) Attach(ctx context.Context, handle ports.RuntimeHandle, _, _ uint16) (ports.Stream, error) {
	a.mu.Lock()
	a.attached = append(a.attached, handle.ID)
	a.mu.Unlock()
	return newIdleStream(ctx), nil
}
func (a *attachRecorder) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}
func (a *attachRecorder) attachedTo(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, got := range a.attached {
		if got == id {
			return true
		}
	}
	return false
}

type idleStream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newIdleStream(ctx context.Context) *idleStream {
	r, w := io.Pipe()
	s := &idleStream{r: r, w: w}
	context.AfterFunc(ctx, func() { _ = s.Close() })
	return s
}
func (s *idleStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *idleStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *idleStream) Resize(uint16, uint16) error { return nil }
func (s *idleStream) Close() error {
	_ = s.w.Close()
	return s.r.Close()
}

type elevatedBoundary struct {
	runtime    *elevatedFakeRuntime
	attach     *attachRecorder
	local      string // loopback listener base URL
	remote     string // LAN listener base URL (tunnel-style, bound on loopback)
	credential string
}

func newElevatedBoundary(t *testing.T) *elevatedBoundary {
	t.Helper()
	b := &elevatedBoundary{runtime: &elevatedFakeRuntime{}, attach: &attachRecorder{}}
	svc := shelltermsvc.NewService(b.runtime, &elevatedFakeStore{}, nil, nil, t.TempDir(), "run-1", discardLogger())
	svc.SetElevationSupportedForTest(true)
	b.credential = shelltermsvc.DesktopElevationToken("desktop-runtime-token")
	svc.SetDesktopElevationToken(b.credential)

	mux := terminal.NewManager(b.attach, nil, discardLogger())
	t.Cleanup(mux.Close)
	mux.SetAttachGuard(svc.AuthorizeTerminalAttach)
	router := NewRouterWithControl(config.Config{}, discardLogger(), mux, APIDeps{ShellTerminals: svc}, ControlDeps{})

	local := httptest.NewServer(router)
	t.Cleanup(local.Close)
	b.local = local.URL

	lan := NewMobileLAN(router, "host-1", 0, discardLogger(), nil)
	lan.SetPasswordHash(mobilebridge.HashPassword("secret12"))
	port, err := lan.StartLoopback(0)
	if err != nil {
		t.Fatalf("start LAN listener: %v", err)
	}
	t.Cleanup(func() { _ = lan.Stop(context.Background()) })
	b.remote = fmt.Sprintf("http://127.0.0.1:%d", port)
	return b
}

type elevatedClient struct {
	base       string
	credential string // desktop credential header, as the app's main process adds it
	bearer     string // remote-host password
	callerID   string // an agent CLI call names its session
}

func (c elevatedClient) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	c.headers(req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c elevatedClient) headers(h http.Header) {
	if c.credential != "" {
		h.Set(requestscope.DesktopCredentialHeader, c.credential)
	}
	if c.bearer != "" {
		h.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.callerID != "" {
		h.Set(caller.Header, c.callerID)
	}
}

// attachRefused opens the mux as this client and asks to attach terminalID.
func (c elevatedClient) attachRefused(t *testing.T, terminalID string) bool {
	t.Helper()
	header := http.Header{}
	c.headers(header)
	url := "ws" + strings.TrimPrefix(c.base, "http") + "/mux"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
	if err := wsjson.Write(ctx, conn, terminalMuxFrame{Ch: "terminal", ID: terminalID, Type: "open"}); err != nil {
		t.Fatalf("write open: %v", err)
	}
	for {
		var frame terminalMuxFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatalf("waiting for the attach answer: %v", err)
		}
		if frame.Ch != "terminal" || frame.ID != terminalID {
			continue
		}
		switch frame.Type {
		case "error":
			return true
		case "opened":
			return false
		}
	}
}

func (b *elevatedBoundary) openElevated(t *testing.T, c elevatedClient) (int, string, string) {
	t.Helper()
	status, body := c.do(t, http.MethodPost, "/api/v1/shell-terminals", `{"shell":"pwsh","elevated":true}`)
	code, _ := body["code"].(string)
	handle := ""
	if term, ok := body["shellTerminal"].(map[string]any); ok {
		handle, _ = term["handleId"].(string)
	}
	return status, code, handle
}

func listedElevated(body map[string]any) int {
	n := 0
	terms, _ := body["shellTerminals"].([]any)
	for _, raw := range terms {
		if term, _ := raw.(map[string]any); term["elevated"] == true {
			n++
		}
	}
	return n
}

func TestElevatedTerminalRequestsAreRefusedUnlessTheyComeFromTheLocalDesktopApp(t *testing.T) {
	b := newElevatedBoundary(t)
	desktop := elevatedClient{base: b.local, credential: b.credential}

	for name, tc := range map[string]struct {
		client elevatedClient
		code   string
	}{
		"remote client with the desktop credential": {elevatedClient{base: b.remote, bearer: "secret12", credential: b.credential}, "ELEVATED_TERMINAL_REMOTE"},
		"remote client without it":                  {elevatedClient{base: b.remote, bearer: "secret12"}, "ELEVATED_TERMINAL_REMOTE"},
		"agent CLI on this computer":                {elevatedClient{base: b.local, credential: b.credential, callerID: "proj-orchestrator"}, "ELEVATED_TERMINAL_DESKTOP_ONLY"},
		"loopback script without the credential":    {elevatedClient{base: b.local}, "ELEVATED_TERMINAL_DESKTOP_ONLY"},
		"loopback script with a guessed credential": {elevatedClient{base: b.local, credential: "guess"}, "ELEVATED_TERMINAL_DESKTOP_ONLY"},
	} {
		status, code, _ := b.openElevated(t, tc.client)
		if status != http.StatusForbidden || code != tc.code {
			t.Errorf("%s: open elevated = %d %q, want 403 %q", name, status, code, tc.code)
		}
	}
	if n := b.runtime.elevatedCreates(); n != 0 {
		t.Fatalf("refused requests started %d elevated runtimes (each one is a UAC prompt)", n)
	}

	status, code, handle := b.openElevated(t, desktop)
	if status != http.StatusCreated || handle == "" {
		t.Fatalf("desktop open elevated = %d %q", status, code)
	}
	if n := b.runtime.elevatedCreates(); n != 1 {
		t.Fatalf("elevated runtimes = %d, want the desktop's one", n)
	}

	// Only the desktop app sees it, can attach (read or type), or close it.
	_, list := desktop.do(t, http.MethodGet, "/api/v1/shell-terminals", "")
	if listedElevated(list) != 1 {
		t.Fatalf("the desktop app must see its administrator terminal: %v", list)
	}
	outsiders := map[string]elevatedClient{
		"remote client":  {base: b.remote, bearer: "secret12", credential: b.credential},
		"agent CLI":      {base: b.local, credential: b.credential, callerID: "proj-orchestrator"},
		"loopback agent": {base: b.local},
	}
	for name, client := range outsiders {
		_, list := client.do(t, http.MethodGet, "/api/v1/shell-terminals", "")
		if listedElevated(list) != 0 {
			t.Errorf("%s can see the administrator terminal", name)
		}
		if !client.attachRefused(t, handle) {
			t.Errorf("%s attached the administrator terminal", name)
		}
		if status, _ := client.do(t, http.MethodDelete, "/api/v1/shell-terminals/"+handle, ""); status != http.StatusForbidden && status != http.StatusNotFound {
			t.Errorf("%s closed the administrator terminal: %d", name, status)
		}
	}
	if b.attach.attachedTo(handle) {
		t.Fatal("a refused client reached the administrator terminal's stream")
	}
	if desktop.attachRefused(t, handle) {
		t.Fatal("the desktop app must be able to attach its administrator terminal")
	}
	if status, _ := desktop.do(t, http.MethodDelete, "/api/v1/shell-terminals/"+handle, ""); status != http.StatusNoContent {
		t.Fatalf("desktop close = %d, want 204", status)
	}
}

func TestTerminalShellsReportsWhetherThisClientMayElevate(t *testing.T) {
	b := newElevatedBoundary(t)
	for name, tc := range map[string]struct {
		client    elevatedClient
		available bool
		reason    string
	}{
		"desktop app":   {elevatedClient{base: b.local, credential: b.credential}, true, ""},
		"remote client": {elevatedClient{base: b.remote, bearer: "secret12", credential: b.credential}, false, "remote"},
		"agent or CLI":  {elevatedClient{base: b.local}, false, "desktop-only"},
	} {
		status, body := tc.client.do(t, http.MethodGet, "/api/v1/terminal-shells", "")
		elevation, _ := body["elevation"].(map[string]any)
		reason, _ := elevation["reason"].(string)
		if status != http.StatusOK || elevation["available"] != tc.available || reason != tc.reason {
			t.Errorf("%s: terminal-shells = %d %v, want available=%v reason=%q", name, status, elevation, tc.available, tc.reason)
		}
	}
}
