package conpty

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// elevatedSpawner starts an administrator host through the platform's prompt
// and returns its in-memory address and PID once it serves its pipe.
type elevatedSpawner func(ctx context.Context, sessionID, cwd string, argv []string, opts HostOptions) (addr string, pid int, err error)

// createElevated starts an administrator host. It is never reserved in the
// pty registry: the address holds the host's token, and the host ends with
// this daemon anyway, so a restarted daemon correctly finds it gone.
//
// A declined prompt (ports.ErrElevationCancelled) leaves nothing behind. A
// host that started but never served its pipe ends itself once no client
// authenticates (elevatedIdleTimeout); this daemon cannot kill it, as an
// unelevated process has no right to terminate an elevated one.
func (r *Runtime) createElevated(ctx context.Context, id string, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.mu.Lock()
	if _, dup := r.sessions[id]; dup {
		r.mu.Unlock()
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: session %q already exists; destroy before re-creating", id))
	}
	r.sessions[id] = &hostSession{addr: unresolvedHostAddress, currentOwner: true, elevated: true}
	r.mu.Unlock()

	addr, pid, err := r.elevatedSpawner(ctx, id, cfg.WorkspacePath, cfg.Argv,
		HostOptions{StartOnAttach: cfg.StartOnAttach, LazySurface: cfg.LazyStyledOutput, Cols: cfg.InitialCols, Rows: cfg.InitialRows})
	if err != nil {
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
		return ports.RuntimeHandle{}, conptyCreateFailure(fmt.Errorf("conpty: start administrator pty-host for %q: %w", id, err))
	}
	r.mu.Lock()
	r.sessions[id] = &hostSession{
		addr: addr, pid: pid, currentOwner: true, elevated: true,
		protocolVersion: conPTYHostProtocolVersion, protocolResolved: true,
	}
	r.mu.Unlock()
	return ports.RuntimeHandle{ID: id}, nil
}
