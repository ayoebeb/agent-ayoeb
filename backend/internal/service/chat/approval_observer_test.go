package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

type approvalObserverFunc func(context.Context, domain.SessionID, chatsvc.ApprovalNotice)

func (f approvalObserverFunc) ApprovalRequested(ctx context.Context, id domain.SessionID, n chatsvc.ApprovalNotice) {
	f(ctx, id, n)
}

// Autopilot answers a permission request by policy, from the observer, so the
// observer must hear the stored request with its tool payload and the decisions
// on offer, and must be able to resolve it through the service without
// deadlocking the event goroutine that told it.
func TestApprovalObserverHearsAStoredRequestAndCanResolveIt(t *testing.T) {
	ctx := context.Background()
	heard := make(chan chatsvc.ApprovalNotice, 1)
	resolved := make(chan error, 1)
	var h *harness
	harnessApprovalObserver = approvalObserverFunc(func(ctx context.Context, id domain.SessionID, n chatsvc.ApprovalNotice) {
		if id != testSession {
			t.Errorf("observer told session %q, want %q", id, testSession)
		}
		heard <- n
		resolved <- h.svc.Resolve(ctx, id, n.RequestID, ports.ChatDecision{ID: "accept"})
	})
	t.Cleanup(func() { harnessApprovalObserver = nil })
	h = newHarness(t)
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "go", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventApprovalRequested, ProviderTurnID: "provider-turn-1",
		ProviderItemID: "item-1", RequestID: "approval-1", Summary: "Run go test",
		Detail: []byte(`{"providerToolName":"Bash","toolKind":"execute","input":{"command":"go test ./..."}}`),
		Decisions: []ports.ChatDecisionOption{
			{ID: "accept", Label: "Approve", Kind: ports.ChatDecisionAllowOnce},
			{ID: "decline", Label: "Reject", Kind: ports.ChatDecisionRejectOnce},
		},
	})

	select {
	case n := <-heard:
		if n.RequestID != "approval-1" || n.Summary != "Run go test" || len(n.Decisions) != 2 {
			t.Fatalf("notice = %+v", n)
		}
		if len(n.Detail) == 0 || !containsAll(string(n.Detail), `"providerToolName":"Bash"`, `go test ./...`, `"decisions"`) {
			t.Fatalf("detail = %s, want the tool payload with the decisions merged in", n.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the observer never heard the request")
	}
	select {
	case err := <-resolved:
		if err != nil {
			t.Fatalf("resolve from the observer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolving from the observer deadlocked")
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Activities) == 1 && s.Activities[0].Status == domain.ActivityStatusResolved
	})
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
