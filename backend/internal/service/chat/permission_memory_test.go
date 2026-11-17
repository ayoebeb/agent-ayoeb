package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type rememberCall struct {
	project domain.ProjectID
	session domain.SessionID
	raw     string
}

type recordingPermissionMemory struct {
	calls []rememberCall
	err   error
}

func (r *recordingPermissionMemory) RememberDecision(_ context.Context, project domain.ProjectID, session domain.SessionID, raw []byte) error {
	r.calls = append(r.calls, rememberCall{project, session, string(raw)})
	return r.err
}

func memoryController(memory PermissionMemory, harness domain.AgentHarness) *Controller {
	return &Controller{
		sessionID: "mer-3", harness: harness, permissionMemory: memory,
		conversation: domain.ConversationRecord{ProjectID: "mer"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func approvalWithChoices(requestID string) ports.ChatEvent {
	return ports.ChatEvent{
		Kind: ports.ChatEventApprovalRequested, RequestID: requestID,
		Decisions: []ports.ChatDecisionOption{
			{ID: "reject", Kind: ports.ChatDecisionRejectOnce, Raw: []byte(`{"optionId":"reject"}`)},
			{ID: "allow", Kind: ports.ChatDecisionAllowOnce, Raw: []byte(`{"optionId":"allow"}`)},
			{ID: "allow_always", Kind: ports.ChatDecisionAllowAlways, Raw: []byte(`{"optionId":"allow_always"}`)},
			{ID: "allow_always_class", Kind: ports.ChatDecisionAllowAlways, Raw: []byte(`{"optionId":"allow_always_class"}`)},
			{ID: "no_raw", Kind: ports.ChatDecisionAllowAlways},
		},
	}
}

func TestOnlyAnAlwaysChoiceIsRememberedForTheProject(t *testing.T) {
	memory := &recordingPermissionMemory{}
	c := memoryController(memory, domain.HarnessClaudeCode)
	c.noteRememberableDecisions(approvalWithChoices("req-1"))

	for _, id := range []string{"reject", "allow", "no_raw", "unknown"} {
		if raw := c.chosenDecisionRaw("req-1", id); raw != nil {
			t.Errorf("choice %q would be remembered: %s", id, raw)
		}
	}
	raw := c.chosenDecisionRaw("req-1", "allow_always_class")
	if string(raw) != `{"optionId":"allow_always_class"}` {
		t.Fatalf("raw of the opt-in class choice = %q", raw)
	}
	c.rememberChosenDecision(context.Background(), raw)
	want := []rememberCall{{project: "mer", session: "mer-3", raw: `{"optionId":"allow_always_class"}`}}
	if !reflect.DeepEqual(memory.calls, want) {
		t.Fatalf("remembered %+v, want %+v", memory.calls, want)
	}
}

// The provider's own "resolved" event reaches the controller while Resolve is
// still returning; the choice must have been read before it can drop the options.
func TestAChoiceReadBeforeTheProviderIsToldSurvivesItsResolvedEvent(t *testing.T) {
	memory := &recordingPermissionMemory{}
	c := memoryController(memory, domain.HarnessClaudeCode)
	c.noteRememberableDecisions(approvalWithChoices("req-1"))

	raw := c.chosenDecisionRaw("req-1", "allow_always")
	c.forgetRememberableDecisions("req-1")
	c.rememberChosenDecision(context.Background(), raw)

	if len(memory.calls) != 1 {
		t.Fatalf("remembered %+v, want the choice read before the resolved event", memory.calls)
	}
	if again := c.chosenDecisionRaw("req-1", "allow_always"); again != nil {
		t.Fatalf("options of a finished approval were kept: %s", again)
	}
}

func TestNothingIsNotedForOtherHarnessesOrWithoutMemory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness domain.AgentHarness
		memory  PermissionMemory
	}{
		{"codex", domain.HarnessCodex, &recordingPermissionMemory{}},
		{"no memory", domain.HarnessClaudeCode, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := memoryController(tc.memory, tc.harness)
			c.noteRememberableDecisions(approvalWithChoices("req-1"))
			if raw := c.chosenDecisionRaw("req-1", "allow_always"); raw != nil {
				t.Fatalf("options were kept: %s", raw)
			}
			// Remembering with nothing noted, or with no memory, is a quiet no-op.
			c.rememberChosenDecision(context.Background(), []byte(`{}`))
		})
	}
}

func TestAFailureToRememberNeverBreaksTheDecision(t *testing.T) {
	memory := &recordingPermissionMemory{err: errors.New("database is locked")}
	c := memoryController(memory, domain.HarnessClaudeCode)
	c.noteRememberableDecisions(approvalWithChoices("req-1"))
	// rememberChosenDecision has no error to return: Resolve goes on to record the
	// approval, and the rule still holds in the workspace it was chosen in.
	c.rememberChosenDecision(context.Background(), c.chosenDecisionRaw("req-1", "allow_always"))
	if len(memory.calls) != 1 {
		t.Fatalf("calls = %+v, want one attempt", memory.calls)
	}
}
