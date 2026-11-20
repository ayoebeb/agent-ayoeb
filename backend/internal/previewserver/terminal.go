package previewserver

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// terminalPollInterval is how often a terminal-hosted server is checked for
// exit and its output tail is refreshed.
var terminalPollInterval = time.Second

// TerminalSpec is one command to run in a real PTY.
type TerminalSpec struct {
	Argv  []string
	Env   map[string]string
	Dir   string
	Title string
}

// Terminals runs a managed server in a PTY the desktop can attach to, so the
// Live tab shows its output and the person can type into it. The daemon wires
// it to the shell-terminal service; without it the manager falls back to a
// plain pipe-backed child (no typing).
type Terminals interface {
	Open(ctx context.Context, spec TerminalSpec) (handleID string, err error)
	Output(ctx context.Context, handleID string, lines int) (string, error)
	Alive(ctx context.Context, handleID string) (bool, error)
	Close(ctx context.Context, handleID string) error
}

// SetTerminals late-binds the PTY launcher. Nil restores the pipe fallback.
func (m *Manager) SetTerminals(t Terminals) {
	m.mu.Lock()
	m.terminals = t
	m.mu.Unlock()
}

func (m *Manager) terminalLauncher() Terminals {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terminals
}

// commandLine renders what the server runs, for display only.
func commandLine(executable string, args []string) string {
	return strings.TrimSpace(strings.Join(append([]string{executable}, args...), " "))
}

// running reports whether the run still has a live process to stop.
func (r *serverRun) running() bool {
	if r.terminalID != "" {
		return !r.exited
	}
	return r.cmd != nil
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]`)

// terminalLines turns scrollback text into display lines with escape
// sequences removed.
func terminalLines(text string, limit int) []string {
	text = ansiPattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n \t"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// watchTerminal polls a terminal-hosted run until its process exits, keeping
// the log tail fresh. It is the PTY counterpart of waitForExit.
func (m *Manager) watchTerminal(ctx context.Context, sessionID domain.SessionID, run *serverRun, terminals Terminals) {
	defer close(run.done)
	ticker := time.NewTicker(terminalPollInterval)
	defer ticker.Stop()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if out, err := terminals.Output(pollCtx, run.terminalID, statusLogLines); err == nil {
			run.logs.Replace(terminalLines(out, maxBufferedLogLines))
		}
		alive, err := terminals.Alive(pollCtx, run.terminalID)
		cancel()
		if err == nil && !alive {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	crashed := false
	m.mu.Lock()
	if m.runs[sessionID] == run {
		run.exited = true
		if run.stopping {
			if run.status.State != StateFailed {
				run.status.State = StateStopped
				run.status.Error = ""
			}
		} else {
			run.status.State = StateFailed
			run.status.Error = "preview server exited"
			crashed = true
		}
	}
	m.mu.Unlock()
	if crashed {
		status := m.statusFor(run)
		if fn := m.onExitCallback(); fn != nil {
			notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			fn(notifyCtx, sessionID, status)
			cancel()
		}
	}
	time.AfterFunc(failedStatusRetention, func() {
		m.mu.Lock()
		if m.runs[sessionID] == run && run.exited {
			delete(m.runs, sessionID)
		}
		m.mu.Unlock()
	})
}

// stopTerminalRun closes a terminal-hosted run's PTY and waits for the
// watcher to see the exit.
func (m *Manager) stopTerminalRun(ctx context.Context, sessionID domain.SessionID, run *serverRun, terminals Terminals) (Status, error) {
	if err := terminals.Close(ctx, run.terminalID); err != nil {
		m.log.Warn("close preview terminal", "session", sessionID, "err", err)
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return m.Status(sessionID), ctx.Err()
	case <-time.After(5 * time.Second):
	}
	m.mu.Lock()
	if m.runs[sessionID] == run {
		run.exited = true
		run.status.State = StateStopped
		run.status.Error = ""
	}
	status := m.statusForLocked(run)
	delete(m.runs, sessionID)
	m.mu.Unlock()
	return status, nil
}
