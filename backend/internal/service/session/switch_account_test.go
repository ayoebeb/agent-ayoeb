package session

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// accountMover fakes the two manager calls an account switch makes.
type accountMover struct {
	commander
	selfErr     error
	workers     []sessionmanager.WorkerAccountMove
	workersErr  error
	selfPolicy  domain.SessionInterfaceTransitionPolicy
	workerCalls int
}

func (m *accountMover) StartAccountTransition(_ context.Context, id domain.SessionID, _ string, policy domain.SessionInterfaceTransitionPolicy) (domain.SessionInterfaceTransition, error) {
	m.selfPolicy = policy
	if m.selfErr != nil {
		return domain.SessionInterfaceTransition{}, m.selfErr
	}
	return domain.SessionInterfaceTransition{ID: "t-" + string(id), SessionID: id}, nil
}

func (m *accountMover) StartWorkerAccountTransitions(context.Context, domain.SessionID, string, domain.SessionInterfaceTransitionPolicy) ([]sessionmanager.WorkerAccountMove, error) {
	m.workerCalls++
	return m.workers, m.workersErr
}

func movedWorker(id string) sessionmanager.WorkerAccountMove {
	return sessionmanager.WorkerAccountMove{SessionID: domain.SessionID(id), Transition: &domain.SessionInterfaceTransition{ID: "t-" + id, SessionID: domain.SessionID(id)}}
}

// An orchestrator's switch also starts its workers' moves, and the result
// counts the ones that started.
func TestSwitchAccountMovesTheOrchestratorsWorkersToo(t *testing.T) {
	mover := &accountMover{workers: []sessionmanager.WorkerAccountMove{
		movedWorker("w-1"), movedWorker("w-2"),
		{SessionID: "w-3", Skipped: sessionmanager.WorkerMoveSkipAlreadyThere},
		{SessionID: "w-4", Err: errors.New("boom")},
	}}
	svc := &Service{manager: mover}

	result, err := svc.SwitchAccount(context.Background(), "orch", "acct-b", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transition == nil || result.Transition.ID != "t-orch" {
		t.Fatalf("transition = %+v, want the orchestrator's own move", result.Transition)
	}
	if result.MovedWorkers() != 2 || len(result.Workers) != 4 {
		t.Fatalf("workers = %+v, want 2 moved of 4", result.Workers)
	}
	if result.Workers[3].Error == "" || result.Workers[2].Skipped != sessionmanager.WorkerMoveSkipAlreadyThere {
		t.Fatalf("workers = %+v, want the failure and the skip reported", result.Workers)
	}
	if mover.selfPolicy != domain.SessionInterfaceTransitionInterrupt {
		t.Fatalf("policy = %s, want interrupt when stopTurn is set", mover.selfPolicy)
	}
}

func TestSwitchAccountCanLeaveWorkersAlone(t *testing.T) {
	mover := &accountMover{workers: []sessionmanager.WorkerAccountMove{movedWorker("w-1")}}
	no := false
	result, err := (&Service{manager: mover}).SwitchAccount(context.Background(), "orch", "acct-b", false, &no)
	if err != nil || mover.workerCalls != 0 || len(result.Workers) != 0 {
		t.Fatalf("result = %+v, err = %v, worker calls = %d, want workers untouched", result, err, mover.workerCalls)
	}
}

// An orchestrator that already moved (by hand) still brings its workers over:
// the case of an orchestrator moved off a limited account with workers left behind.
func TestSwitchAccountBringsWorkersOverEvenWhenTheOrchestratorAlreadyMoved(t *testing.T) {
	mover := &accountMover{selfErr: ports.ErrAgentAccountUnchanged, workers: []sessionmanager.WorkerAccountMove{movedWorker("w-1")}}
	result, err := (&Service{manager: mover}).SwitchAccount(context.Background(), "orch", "acct-b", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transition != nil || result.MovedWorkers() != 1 {
		t.Fatalf("result = %+v, want no own move and one worker moved", result)
	}
}

// Nothing to move anywhere is still the plain "already on this account" refusal.
func TestSwitchAccountRefusesWhenNothingMoves(t *testing.T) {
	mover := &accountMover{selfErr: ports.ErrAgentAccountUnchanged}
	_, err := (&Service{manager: mover}).SwitchAccount(context.Background(), "worker-1", "acct-b", false, nil)
	if err == nil {
		t.Fatal("an unchanged account with no workers to move was accepted")
	}
}

func TestSwitchAccountReportsOtherFailures(t *testing.T) {
	mover := &accountMover{selfErr: ports.ErrAgentAccountSignedOut, workers: []sessionmanager.WorkerAccountMove{movedWorker("w-1")}}
	if _, err := (&Service{manager: mover}).SwitchAccount(context.Background(), "orch", "acct-b", false, nil); err == nil || mover.workerCalls != 0 {
		t.Fatalf("err = %v, worker calls = %d, want the refusal and no worker moves", err, mover.workerCalls)
	}
}
