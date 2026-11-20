package previewserver

import (
	"context"
	"errors"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeTerminals hosts each "terminal" as a plain child process so the manager
// can exercise its PTY path (readiness, exit watching, stop) without a PTY.
type fakeTerminals struct {
	mu     sync.Mutex
	procs  map[string]*exec.Cmd
	done   map[string]chan struct{}
	closed []string
	output string
	opens  []TerminalSpec
}

func newFakeTerminals() *fakeTerminals {
	return &fakeTerminals{procs: map[string]*exec.Cmd{}, done: map[string]chan struct{}{}}
}

func (f *fakeTerminals) Open(_ context.Context, spec TerminalSpec) (string, error) {
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...) //nolint:gosec // test helper re-executes the test binary
	cmd.Dir = spec.Dir
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "term-" + spec.Title
	f.procs[id] = cmd
	f.opens = append(f.opens, spec)
	done := make(chan struct{})
	f.done[id] = done
	go func() { _ = cmd.Wait(); close(done) }()
	return id, nil
}

func (f *fakeTerminals) Output(context.Context, string, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, nil
}

func (f *fakeTerminals) Alive(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	done, ok := f.done[id]
	f.mu.Unlock()
	if !ok {
		return false, errors.New("unknown terminal")
	}
	select {
	case <-done:
		return false, nil
	default:
		return true, nil
	}
}

func (f *fakeTerminals) Close(_ context.Context, id string) error {
	f.mu.Lock()
	cmd := f.procs[id]
	f.closed = append(f.closed, id)
	f.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func TestManagerRunsServerInTerminalAndStopsIt(t *testing.T) {
	prev := terminalPollInterval
	terminalPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { terminalPollInterval = prev })

	workspace := writeLaunchFile(t, []Configuration{helperConfiguration("web", TargetApp)})
	terminals := newFakeTerminals()
	terminals.output = "\x1b[32mready\x1b[0m on port\r\nline two\r\n"
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	manager.SetTerminals(terminals)
	t.Cleanup(manager.Close)

	status, err := manager.Start(context.Background(), "ao-1", workspace, "", nil)
	if err != nil {
		t.Fatalf("Start: %v\nstatus=%+v", err, status)
	}
	if status.State != StateReady || status.TerminalID == "" || status.Command == "" {
		t.Fatalf("status = %+v, want ready run with terminal id and command", status)
	}
	if len(terminals.opens) != 1 || terminals.opens[0].Title != "web :"+strconv.Itoa(status.Port) {
		t.Fatalf("opens = %+v", terminals.opens)
	}
	time.Sleep(100 * time.Millisecond)
	logs := manager.Status("ao-1").Logs
	if len(logs) != 2 || logs[0] != "ready on port" {
		t.Fatalf("logs = %q, want ANSI-stripped terminal tail", logs)
	}
	if got := manager.List(); len(got) != 1 || got[0].TerminalID != status.TerminalID {
		t.Fatalf("List = %+v", got)
	}

	stopped, err := manager.Stop(context.Background(), "ao-1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.State != StateStopped {
		t.Fatalf("stopped state = %q", stopped.State)
	}
	if len(terminals.closed) != 1 {
		t.Fatalf("closed terminals = %v, want one", terminals.closed)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("List after stop = %+v", got)
	}
}

func TestManagerMarksTerminalRunFailedWhenProcessExits(t *testing.T) {
	prev := terminalPollInterval
	terminalPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { terminalPollInterval = prev })

	cfg := crashConfiguration("crashy", TargetApp)
	workspace := writeLaunchFile(t, []Configuration{cfg})
	terminals := newFakeTerminals()
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	manager.SetTerminals(terminals)
	t.Cleanup(manager.Close)
	exited := make(chan Status, 1)
	manager.SetOnExit(func(_ context.Context, _ domain.SessionID, status Status) { exited <- status })

	if _, err := manager.Start(context.Background(), "ao-2", workspace, "", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case status := <-exited:
		if status.State != StateFailed {
			t.Fatalf("exit status = %+v, want failed", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnExit was not fired for a terminal-hosted server that exited")
	}
	if os.Getenv("NEVER") != "" {
		t.Skip()
	}
}
