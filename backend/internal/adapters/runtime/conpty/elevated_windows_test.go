//go:build windows

package conpty

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func testElevatedConfig(t *testing.T) elevatedHostConfig {
	t.Helper()
	id, err := randomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	return elevatedHostConfig{Pipe: elevatedPipePrefix + "test-" + id, Token: "tok-" + id, ParentPID: os.Getpid()}
}

// These run unelevated: the pipe's security descriptor (current user only,
// medium label) must be creatable by the same code an elevated host runs.
func TestElevatedPipeAdmitsTheDaemonWithItsToken(t *testing.T) {
	cfg := testElevatedConfig(t)
	ln, err := listenElevatedHost(cfg)
	if err != nil {
		t.Fatalf("listenElevatedHost: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptLoop(t, ln)

	conn, err := dialElevatedHost(context.Background(), elevatedAddr{Pipe: cfg.Pipe, Token: cfg.Token, ServerPID: os.Getpid()}, time.Second)
	if err != nil {
		t.Fatalf("dialElevatedHost: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	frame, _ := EncodeMessage(MsgTerminalInput, []byte("whoami\r"))
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case server := <-accepted:
		got := make([]byte, len(frame))
		_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(server, got); err != nil || string(got[5:]) != "whoami\r" {
			t.Fatalf("host read %q (%v)", got, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon was not admitted")
	}
}

func TestElevatedPipeRefusesAWrongToken(t *testing.T) {
	cfg := testElevatedConfig(t)
	ln, err := listenElevatedHost(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptLoop(t, ln)
	conn, err := dialElevatedHost(context.Background(), elevatedAddr{Pipe: cfg.Pipe, Token: "guessed", ServerPID: os.Getpid()}, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("a wrong token must never be acknowledged")
	}
	if len(accepted) != 0 {
		t.Fatal("a wrong token reached the host")
	}
}

func TestDialRefusesAPipeServedByAnotherProcess(t *testing.T) {
	cfg := testElevatedConfig(t)
	ln, err := listenElevatedHost(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := acceptLoop(t, ln)
	// The daemon launched pid 1; this test process serving the pipe is an impostor.
	_, err = dialElevatedHost(context.Background(), elevatedAddr{Pipe: cfg.Pipe, Token: cfg.Token, ServerPID: 1}, time.Second)
	if !errors.Is(err, errElevatedPipeImpostor) {
		t.Fatalf("dial = %v, want errElevatedPipeImpostor", err)
	}
	if len(accepted) != 0 {
		t.Fatal("the daemon must hang up before authenticating to an impostor")
	}
}

func TestElevatedHostRefusesASquattedPipeName(t *testing.T) {
	cfg := testElevatedConfig(t)
	squatter, err := winio.ListenPipe(cfg.Pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = squatter.Close() })
	if ln, err := listenElevatedHost(cfg); err == nil {
		_ = ln.Close()
		t.Fatal("an elevated host must not share a pipe name someone else created first")
	}
}

func TestDialMissingElevatedPipeReadsAsRefused(t *testing.T) {
	cfg := testElevatedConfig(t)
	_, err := dialElevatedHost(context.Background(), elevatedAddr{Pipe: cfg.Pipe, Token: cfg.Token, ServerPID: os.Getpid()}, 200*time.Millisecond)
	if !isConnRefused(err) {
		t.Fatalf("dial missing pipe = %v, want connection refused", err)
	}
}

// The runtime drives an elevated host over its pipe exactly like a loopback
// one: status, input, output and kill all go through the authenticated pipe.
func TestRuntimeDrivesAnElevatedHostOverItsPipe(t *testing.T) {
	isolateRegistry(t)
	cfg := testElevatedConfig(t)
	pty := newFakePTY(4321)
	ring := NewRing()
	var served atomic.Bool
	serveDone := make(chan struct{})
	rt := New(Options{ElevatedSpawner: func(ctx context.Context, sessionID, cwd string, argv []string, opts HostOptions) (string, int, error) {
		ln, err := listenElevatedHost(cfg)
		if err != nil {
			return "", 0, err
		}
		go func() {
			defer close(serveDone)
			_ = Serve(context.Background(), ServeConfig{SessionID: sessionID, Listener: ln, PTY: pty, Ring: ring})
			served.Store(true)
		}()
		return elevatedAddr{Pipe: cfg.Pipe, Token: cfg.Token, ServerPID: os.Getpid()}.String(), os.Getpid(), nil
	}})
	rt.pidIsAlive = func(int) bool { return !served.Load() }

	ctx := context.Background()
	handle, err := rt.Create(ctx, ports.RuntimeConfig{SessionID: "shellterm-admin-e2e", WorkspacePath: t.TempDir(), Argv: []string{"pwsh.exe"}, Elevated: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if alive, err := rt.IsAlive(ctx, handle); !alive || err != nil {
		t.Fatalf("IsAlive = %v, %v", alive, err)
	}
	ring.Append([]byte("Administrator prompt ready\n"))
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := rt.GetOutput(ctx, handle, 10)
		if err == nil && strings.Contains(out, "Administrator prompt ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GetOutput = %q, %v", out, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	inputDone := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := pty.ReadInput(buf)
		inputDone <- string(buf[:n])
	}()
	if err := rt.SendInput(ctx, handle, "net session"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	select {
	case got := <-inputDone:
		if !strings.Contains(got, "net session") {
			t.Fatalf("host got input %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("input never reached the elevated host")
	}
	if err := rt.Destroy(ctx, handle); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Destroy did not stop the elevated host")
	}
}

func TestDefaultSpawnElevatedHostReportsADeclinedPrompt(t *testing.T) {
	previous := shellExecuteRunAs
	t.Cleanup(func() { shellExecuteRunAs = previous })
	var gotParams string
	shellExecuteRunAs = func(_, params, _ string) (windows.Handle, error) {
		gotParams = params
		return 0, ports.ErrElevationCancelled
	}
	addr, pid, err := defaultSpawnElevatedHost(context.Background(), "shellterm-admin-x", t.TempDir(), []string{`C:\Program Files\PowerShell\7\pwsh.exe`, "-NoLogo"}, HostOptions{StartOnAttach: true})
	if !errors.Is(err, ports.ErrElevationCancelled) || addr != "" || pid != 0 {
		t.Fatalf("spawn = %q, %d, %v; want a clean ErrElevationCancelled", addr, pid, err)
	}
	for _, want := range []string{"pty-host", elevatedPipeArgPrefix + elevatedPipePrefix, elevatedTokenArgPrefix, elevatedParentArgPrefix, "--start=attach", "shellterm-admin-x", "pwsh.exe"} {
		if !strings.Contains(gotParams, want) {
			t.Errorf("runas parameters %q lack %q", gotParams, want)
		}
	}
}

func TestWatchElevatedParentStopsWhenTheDaemonExits(t *testing.T) {
	parent := exec.Command("cmd.exe", "/c", "exit 0")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	var once atomic.Bool
	watchElevatedParent(make(chan struct{}), parent.Process.Pid, func() {
		if once.CompareAndSwap(false, true) {
			close(stopped)
		}
	})
	_ = parent.Wait()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the elevated host must stop when its daemon exits")
	}
}
