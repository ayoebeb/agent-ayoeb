package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/terminal"
)

func TestReadDesktopHandoff(t *testing.T) {
	secret := strings.Repeat("s", minElevationSecretBytes)
	got, err := readDesktopHandoff(strings.NewReader("runtime-token\n" + secret + "\n"))
	if err != nil || got.browserRuntimeToken != "runtime-token" || got.elevationSecret != secret {
		t.Fatalf("two-line handoff = %+v, %v", got, err)
	}
	// An app that sends only the runtime token gives no elevation secret.
	got, err = readDesktopHandoff(strings.NewReader("runtime-token\n"))
	if err != nil || got.elevationSecret != "" {
		t.Fatalf("one-line handoff = %+v, %v", got, err)
	}
	if _, err := readDesktopHandoff(strings.NewReader("runtime-token\nshort\n")); err == nil {
		t.Fatal("a weak elevation secret must be rejected")
	}
	if _, err := readDesktopHandoff(strings.NewReader("\n")); err == nil {
		t.Fatal("an empty runtime token must be rejected")
	}
}

// A process of the same user can read every AO file, including
// browser-runtime.key. Nothing it can compute from that key may list, open,
// attach to (read or type into) or close an administrator terminal: the
// credential is the per-app-run secret from the stdin handoff alone.
func TestBrowserRuntimeKeyReaderCannotReachAdministratorTerminals(t *testing.T) {
	dataDir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "browser-runtime.key"), []byte(base64.RawURLEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// What the app derives from the key file (frontend/src/main/browser-runtime-token.ts).
	runtimeToken := hmacB64(key, "ao-browser-runtime-token-v1")
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	handoff, err := readDesktopHandoff(strings.NewReader(runtimeToken + "\n" + secret + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	boundary := newElevationBoundary(t, dataDir, handoff.elevationSecret)

	desktop := elevationClient{base: boundary.url, credential: secret}
	status, handle := desktop.openElevated(t)
	if status != http.StatusCreated || handle == "" {
		t.Fatalf("desktop open elevated = %d", status)
	}

	// Everything a key-file reader can compute.
	keyFile, err := os.ReadFile(filepath.Join(dataDir, "browser-runtime.key"))
	if err != nil {
		t.Fatal(err)
	}
	readKey, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(keyFile)))
	if err != nil {
		t.Fatal(err)
	}
	derivedToken := hmacB64(readKey, "ao-browser-runtime-token-v1")
	for name, credential := range map[string]string{
		"the browser runtime token":      derivedToken,
		"the earlier derived credential": hmacB64([]byte(derivedToken), "ao-desktop-elevated-terminal-v1"),
		"the raw key":                    strings.TrimSpace(string(keyFile)),
	} {
		attacker := elevationClient{base: boundary.url, credential: credential}
		if listed := attacker.listsElevated(t); listed {
			t.Errorf("%s: lists the administrator terminal", name)
		}
		if status, _ := attacker.openElevated(t); status != http.StatusForbidden {
			t.Errorf("%s: open elevated = %d, want 403", name, status)
		}
		if !attacker.attachRefused(t, handle) {
			t.Errorf("%s: attached the administrator terminal", name)
		}
		if status := attacker.close(t, handle); status != http.StatusForbidden {
			t.Errorf("%s: close = %d, want 403", name, status)
		}
	}
	if boundary.attachedTo(handle) {
		t.Fatal("a key-file reader reached the administrator terminal's stream")
	}
	if boundary.elevatedCreates() != 1 {
		t.Fatalf("elevated runtimes = %d, want only the desktop's", boundary.elevatedCreates())
	}
	if desktop.attachRefused(t, handle) {
		t.Fatal("the desktop app must still attach its administrator terminal")
	}
}

// A daemon the app adopted instead of spawning got no secret over stdin, so it
// refuses administrator terminals even to the desktop until it restarts.
func TestAdoptedDaemonWithoutHandoffSecretRefusesElevation(t *testing.T) {
	handoff, err := readDesktopHandoff(strings.NewReader("runtime-token\n"))
	if err != nil {
		t.Fatal(err)
	}
	boundary := newElevationBoundary(t, t.TempDir(), handoff.elevationSecret)
	for _, credential := range []string{"", "runtime-token", strings.Repeat("x", 43)} {
		client := elevationClient{base: boundary.url, credential: credential}
		if status, _ := client.openElevated(t); status != http.StatusForbidden {
			t.Errorf("credential %q: open elevated = %d, want 403", credential, status)
		}
	}
}

func hmacB64(key []byte, label string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(label))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type elevationBoundary struct {
	url     string
	runtime *boundaryRuntime
	attach  *boundaryAttach
}

func (b *elevationBoundary) elevatedCreates() int {
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	n := 0
	for _, cfg := range b.runtime.created {
		if cfg.Elevated {
			n++
		}
	}
	return n
}

func (b *elevationBoundary) attachedTo(id string) bool {
	b.attach.mu.Lock()
	defer b.attach.mu.Unlock()
	for _, got := range b.attach.attached {
		if got == id {
			return true
		}
	}
	return false
}

