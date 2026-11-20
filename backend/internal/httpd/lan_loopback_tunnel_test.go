package httpd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func TestLoopbackDialTargetsRefuseEverythingButLoopback(t *testing.T) {
	allowed := map[string][]string{
		"localhost":         {"127.0.0.1:80", "[::1]:80"},
		"LOCALHOST.":        {"127.0.0.1:80", "[::1]:80"},
		"app.dev.localhost": {"127.0.0.1:80", "[::1]:80"},
		"127.0.0.1":         {"127.0.0.1:80"},
		"127.8.9.10":        {"127.8.9.10:80"},
		"::1":               {"[::1]:80"},
		"[::1]":             {"[::1]:80"},
		"::ffff:127.0.0.1":  {"127.0.0.1:80"},
	}
	for host, want := range allowed {
		got, err := loopbackDialTargets(host, "80")
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("loopbackDialTargets(%q) = %v, %v; want %v", host, got, err, want)
		}
	}
	for _, host := range []string{
		"", "example.com", "evil.localhost.example.com", "localhost.evil.com",
		"192.168.1.5", "10.0.0.1", "169.254.169.254", "0.0.0.0", "::", "8.8.8.8",
		"2001:db8::1", "fe80::1", "127.0.0.1.nip.io", "127.1", "localhost@evil.com",
	} {
		if got, err := loopbackDialTargets(host, "80"); err == nil {
			t.Errorf("loopbackDialTargets(%q) = %v, want a refusal", host, got)
		}
	}
	for _, port := range []string{"", "0", "-1", "65536", "abc", "80;81"} {
		if _, err := loopbackDialTargets("localhost", port); err == nil {
			t.Errorf("port %q accepted", port)
		}
	}
}

func TestLANLoopbackTunnelRoundTripAndRefusals(t *testing.T) {
	// A dev server on this computer's loopback: answers plain HTTP.
	devServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "dev:"+r.URL.Path)
	}))
	defer devServer.Close()
	_, devPort, _ := net.SplitHostPort(devServer.Listener.Addr().String())

	st := &authState{}
	st.setHash(mobilebridge.HashPassword("secret12"))
	m := NewLANManager(http.NotFoundHandler(), st, 0, slog.Default(), nil)
	port, err := startLANForTest(m, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop(context.Background())
	base := fmt.Sprintf("ws://127.0.0.1:%d%s", port, lanLoopbackTunnelPath)
	auth := &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer secret12"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status := func(query string, opts *websocket.DialOptions) int {
		ws, resp, err := websocket.Dial(ctx, base+query, opts)
		if err == nil {
			ws.CloseNow()
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial %s: %v", query, err)
		}
		return resp.StatusCode
	}
	if got := status("?host=localhost&port="+devPort, nil); got != http.StatusUnauthorized {
		t.Fatalf("without a password: %d, want 401", got)
	}
	if got := status("?host=example.com&port=80", auth); got != http.StatusForbidden {
		t.Fatalf("non-loopback host: %d, want 403", got)
	}
	if got := status("?host=192.168.1.5&port=80", auth); got != http.StatusForbidden {
		t.Fatalf("LAN address: %d, want 403", got)
	}
	if got := status("?host=localhost&port=nope", auth); got != http.StatusBadRequest {
		t.Fatalf("bad port: %d, want 400", got)
	}
	// Reserve a port, close it, and expect a refusal to be a 502 the desktop can read.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	_, closedPort, _ := net.SplitHostPort(closed.Addr().String())
	closed.Close()
	if got := status("?host=127.0.0.1&port="+closedPort, auth); got != http.StatusBadGateway {
		t.Fatalf("closed port: %d, want 502", got)
	}

	// The round trip: an HTTP client whose connection is the tunnel reaches the
	// dev server, as the desktop's browser does through its local proxy.
	ws, _, err := websocket.Dial(ctx, base+"?host=127.0.0.1&port="+devPort, auth)
	if err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer conn.Close()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil },
	}}
	resp, err := client.Get("http://anything.invalid/hello")
	if err != nil {
		t.Fatalf("request through the tunnel: %v", err)
	}
	defer resp.Body.Close()
	if body, _ := io.ReadAll(resp.Body); string(body) != "dev:/hello" {
		t.Fatalf("body %q, want dev:/hello", body)
	}
}

func TestPumpIdleDropsASilentConnection(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a2.Close()
	defer b2.Close()
	done := make(chan struct{})
	go func() { pumpIdle(a1, b1, 100*time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("idle connection was not dropped")
	}
}
