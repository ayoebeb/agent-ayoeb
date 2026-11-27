package usagelimit

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
)

type fakePolicy struct{ covered, switches bool }

func (f fakePolicy) UsageLimit(context.Context, domain.SessionID) (bool, bool) {
	return f.covered, f.switches
}

func policyHarness(t *testing.T, p AutopilotPolicy) (*harness, *fakeMover, *[]string) {
	t.Helper()
	h := newHarness(t)
	h.store.addStop("orch", domain.KindOrchestrator, limitMessage)
	h.store.addStop("w1", domain.KindWorker, limitMessage)
	lister := &fakeAccountLister{views: []agentaccounts.AccountView{
		claudeView("acct-1", "limited@example.com", true, 100, 40),
		claudeView("acct-light", "light@example.com", true, 10, 20),
	}}
	mover := &fakeMover{errs: map[domain.SessionID]error{}}
	h.svc.lister, h.svc.mover = lister, mover
	var accounts []string
	mover.finish = func(id domain.SessionID, accountID string) {
		accounts = append(accounts, accountID)
		if _, err := h.svc.ResumeAfterAccountMove(context.Background(), id, accountID); err != nil {
			t.Errorf("resume %s: %v", id, err)
		}
	}
	if p != nil {
		h.svc.SetAutopilotPolicy(p)
	}
	h.sweep()
	return h, mover, &accounts
}

// Without Autopilot nothing changes: the owner gets the switch prompt.
func TestWithoutAutopilotTheOwnerIsStillAsked(t *testing.T) {
	h, mover, _ := policyHarness(t, fakePolicy{})
	if prompts, _ := h.svc.ListPrompts(context.Background()); len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	if len(mover.moved) != 0 {
		t.Fatalf("moved = %v, want nothing", mover.moved)
	}
}

// Under Autopilot the default is to wait for the reset: no prompt is raised, the
// notification does not point at a switch, and no session moves to another account.
func TestAutopilotWaitsForTheResetByDefault(t *testing.T) {
	h, mover, _ := policyHarness(t, fakePolicy{covered: true})
	if prompts, _ := h.svc.ListPrompts(context.Background()); len(prompts) != 0 {
		t.Fatalf("prompts = %+v, want none while Autopilot waits", prompts)
	}
	if len(mover.moved) != 0 {
		t.Fatalf("moved = %v: never move to another account unless the profile says so", mover.moved)
	}
	intent := h.notifier.stored["usage_limit:"+domain.UsageLimitWindowKey("acct-1", &resetAt, failedAt)]
	if intent.UsageLimitSwitchPrompt {
		t.Fatalf("notification = %+v, must not offer a switch", intent)
	}
	if len(h.store.stops) == 0 {
		t.Fatal("the stop must still be recorded so the reset resumes the session")
	}
}

// A profile that clearly allows it moves the sessions to the account with the
// most room, with no question to the owner.
func TestAutopilotSwitchesOnlyWhenTheProfileSaysSo(t *testing.T) {
	h, mover, accounts := policyHarness(t, fakePolicy{covered: true, switches: true})
	// Every stopped session moves, not only the first one of the window.
	if len(mover.moved) != 2 {
		t.Fatalf("moved = %v, want both stopped sessions", mover.moved)
	}
	for _, a := range *accounts {
		if a != "acct-light" {
			t.Fatalf("moved to %q, want the account with the most room", a)
		}
	}
	if prompts, _ := h.svc.ListPrompts(context.Background()); len(prompts) != 0 {
		t.Fatalf("prompts = %+v, want no prompt for covered sessions", prompts)
	}
}

// sessionPolicy covers only the named sessions.
type sessionPolicy map[domain.SessionID]bool

func (p sessionPolicy) UsageLimit(_ context.Context, id domain.SessionID) (bool, bool) {
	return p[id], p[id]
}

// A session driven by hand that stopped on the same account limit is neither
// moved nor answered for: its prompt stays open for the owner.
func TestAutopilotMovesOnlyTheSessionsItCovers(t *testing.T) {
	for name, covered := range map[string]sessionPolicy{
		"covered one detected first":  {"orch": true},
		"covered one detected second": {"w1": true},
	} {
		t.Run(name, func(t *testing.T) {
			h, mover, _ := policyHarness(t, covered)
			if len(mover.moved) != 1 || !covered[mover.moved[0]] {
				t.Fatalf("moved = %v, want only the covered session of %v", mover.moved, covered)
			}
			prompts, err := h.svc.ListPrompts(context.Background())
			if err != nil || len(prompts) != 1 {
				t.Fatalf("prompts = %+v, %v: the uncovered session keeps its question", prompts, err)
			}
			if len(prompts[0].Sessions) != 1 || covered[prompts[0].Sessions[0].ID] {
				t.Fatalf("prompt sessions = %+v, want only the uncovered one", prompts[0].Sessions)
			}
		})
	}
}
