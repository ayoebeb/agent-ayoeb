package agentaccounts

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func limitsAt(fiveHour, sevenDay float64, fiveHourResets time.Time) *domain.AgentAccountLimits {
	return &domain.AgentAccountLimits{Windows: []domain.AgentAccountLimitWindow{
		{ID: FiveHourWindowID, Label: "Session (5h)", UsedPercent: fiveHour, ResetsAt: &fiveHourResets},
		{ID: "seven_day", Label: "Week", UsedPercent: sevenDay},
	}}
}

func signedIn(limits *domain.AgentAccountLimits) *reading {
	return &reading{
		identity: domain.AgentAccountIdentityCheck{SignedIn: true, LoggedIn: true},
		limits:   limits, at: time.Now(),
	}
}

// newHeadroomService has the device account plus three more, each with a reading.
func newHeadroomService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	store := &memStore{defaults: map[domain.AgentAccountProvider]string{}, bindings: map[domain.SessionID]string{}}
	root := t.TempDir()
	svc := New(store, []ports.AgentAccountProviderCLI{&fakeClaude{signedInAs: map[string]string{}}}, root, fakeTerminals{}, nil)
	for _, id := range []string{"acct-a", "acct-b", "acct-c"} {
		store.accounts = append(store.accounts, domain.AgentAccount{ID: id, Provider: domain.AgentAccountProviderClaude, Email: id + "@example.com"})
	}
	return svc, store
}

func TestHasHeadroomOnlyFalseForAFullWindow(t *testing.T) {
	svc, _ := newHeadroomService(t)
	later := time.Now().Add(time.Hour)
	svc.readings = map[string]*reading{
		"acct-a": signedIn(limitsAt(100, 10, later)),
		"acct-b": signedIn(limitsAt(30, 10, later)),
		"acct-c": signedIn(limitsAt(100, 10, time.Now().Add(-time.Minute))), // already reset
	}
	ctx := context.Background()
	if svc.HasHeadroom(ctx, "acct-a") {
		t.Fatal("a full 5h window reads as having room")
	}
	if !svc.HasHeadroom(ctx, "acct-b") || !svc.HasHeadroom(ctx, "acct-c") || !svc.HasHeadroom(ctx, "never-read") {
		t.Fatal("an account with room, a window that already reset, or no reading reads as full")
	}
}

func TestMostHeadroomAccountPicksTheLeastUsedFiveHourWindow(t *testing.T) {
	svc, _ := newHeadroomService(t)
	later := time.Now().Add(time.Hour)
	svc.readings = map[string]*reading{
		domain.DeviceAgentAccountID(domain.AgentAccountProviderClaude): signedIn(limitsAt(90, 10, later)),
		"acct-a": signedIn(limitsAt(100, 10, later)), // full
		"acct-b": signedIn(limitsAt(40, 90, later)),  // 40% used
		"acct-c": signedIn(limitsAt(15, 20, later)),  // 15% used: the best
	}
	ctx := context.Background()
	if got := svc.MostHeadroomAccount(ctx, domain.HarnessClaudeCode, ""); got != "acct-c" {
		t.Fatalf("best = %q, want acct-c", got)
	}
	if got := svc.MostHeadroomAccount(ctx, domain.HarnessClaudeCode, "acct-c"); got != "acct-b" {
		t.Fatalf("best excluding acct-c = %q, want acct-b", got)
	}
	// An account that is signed out is never picked.
	svc.readings["acct-c"].identity.SignedIn = false
	if got := svc.MostHeadroomAccount(ctx, domain.HarnessClaudeCode, ""); got != "acct-b" {
		t.Fatalf("best with acct-c signed out = %q, want acct-b", got)
	}
	// A harness without accounts has none to pick.
	if got := svc.MostHeadroomAccount(ctx, domain.HarnessAider, ""); got != "" {
		t.Fatalf("aider account = %q, want none", got)
	}
}

// A worker spawned next to an orchestrator inherits its pinned account, unless
// that is the default account.
func TestNonDefaultSessionAccountID(t *testing.T) {
	svc, store := newHeadroomService(t)
	ctx := context.Background()
	store.defaults[domain.AgentAccountProviderClaude] = "acct-a"

	if got, err := svc.NonDefaultSessionAccountID(ctx, "orch", domain.HarnessClaudeCode); err != nil || got != "" {
		t.Fatalf("unpinned = %q, %v, want none", got, err)
	}
	store.bindings["orch"] = "acct-a"
	if got, _ := svc.NonDefaultSessionAccountID(ctx, "orch", domain.HarnessClaudeCode); got != "" {
		t.Fatalf("pinned to the default = %q, want none", got)
	}
	store.bindings["orch"] = "acct-b"
	if got, err := svc.NonDefaultSessionAccountID(ctx, "orch", domain.HarnessClaudeCode); err != nil || got != "acct-b" {
		t.Fatalf("pinned to acct-b = %q, %v, want acct-b", got, err)
	}
	if got, _ := svc.NonDefaultSessionAccountID(ctx, "orch", domain.HarnessAider); got != "" {
		t.Fatalf("harness without accounts = %q, want none", got)
	}
}
