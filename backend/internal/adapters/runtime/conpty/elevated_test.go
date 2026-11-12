package conpty

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestElevatedAddrRoundTrips(t *testing.T) {
	want := elevatedAddr{Pipe: elevatedPipePrefix + "abc", Token: "t0k", ServerPID: 4242}
	got, ok := parseElevatedAddr(want.String())
	if !ok || got != want {
		t.Fatalf("parseElevatedAddr(%q) = %+v, %v; want %+v", want.String(), got, ok, want)
	}
}

func TestParseElevatedAddrRejectsLoopbackAndMalformedAddresses(t *testing.T) {
	for _, addr := range []string{
		"", "127.0.0.1:4000", unresolvedHostAddress, "elevated-pipe:", "elevated-pipe:p|t",
		"elevated-pipe:p|t|0", "elevated-pipe:p|t|-1", "elevated-pipe:|t|5", "elevated-pipe:p||5",
	} {
		if got, ok := parseElevatedAddr(addr); ok {
			t.Errorf("parseElevatedAddr(%q) = %+v, want rejected", addr, got)
		}
	}
}

func TestElevatedArgsLeadTheOrdinaryHostOptions(t *testing.T) {
	cfg := elevatedHostConfig{Pipe: elevatedPipePrefix + "abc", Token: "tok", ParentPID: 42}
	args := append(elevatedHostArgs(cfg), "--start=attach", "--size=80x24", "shellterm-admin-1", `C:\work`, "pwsh.exe", "-NoLogo")
	got, rest := splitElevatedArgs(args)
	if got != cfg {
		t.Fatalf("elevated config = %+v, want %+v", got, cfg)
	}
	opts, rest := splitHostOptionArgs(rest)
	if !opts.StartOnAttach || opts.Cols != 80 || opts.Rows != 24 {
		t.Fatalf("host options = %+v", opts)
	}
	if len(rest) != 4 || rest[0] != "shellterm-admin-1" || rest[2] != "pwsh.exe" {
		t.Fatalf("positional args = %q", rest)
	}

	plain, rest := splitElevatedArgs([]string{"shellterm-1", "/tmp", "zsh"})
	if plain.enabled() || len(rest) != 3 {
		t.Fatalf("a normal host argv must pass through: %+v %q", plain, rest)
	}
}

func TestElevatedHostConfigValidate(t *testing.T) {
	good := elevatedHostConfig{Pipe: elevatedPipePrefix + "abc", Token: "tok", ParentPID: 1}
	if err := good.validate(); err != nil {
		t.Fatalf("validate(good) = %v", err)
	}
	for name, cfg := range map[string]elevatedHostConfig{
		"foreign pipe":  {Pipe: `\\.\pipe\something-else`, Token: "tok", ParentPID: 1},
		"bare prefix":   {Pipe: elevatedPipePrefix, Token: "tok", ParentPID: 1},
		"no token":      {Pipe: elevatedPipePrefix + "abc", ParentPID: 1},
		"huge token":    {Pipe: elevatedPipePrefix + "abc", Token: string(make([]byte, maxElevatedTokenLen+1)), ParentPID: 1},
		"no parent pid": {Pipe: elevatedPipePrefix + "abc", Token: "tok"},
	} {
		if err := cfg.validate(); err == nil {
			t.Errorf("validate(%s) = nil, want error", name)
		}
	}
}

// acceptLoop feeds every connection authListener admits into a channel.
func acceptLoop(t *testing.T, ln net.Listener) <-chan net.Conn {
	t.Helper()
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	return accepted
}

func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !isReset(err) {
		t.Fatalf("read on refused connection = %v, want the host to close it", err)
	}
}

func isReset(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && !opErr.Timeout()
}

