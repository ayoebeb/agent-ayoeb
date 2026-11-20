package daemon

import (
	"context"
	"os"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
	liveserversvc "github.com/aoagents/agent-orchestrator/backend/internal/service/liveserver"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// previewOwner tags the command terminals that host managed preview servers.
const previewOwner = "preview-server"

// previewTerminals runs managed preview servers in shell-terminal PTYs, so the
// desktop can attach to the same terminal the server writes to.
type previewTerminals struct {
	terminals *shelltermsvc.Service
	runtime   interface {
		GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
	}
}

func (p previewTerminals) Open(ctx context.Context, spec previewserver.TerminalSpec) (string, error) {
	terminal, err := p.terminals.OpenCommandTerminal(ctx, shelltermsvc.OpenCommandTerminalInput{
		Owner: previewOwner, Argv: spec.Argv, Env: spec.Env, WorkingDir: spec.Dir, Title: spec.Title,
	})
	if err != nil {
		return "", err
	}
	return terminal.HandleID, nil
}

func (p previewTerminals) Output(ctx context.Context, handleID string, lines int) (string, error) {
	return p.runtime.GetOutput(ctx, ports.RuntimeHandle{ID: handleID}, lines)
}

func (p previewTerminals) Alive(ctx context.Context, handleID string) (bool, error) {
	return p.terminals.IsShellTerminalChildAlive(ctx, handleID)
}

func (p previewTerminals) Close(ctx context.Context, handleID string) error {
	return p.terminals.CloseShellTerminal(ctx, handleID)
}

// liveServerDeps are the daemon pieces the live server service reads.
type liveServerDeps struct {
	dataDir  string
	managed  *previewserver.Manager
	sessions *sessionsvc.Service
	projects projectsvc.Manager
	runtime  ports.RuntimeProcessRootInspector
}

func newLiveServerService(d liveServerDeps) *liveserversvc.Service {
	return liveserversvc.New(liveserversvc.Deps{
		Managed: d.managed,
		Sessions: func(ctx context.Context, project domain.ProjectID) ([]liveserversvc.Session, error) {
			list, err := d.sessions.List(ctx, sessionsvc.ListFilter{ProjectID: project})
			if err != nil {
				return nil, err
			}
			out := make([]liveserversvc.Session, 0, len(list))
			for _, sess := range list {
				name := strings.TrimSpace(sess.DisplayName)
				if name == "" {
					name = string(sess.ID)
				}
				out = append(out, liveserversvc.Session{
					ID: sess.ID, Name: name, WorkspacePath: sess.Metadata.WorkspacePath,
					RootPIDs: d.rootPIDs(ctx, sess), Terminated: sess.IsTerminated,
				})
			}
			return out, nil
		},
		ProjectEnv: func(ctx context.Context, project domain.ProjectID) map[string]string {
			res, err := d.projects.Get(ctx, project)
			if err != nil || res.Project == nil || res.Project.Config == nil {
				return nil
			}
			return res.Project.Config.Env
		},
		Listeners: liveserversvc.SystemListeners,
		Table:     func(ctx context.Context) (*procmem.Table, error) { return procmem.Snapshot(ctx, nil) },
		Probe:     liveserversvc.SystemProbe,
		Kill:      liveserversvc.SystemKill,
		SetPreview: func(ctx context.Context, id domain.SessionID, url string) error {
			_, err := d.sessions.SetPreview(ctx, id, url)
			return err
		},
		PreviewURL: func(ctx context.Context, id domain.SessionID) string {
			sess, err := d.sessions.Get(ctx, id)
			if err != nil {
				return ""
			}
			return sess.Metadata.PreviewURL
		},
		OwnPID: os.Getpid(),
	})
}

// rootPIDs names where a session's process tree starts: the runtime handle
// for terminal sessions, the provider host for Chat sessions. Failures yield
// no roots, so the session simply owns no detected servers.
func (d liveServerDeps) rootPIDs(ctx context.Context, sess domain.Session) []int {
	if sess.Mode == domain.SessionModeChat {
		if pid, ok := persistenthost.HostPID(d.dataDir, string(sess.ID)); ok {
			return []int{pid}
		}
		return nil
	}
	if sess.Metadata.RuntimeHandleID == "" {
		return nil
	}
	roots, err := d.runtime.ProcessRootPIDs(ctx, ports.RuntimeHandle{ID: sess.Metadata.RuntimeHandleID})
	if err != nil {
		return nil
	}
	return roots
}
