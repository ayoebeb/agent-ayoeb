package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/workspace/scratch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recordingPermissionApplier struct {
	calls      []string
	hooksExist bool
	err        error
}

func (a *recordingPermissionApplier) ApplyToWorkspace(_ context.Context, id domain.SessionID, workspacePath string) error {
	a.calls = append(a.calls, string(id)+"@"+workspacePath)
	// The rules go into the file AO's hooks were just written to, so they must
	// merge into it rather than be overwritten by it.
	_, statErr := os.Stat(filepath.Join(workspacePath, ".claude", "settings.local.json"))
	a.hooksExist = statErr == nil
	return a.err
}

// spawnWithApplier spawns into a scratch workspace whose agent installs hooks
// and then fails to start, which is the window in which prepareWorkspace runs.
func spawnWithApplier(t *testing.T, applier *recordingPermissionApplier) (domain.SessionRecord, error) {
	t.Helper()
	st := newFakeStore()
	st.projects["scratch"] = domain.ProjectRecord{ID: "scratch", Kind: domain.ProjectKindScratch, Config: testRoleAgents()}
	workspace, err := scratch.New(scratch.Options{ManagedRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	notFound := func(name string) (string, error) {
		if name == "tmux" {
			return "/bin/tmux", nil
		}
		return "", fmt.Errorf("exec: %q: not found", name)
	}
	m := New(Deps{
		Runtime: &fakeRuntime{}, Agents: singleAgent{agent: &scratchHookAgent{}}, Workspace: workspace,
		Store: &maxSessionNumStore{fakeStore: st}, Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st},
		DataDir: t.TempDir(), LookPath: notFound,
	})
	m.SetProjectPermissionApplier(applier)
	_, _, _, spawnErr := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "scratch", Kind: domain.KindOrchestrator})
	return st.sessions["scratch-1"], spawnErr
}

func TestSpawnAppliesTheProjectsRememberedPermissionsAfterTheHooks(t *testing.T) {
	applier := &recordingPermissionApplier{}
	record, err := spawnWithApplier(t, applier)
	if !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("spawn err = %v, want the missing-binary error that ends this setup", err)
	}
	want := "scratch-1@" + record.Metadata.WorkspacePath
	if len(applier.calls) != 1 || applier.calls[0] != want || record.Metadata.WorkspacePath == "" {
		t.Fatalf("applier calls = %v, want [%s]", applier.calls, want)
	}
	if !applier.hooksExist {
		t.Fatal("permissions were applied before AO's hooks file was written, so the hooks would overwrite them")
	}
}

func TestSpawnStillLaunchesWhenRememberedPermissionsCannotBeApplied(t *testing.T) {
	applier := &recordingPermissionApplier{err: errors.New("settings file is unreadable")}
	_, err := spawnWithApplier(t, applier)
	if errors.Is(err, applier.err) || !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("spawn err = %v: a failed apply must only cost the extra prompts, not the session", err)
	}
	if len(applier.calls) != 1 {
		t.Fatalf("applier calls = %v, want one attempt", applier.calls)
	}
}