func TestAuthListenerAdmitsOnlyTheLaunchingDaemonWithItsToken(t *testing.T) {
	previous := elevatedAuthTimeout
	elevatedAuthTimeout = 150 * time.Millisecond
	t.Cleanup(func() { elevatedAuthTimeout = previous })

	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var peer atomic.Int64
	ln := newAuthListener(inner, "secret-token", 7, func(net.Conn) (int, error) { return int(peer.Load()), nil })
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptLoop(t, ln)
	dial := func() net.Conn {
		conn, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	// Another process of the same user, even one that knows the token.
	peer.Store(8)
	other := dial()
	if err := writeAuthFrame(other, "secret-token"); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, other)

	// The daemon, with a wrong token.
	peer.Store(7)
	wrong := dial()
	if err := writeAuthFrame(wrong, "guessed"); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, wrong)

	// The daemon, sending a request instead of authenticating.
	silent := dial()
	status, _ := EncodeMessage(MsgStatusReq, nil)
	if _, err := silent.Write(status); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, silent)

	select {
	case <-ln.Authenticated():
		t.Fatal("no connection has authenticated yet")
	default:
	}

	// The daemon with the token: admitted, and the host sees the next frame.
	good := dial()
	if err := writeAuthFrame(good, "secret-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := good.Write(status); err != nil {
		t.Fatal(err)
	}
	select {
	case conn := <-accepted:
		header := make([]byte, 5)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(conn, header); err != nil || header[0] != MsgStatusReq {
			t.Fatalf("host read %v (%v), want the status request after auth", header, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated connection was never handed to the host")
	}
	select {
	case <-ln.Authenticated():
	default:
		t.Fatal("Authenticated() must close after the first admitted client")
	}
	if len(accepted) != 0 {
		t.Fatalf("refused connections reached the host: %d", len(accepted))
	}
}

func TestSuperviseElevatedHostStopsAHostNobodyReached(t *testing.T) {
	previous := elevatedIdleTimeout
	elevatedIdleTimeout = 30 * time.Millisecond
	t.Cleanup(func() { elevatedIdleTimeout = previous })

	ln := newAuthListener(nil, "tok", 1, nil)
	stopped := make(chan struct{})
	superviseElevatedHost(make(chan struct{}), ln, func() { close(stopped) })
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("an unreached elevated host must stop itself")
	}

	reached := newAuthListener(nil, "tok", 1, nil)
	reached.authOnce.Do(func() { close(reached.authed) })
	var stops atomic.Int32
	superviseElevatedHost(make(chan struct{}), reached, func() { stops.Add(1) })
	time.Sleep(120 * time.Millisecond)
	if stops.Load() != 0 {
		t.Fatal("an authenticated elevated host must keep running")
	}
}

func TestCreateElevatedDeclinedPromptLeavesNoRuntime(t *testing.T) {
	isolateRegistry(t)
	ordinarySpawns := 0
	rt := New(Options{
		Spawner: func(context.Context, string, string, []string, map[string]string, HostOptions) (string, int, error) {
			ordinarySpawns++
			return "", 0, errors.New("must not spawn an ordinary host")
		},
		ElevatedSpawner: func(context.Context, string, string, []string, HostOptions) (string, int, error) {
			return "", 0, ports.ErrElevationCancelled
		},
	})
	handle := ports.RuntimeHandle{ID: "shellterm-admin-1"}
	_, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "shellterm-admin-1", WorkspacePath: t.TempDir(), Argv: []string{"pwsh.exe"}, Elevated: true,
	})
	if !errors.Is(err, ports.ErrElevationCancelled) {
		t.Fatalf("Create = %v, want ErrElevationCancelled", err)
	}
	if ordinarySpawns != 0 {
		t.Fatal("a declined elevation must never fall back to an ordinary host")
	}
	if alive, err := rt.IsAlive(context.Background(), handle); alive || err != nil {
		t.Fatalf("IsAlive after decline = %v, %v; want definitively gone", alive, err)
	}
	entries, _, err := ptyregistry.Scan(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("registry after decline = %+v, %v; want empty", entries, err)
	}
}

func TestCreateElevatedKeepsTheHostOutOfTheRegistry(t *testing.T) {
	isolateRegistry(t)
	addr := elevatedAddr{Pipe: elevatedPipePrefix + "abc", Token: "secret", ServerPID: livePID()}
	var gotOpts HostOptions
	rt := New(Options{ElevatedSpawner: func(_ context.Context, _ string, _ string, _ []string, opts HostOptions) (string, int, error) {
		gotOpts = opts
		return addr.String(), addr.ServerPID, nil
	}})
	registered := 0
	rt.registerHost = func(context.Context, ptyregistry.Entry) error { registered++; return nil }
	unregistered := 0
	rt.unregisterHost = func(context.Context, string) error { unregistered++; return nil }
	rt.killHost = func(string) error { return nil }
	rt.pidIsAlive = func(int) bool { return false }

	handle, err := rt.Create(context.Background(), ports.RuntimeConfig{
		SessionID: "shellterm-admin-2", WorkspacePath: t.TempDir(), Argv: []string{"pwsh.exe"}, Elevated: true,
		StartOnAttach: true, InitialCols: 100, InitialRows: 30,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !gotOpts.StartOnAttach || gotOpts.Cols != 100 || gotOpts.Rows != 30 {
		t.Fatalf("elevated host options = %+v", gotOpts)
	}
	if registered != 0 {
		t.Fatal("an elevated host's token-bearing address must never reach the registry")
	}
	if err := rt.Destroy(context.Background(), handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if unregistered != 0 {
		t.Fatal("Destroy must not touch the registry for an elevated host")
	}
}
