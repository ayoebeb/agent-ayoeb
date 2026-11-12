package shellterm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const testDesktopRuntimeToken = "desktop-runtime-token"

// elevatedTestService is a Windows-like service that trusts the desktop app
// holding testDesktopRuntimeToken.
func elevatedTestService(t *testing.T, rt *fakeShellRuntime, st *fakeShellTerminalStore) *Service {
	t.Helper()
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"proj-orchestrator": {workspacePath: t.TempDir(), projectID: "proj"},
	}}
	projects := &fakeProjectRootLocator{
		roots: map[domain.ProjectID]string{"proj": t.TempDir()},
		envs:  map[domain.ProjectID]map[string]string{"proj": {"PROJECT_SECRET": "x"}},
	}
	svc := newTestServiceWithSessions(rt, st, projects, sessions)
	svc.SetElevationSupportedForTest(true)
	svc.SetDesktopElevationToken(DesktopElevationToken(testDesktopRuntimeToken))
	return svc
}

func desktopContext() context.Context {
	return requestscope.WithDesktopCredential(context.Background(), DesktopElevationToken(testDesktopRuntimeToken))
}

func apiCode(err error) string {
	var apiErr *apierr.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// The same vector is asserted by the desktop app's derivation test
// (frontend/src/main/desktop-elevation.test.ts), so both sides agree.
func TestDesktopElevationTokenMatchesTheDesktopDerivation(t *testing.T) {
	if got, want := DesktopElevationToken("test-runtime-token"), "daz7gKKnkPMiP--MrTxyfqmGBM7nJNwmowx_O95iJb8"; got != want {
		t.Fatalf("DesktopElevationToken = %q, want %q", got, want)
	}
	if DesktopElevationToken("") != "" {
		t.Fatal("no runtime token must derive no credential")
	}
}

func TestElevationAccessIsForTheLocalDesktopAppOnly(t *testing.T) {
	valid := DesktopElevationToken(testDesktopRuntimeToken)
	for _, tc := range []struct {
		name      string
		supported bool
		token     string
		ctx       context.Context
		want      ElevationAccess
	}{
		{"desktop app on this computer", true, valid, desktopContext(), ElevationAccess{Available: true}},
		{"not Windows", false, valid, desktopContext(), ElevationAccess{Reason: ElevationUnsupported}},
		{"remote client, even with the credential", true, valid, requestscope.WithLAN(desktopContext()), ElevationAccess{Reason: ElevationRemote}},
		{"remote client without it", true, valid, requestscope.WithLAN(context.Background()), ElevationAccess{Reason: ElevationRemote}},
		{"agent CLI call naming its session", true, valid, caller.With(desktopContext(), "proj-3"), ElevationAccess{Reason: ElevationDesktopOnly}},
		{"loopback script without a credential", true, valid, context.Background(), ElevationAccess{Reason: ElevationDesktopOnly}},
		{"wrong credential", true, valid, requestscope.WithDesktopCredential(context.Background(), "guess"), ElevationAccess{Reason: ElevationDesktopOnly}},
		{"daemon the desktop did not start", true, "", desktopContext(), ElevationAccess{Reason: ElevationDesktopOnly}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{})
			svc.SetElevationSupportedForTest(tc.supported)
			svc.SetDesktopElevationToken(tc.token)
			if got := svc.ElevationAccess(tc.ctx); got != tc.want {
				t.Fatalf("ElevationAccess = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOpenElevatedTerminalRefusesNonDesktopCallersBeforeAnyRuntimeWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code string
	}{
		{"remote listener", requestscope.WithLAN(desktopContext()), "ELEVATED_TERMINAL_REMOTE"},
		{"agent CLI", caller.With(desktopContext(), "proj-orchestrator"), "ELEVATED_TERMINAL_DESKTOP_ONLY"},
		{"no credential", context.Background(), "ELEVATED_TERMINAL_DESKTOP_ONLY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newFakeShellRuntime()
			st := &fakeShellTerminalStore{}
			svc := elevatedTestService(t, rt, st)
			_, err := svc.OpenShellTerminal(tc.ctx, OpenShellTerminalInput{SessionID: "proj-orchestrator", Shell: "pwsh", Elevated: true})
			if got := apiCode(err); got != tc.code {
				t.Fatalf("code = %q (%v), want %q", got, err, tc.code)
			}
			if len(rt.created) != 0 || len(st.records) != 0 {
				t.Fatal("a refused elevated request must not start or record anything")
			}
		})
	}
}

func TestOpenElevatedTerminalRunsInTheSessionWorkspaceWithACleanEnvironment(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := elevatedTestService(t, rt, st)
	tracker := &recordingTracker{}
	svc.agents = tracker

	terminal, err := svc.OpenShellTerminal(desktopContext(), OpenShellTerminalInput{
		SessionID: "proj-orchestrator", Elevated: true, StartOnAttach: true, Cols: 120, Rows: 40,
	})
	if err != nil {
		t.Fatalf("OpenShellTerminal: %v", err)
	}
	if len(rt.created) != 1 {
		t.Fatalf("runtime creates = %d, want 1", len(rt.created))
	}
	cfg := rt.created[0]
	if !cfg.Elevated {
		t.Fatal("the runtime must be asked for an elevated process")
	}
	if len(cfg.Env) != 0 {
		t.Fatalf("an administrator shell must get no AO or project env, got %v", cfg.Env)
	}
	if !strings.HasPrefix(string(cfg.SessionID), elevatedHandlePrefix) || !IsElevatedHandleID(terminal.HandleID) {
		t.Fatalf("handle %q / %q must carry the administrator prefix", cfg.SessionID, terminal.HandleID)
	}
	ws, _, _ := svc.sessions.SessionWorkspace(context.Background(), "proj-orchestrator")
	if cfg.WorkspacePath != ws || terminal.WorkingDir != ws {
		t.Fatalf("working dir = %q, want the orchestrator workspace %q", cfg.WorkspacePath, ws)
	}
	if !terminal.Elevated || terminal.SessionID != "proj-orchestrator" || terminal.ProjectID != "proj" {
		t.Fatalf("terminal = %+v", terminal)
	}
	if !cfg.StartOnAttach || cfg.InitialCols != 120 || cfg.InitialRows != 40 {
		t.Fatalf("grid options were not passed through: %+v", cfg)
	}
	if len(tracker.tracked) != 0 {
		t.Fatal("an administrator shell must not be watched for agents")
	}
}

func TestOpenElevatedTerminalDeclinedPromptOpensNoTab(t *testing.T) {
	rt := newFakeShellRuntime()
	rt.createErr = fmt.Errorf("conpty: start administrator pty-host: %w", ports.ErrElevationCancelled)
	st := &fakeShellTerminalStore{}
	svc := elevatedTestService(t, rt, st)
	_, err := svc.OpenShellTerminal(desktopContext(), OpenShellTerminalInput{SessionID: "proj-orchestrator", Elevated: true})
	if got := apiCode(err); got != "ELEVATION_DECLINED" {
		t.Fatalf("code = %q (%v), want ELEVATION_DECLINED", got, err)
	}
	if len(st.records) != 0 {
		t.Fatal("a declined prompt must leave no terminal behind")
	}
	list, err := svc.ListShellTerminalsForCurrentAppRun(desktopContext())
	if err != nil || len(list) != 0 {
		t.Fatalf("list after decline = %+v, %v", list, err)
	}
}

func TestElevatedTerminalsAreHiddenAndLockedForEveryoneButTheDesktopApp(t *testing.T) {
	rt := newFakeShellRuntime()
	st := &fakeShellTerminalStore{}
	svc := elevatedTestService(t, rt, st)
	admin, err := svc.OpenShellTerminal(desktopContext(), OpenShellTerminalInput{SessionID: "proj-orchestrator", Elevated: true})
	if err != nil {
		t.Fatal(err)
	}
	user, err := svc.OpenShellTerminal(context.Background(), OpenShellTerminalInput{SessionID: "proj-orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	if user.Elevated || IsElevatedHandleID(user.HandleID) {
		t.Fatalf("a user shell must not read as elevated: %+v", user)
	}

	desktopList, _ := svc.ListShellTerminalsForCurrentAppRun(desktopContext())
	if len(desktopList) != 2 {
		t.Fatalf("the desktop app sees %d terminals, want both", len(desktopList))
	}
	for name, ctx := range map[string]context.Context{
		"remote":    requestscope.WithLAN(desktopContext()),
		"agent CLI": caller.With(desktopContext(), "proj-orchestrator"),
		"script":    context.Background(),
	} {
		list, _ := svc.ListShellTerminalsForCurrentAppRun(ctx)
		if len(list) != 1 || list[0].HandleID != user.HandleID {
			t.Errorf("%s lists %+v, want only the user shell", name, list)
		}
		if err := svc.AuthorizeTerminalAttach(ctx, admin.HandleID); err == nil {
			t.Errorf("%s may attach (read and type into) the administrator terminal", name)
		}
		if err := svc.AuthorizeTerminalAttach(ctx, user.HandleID); err != nil {
			t.Errorf("%s may not attach a user shell: %v", name, err)
		}
		if _, err := svc.RenameShellTerminal(ctx, admin.HandleID, "pwned"); err == nil {
			t.Errorf("%s renamed the administrator terminal", name)
		}
		if err := svc.CloseShellTerminal(ctx, admin.HandleID); err == nil {
			t.Errorf("%s closed the administrator terminal", name)
		}
	}
	if err := svc.AuthorizeTerminalAttach(desktopContext(), admin.HandleID); err != nil {
		t.Fatalf("the desktop app may not attach its administrator terminal: %v", err)
	}
	if err := svc.CloseShellTerminal(desktopContext(), admin.HandleID); err != nil {
		t.Fatalf("the desktop app may not close its administrator terminal: %v", err)
	}
}

func TestIsElevatedHandleIDSeesThroughRuntimeWrappers(t *testing.T) {
	for handle, want := range map[string]bool{
		"shellterm-admin-0123abcd":             true,
		"ptyhost-v1:shellterm-admin-0123abcd":  true,
		"shellterm-0123abcd":                   false,
		"proj-orchestrator":                    false,
		"ptyhost-v1:shellterm-0123abcd":        false,
		"shellterm-admin-x:shellterm-0123abcd": false,
	} {
		if got := IsElevatedHandleID(handle); got != want {
			t.Errorf("IsElevatedHandleID(%q) = %v, want %v", handle, got, want)
		}
	}
}

type recordingTracker struct{ tracked []string }

func (r *recordingTracker) TrackShellTerminal(handleID string) {
	r.tracked = append(r.tracked, handleID)
}
func (r *recordingTracker) UntrackShellTerminal(handleID string) {}