func newElevationBoundary(t *testing.T, dataDir, elevationSecret string) *elevationBoundary {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := &elevationBoundary{runtime: &boundaryRuntime{}, attach: &boundaryAttach{}}
	svc := shelltermsvc.NewService(b.runtime, &boundaryStore{}, nil, nil, dataDir, "run-1", log)
	svc.SetElevationSupportedForTest(true)
	mux := terminal.NewManager(b.attach, nil, log)
	t.Cleanup(mux.Close)
	configureShellTerminalElevation(svc, mux, elevationSecret)
	server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, mux, httpd.APIDeps{ShellTerminals: svc}, httpd.ControlDeps{}))
	t.Cleanup(server.Close)
	b.url = server.URL
	return b
}

type elevationClient struct {
	base       string
	credential string
}

func (c elevationClient) request(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.credential != "" {
		req.Header.Set(requestscope.DesktopCredentialHeader, c.credential)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c elevationClient) openElevated(t *testing.T) (int, string) {
	t.Helper()
	status, body := c.request(t, http.MethodPost, "/api/v1/shell-terminals", `{"elevated":true}`)
	handle := ""
	if term, ok := body["shellTerminal"].(map[string]any); ok {
		handle, _ = term["handleId"].(string)
	}
	return status, handle
}

func (c elevationClient) listsElevated(t *testing.T) bool {
	t.Helper()
	_, body := c.request(t, http.MethodGet, "/api/v1/shell-terminals", "")
	terms, _ := body["shellTerminals"].([]any)
	for _, raw := range terms {
		if term, _ := raw.(map[string]any); term["elevated"] == true {
			return true
		}
	}
	return false
}

func (c elevationClient) close(t *testing.T, handle string) int {
	t.Helper()
	status, _ := c.request(t, http.MethodDelete, "/api/v1/shell-terminals/"+handle, "")
	return status
}

type muxFrame struct {
	Ch   string `json:"ch"`
	ID   string `json:"id"`
	Type string `json:"type"`
}

func (c elevationClient) attachRefused(t *testing.T, handle string) bool {
	t.Helper()
	header := http.Header{}
	if c.credential != "" {
		header.Set(requestscope.DesktopCredentialHeader, c.credential)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.base, "http")+"/mux", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial mux: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
	if err := wsjson.Write(ctx, conn, muxFrame{Ch: "terminal", ID: handle, Type: "open"}); err != nil {
		t.Fatal(err)
	}
	for {
		var frame muxFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			t.Fatalf("waiting for the attach answer: %v", err)
		}
		if frame.Ch == "terminal" && frame.ID == handle {
			switch frame.Type {
			case "error":
				return true
			case "opened":
				return false
			}
		}
	}
}

type boundaryRuntime struct {
	mu      sync.Mutex
	created []ports.RuntimeConfig
}

func (r *boundaryRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, cfg)
	return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
}
func (r *boundaryRuntime) Destroy(context.Context, ports.RuntimeHandle) error { return nil }
func (r *boundaryRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", nil
}
func (r *boundaryRuntime) SendInput(context.Context, ports.RuntimeHandle, string) error {
	return nil
}
func (r *boundaryRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	return nil
}
func (r *boundaryRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}
func (r *boundaryRuntime) IsChildAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

type boundaryStore struct {
	mu   sync.Mutex
	rows []shelltermsvc.ShellTerminalRecord
}

func (s *boundaryStore) SelectRestorableShellTerminals(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]shelltermsvc.ShellTerminalRecord(nil), s.rows...), nil
}
func (s *boundaryStore) InsertShellTerminal(_ context.Context, rec shelltermsvc.ShellTerminalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rec)
	return nil
}
func (s *boundaryStore) UpdateShellTerminalTitle(context.Context, string, string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	return shelltermsvc.ShellTerminalRecord{}, false, nil
}
func (s *boundaryStore) SelectShellTerminalByHandleID(_ context.Context, id string) (shelltermsvc.ShellTerminalRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.rows {
		if rec.HandleID == id {
			return rec, true, nil
		}
	}
	return shelltermsvc.ShellTerminalRecord{}, false, nil
}
func (s *boundaryStore) SelectShellTerminalsByAppRunID(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *boundaryStore) SelectShellTerminalsBySessionID(context.Context, domain.SessionID) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *boundaryStore) SelectShellTerminalsFromPreviousAppRuns(context.Context, string) ([]shelltermsvc.ShellTerminalRecord, error) {
	return nil, nil
}
func (s *boundaryStore) DeleteShellTerminalByHandleID(_ context.Context, id string) (bool, error) {
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
func (s *boundaryStore) DeleteShellTerminalsFromPreviousAppRuns(context.Context, string) (int64, error) {
	return 0, nil
}

type boundaryAttach struct {
	mu       sync.Mutex
	attached []string
}

func (a *boundaryAttach) Attach(ctx context.Context, handle ports.RuntimeHandle, _, _ uint16) (ports.Stream, error) {
	a.mu.Lock()
	a.attached = append(a.attached, handle.ID)
	a.mu.Unlock()
	r, w := io.Pipe()
	stream := &boundaryStream{r: r, w: w}
	context.AfterFunc(ctx, func() { _ = stream.Close() })
	return stream, nil
}
func (a *boundaryAttach) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}

type boundaryStream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (s *boundaryStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *boundaryStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *boundaryStream) Resize(uint16, uint16) error { return nil }
func (s *boundaryStream) Close() error {
	_ = s.w.Close()
	return s.r.Close()
}
