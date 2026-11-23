package sessionmanager

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// addCascadeSession adds a session of the same shape as session-1, so its own
// account move can run end to end.
func addCascadeSession(store *transitionStore, accounts *transitionAccounts, id string, mutate func(*domain.SessionRecord)) {
	rec := store.sessions["session-1"]
	rec.ID = domain.SessionID(id)
	rec.Kind = domain.KindWorker
	rec.Metadata.WorkspacePath = "/ws/" + id
	rec.Metadata.Branch = "ao/" + id
	rec.Metadata.RuntimeHandleID = "runtime-" + id
	rec.Metadata.AgentSessionID = "native-" + id
	if mutate != nil {
		mutate(&rec)
	}
	store.sessions[rec.ID] = rec
	accounts.mu.Lock()
	accounts.bound[rec.ID] = "device-claude"
	accounts.mu.Unlock()
}

// Moving an orchestrator's account brings its live workers of the same project
// and the same agent along, and nobody else.
func TestStartWorkerAccountTransitionsMovesOnlyLiveSameProjectSameHarnessWorkers(t *testing.T) {
	manager, store, _, _, accounts, _ := newAccountTransitionManager(t, domain.SessionModeTUI)
	orch := store.sessions["session-1"]
	orch.Kind = domain.KindOrchestrator
	store.sessions["session-1"] = orch
	addCascadeSession(store, accounts, "worker-live", nil)
	addCascadeSession(store, accounts, "worker-other-project", func(r *domain.SessionRecord) { r.ProjectID = "other" })
	addCascadeSession(store, accounts, "worker-codex", func(r *domain.SessionRecord) { r.Harness = domain.HarnessCodex })
	addCascadeSession(store, accounts, "worker-terminated", func(r *domain.SessionRecord) { r.IsTerminated = true })
	addCascadeSession(store, accounts, "worker-exited", func(r *domain.SessionRecord) { r.Activity.State = domain.ActivityExited })
	addCascadeSession(store, accounts, "worker-there", nil)
	accounts.bound["worker-there"] = "acct-b"
	addCascadeSession(store, accounts, "other-orchestrator", func(r *domain.SessionRecord) { r.Kind = domain.KindOrchestrator })

	moves, err := manager.StartWorkerAccountTransitions(context.Background(), "session-1", "acct-b", domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	got := map[domain.SessionID]WorkerAccountMove{}
	for _, mv := range moves {
		got[mv.SessionID] = mv
	}
	if len(got) != 2 {
		t.Fatalf("moves = %+v, want only worker-live (moved) and worker-there (already there)", moves)
	}
	live := got["worker-live"]
	if live.Transition == nil || live.Err != nil {
		t.Fatalf("worker-live = %+v, want a started move", live)
	}
	if there := got["worker-there"]; there.Transition != nil || there.Skipped != WorkerMoveSkipAlreadyThere {
		t.Fatalf("worker-there = %+v, want skipped as already on the account", there)
	}
	if settled := awaitTransition(t, store, live.Transition.ID); settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("worker-live phase = %s %s %s", settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
	if bound := accounts.boundTo("worker-live"); bound != "acct-b" {
		t.Fatalf("worker-live bound to %q, want acct-b", bound)
	}
	for _, id := range []domain.SessionID{"worker-other-project", "worker-codex", "worker-terminated", "worker-exited", "other-orchestrator", "session-1"} {
		if bound := accounts.boundTo(id); bound != "device-claude" {
			t.Fatalf("%s bound to %q, want it untouched", id, bound)
		}
		if _, active, _ := store.GetActiveSessionInterfaceTransition(context.Background(), id); active {
			t.Fatalf("%s was claimed by a move", id)
		}
	}
}

// A worker's own switch never reaches other workers.
func TestStartWorkerAccountTransitionsIgnoresANonOrchestrator(t *testing.T) {
	manager, store, _, _, accounts, _ := newAccountTransitionManager(t, domain.SessionModeTUI)
	addCascadeSession(store, accounts, "worker-2", nil)

	moves, err := manager.StartWorkerAccountTransitions(context.Background(), "session-1", "acct-b", domain.SessionInterfaceTransitionDrain)
	if err != nil || len(moves) != 0 {
		t.Fatalf("moves = %+v, err = %v, want none for a worker", moves, err)
	}
	if bound := accounts.boundTo("worker-2"); bound != "device-claude" {
		t.Fatalf("worker-2 bound to %q, want untouched", bound)
	}
}

// What continues a session stopped on a usage limit runs after the move has
// completed, never before.
func TestAccountMovedHookRunsAfterTheMoveCompletes(t *testing.T) {
	manager, store, _, _, accounts, _ := newAccountTransitionManager(t, domain.SessionModeTUI)
	type seen struct {
		transition domain.SessionInterfaceTransition
		bound      string
	}
	hooked := make(chan seen, 1)
	manager.SetAccountMovedHook(func(_ context.Context, tr domain.SessionInterfaceTransition) {
		hooked <- seen{transition: tr, bound: accounts.boundTo(tr.SessionID)}
	})

	transition, err := manager.StartAccountTransition(context.Background(), "session-1", "acct-b", domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	awaitTransition(t, store, transition.ID)
	select {
	case got := <-hooked:
		if got.transition.SessionID != "session-1" || got.transition.TargetAccountID != "acct-b" || got.bound != "acct-b" {
			t.Fatalf("hook = %+v, want session-1 already bound to acct-b", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the account-moved hook never ran")
	}
}

// inheritAccounts adds the account service's lookup of a non-default account.
type inheritAccounts struct {
	*transitionAccounts
}

func (a inheritAccounts) NonDefaultSessionAccountID(_ context.Context, id domain.SessionID, _ domain.AgentHarness) (string, error) {
	bound := a.boundTo(id)
	if bound == "device-claude" {
		return "", nil
	}
	return bound, nil
}

// A new worker starts on the account its project's live orchestrator moved to.
func TestInheritedSpawnAccountFollowsTheProjectsOrchestrator(t *testing.T) {
	manager, store, _, _, accounts, _ := newAccountTransitionManager(t, domain.SessionModeTUI)
	manager.SetAgentAccounts(inheritAccounts{accounts})
	ctx := context.Background()

	if got := manager.inheritedSpawnAccount(ctx, "proj", domain.HarnessClaudeCode); got != "" {
		t.Fatalf("no orchestrator: account = %q, want none", got)
	}
	addCascadeSession(store, accounts, "orch-old", func(r *domain.SessionRecord) {
		r.Kind = domain.KindOrchestrator
		r.CreatedAt = time.Now().Add(-time.Hour)
	})
	accounts.bound["orch-old"] = "acct-old"
	addCascadeSession(store, accounts, "orch-new", func(r *domain.SessionRecord) {
		r.Kind = domain.KindOrchestrator
		r.CreatedAt = time.Now()
	})
	accounts.bound["orch-new"] = "acct-b"
	if got := manager.inheritedSpawnAccount(ctx, "proj", domain.HarnessClaudeCode); got != "acct-b" {
		t.Fatalf("account = %q, want the newest orchestrator's acct-b", got)
	}

	// On the provider's default there is nothing to inherit.
	accounts.bound["orch-new"] = "device-claude"
	if got := manager.inheritedSpawnAccount(ctx, "proj", domain.HarnessClaudeCode); got != "" {
		t.Fatalf("default account: account = %q, want none", got)
	}
	// A terminated orchestrator does not count.
	accounts.bound["orch-new"] = "acct-b"
	rec := store.sessions["orch-new"]
	rec.IsTerminated = true
	store.sessions["orch-new"] = rec
	if got := manager.inheritedSpawnAccount(ctx, "proj", domain.HarnessClaudeCode); got != "acct-old" {
		t.Fatalf("account = %q, want the live orchestrator's acct-old", got)
	}
	// Another project's orchestrator does not count.
	if got := manager.inheritedSpawnAccount(ctx, "elsewhere", domain.HarnessClaudeCode); got != "" {
		t.Fatalf("other project: account = %q, want none", got)
	}
}
