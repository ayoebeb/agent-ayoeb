package chat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestChatHandoffTurnsCreditEachProviderAndKeepRequestsSentThroughAO(t *testing.T) {
	marker := func(sequence int64, source, target domain.AgentHarness, branch string) domain.ConversationActivity {
		detail, err := json.Marshal(providerSwitchActivityDetail{
			Event: "provider_switch", SourceHarness: source, TargetHarness: target, BranchID: branch,
		})
		if err != nil {
			t.Fatal(err)
		}
		return domain.ConversationActivity{Sequence: sequence, Kind: domain.ActivityKindSystem, Detail: detail}
	}
	message := func(sequence int64, turn string, role domain.MessageRole, origin domain.MessageOrigin, text string) domain.ConversationMessage {
		return domain.ConversationMessage{Sequence: sequence, TurnID: turn, Role: role, Origin: origin, Text: text}
	}
	turns := []domain.ConversationTurn{
		{ID: "t1", BranchID: "root", State: domain.TurnStateCompleted},
		{ID: "t2", BranchID: "b-claude", State: domain.TurnStateCompleted},
		{ID: "t3", BranchID: "b-claude", State: domain.TurnStateCompleted},
		{ID: "t4", BranchID: "b-kimchi", State: domain.TurnStateCompleted},
		// An edit's branch has no marker of its own.
		{ID: "t5", BranchID: "b-edited", State: domain.TurnStateCompleted},
	}
	messages := []domain.ConversationMessage{
		message(1, "t1", domain.MessageRoleUser, domain.MessageOriginHuman, "typed in the app"),
		message(2, "t1", domain.MessageRoleAssistant, domain.MessageOriginProvider, "codex answered"),
		message(4, "t2", domain.MessageRoleUser, domain.MessageOriginAutomation, "sent with ao send"),
		message(5, "t2", domain.MessageRoleAssistant, domain.MessageOriginProvider, "claude answered"),
		message(6, "t3", domain.MessageRoleUser, domain.MessageOriginDaemon, "review batch from the daemon"),
		message(7, "t3", domain.MessageRoleAssistant, domain.MessageOriginProvider, "claude handled the review"),
		message(9, "t4", domain.MessageRoleUser, domain.MessageOriginHuman, "typed after the second switch"),
		message(10, "t4", domain.MessageRoleAssistant, domain.MessageOriginProvider, "kimchi answered"),
		message(11, "t5", domain.MessageRoleUser, domain.MessageOriginHuman, "an edited message"),
		message(12, "t5", domain.MessageRoleAssistant, domain.MessageOriginProvider, "kimchi answered the edit"),
	}
	activities := []domain.ConversationActivity{
		{Sequence: 2, Kind: domain.ActivityKindSystem, Detail: []byte(`{"event":"something_else"}`)},
		marker(3, domain.HarnessCodex, domain.HarnessClaudeCode, "b-claude"),
		marker(8, domain.HarnessClaudeCode, domain.HarnessKimchi, "b-kimchi"),
	}

	got := chatHandoffTurns(turns, messages, activities)
	want := []domain.HandoffTurn{
		{UserRequest: "typed in the app", AgentResponse: "codex answered", Harness: domain.HarnessCodex},
		{UserRequest: "sent with ao send", AgentResponse: "claude answered", Harness: domain.HarnessClaudeCode},
		{UserRequest: "typed after the second switch", AgentResponse: "kimchi answered", Harness: domain.HarnessKimchi},
		{UserRequest: "an edited message", AgentResponse: "kimchi answered the edit", Harness: domain.HarnessKimchi},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chatHandoffTurns =\n%+v\nwant\n%+v", got, want)
	}

	// A conversation that never switched cannot say who answered.
	unswitched := chatHandoffTurns(turns[:1], messages[:2], nil)
	if len(unswitched) != 1 || unswitched[0].Harness != "" {
		t.Fatalf("unswitched turns = %+v, want one uncredited turn", unswitched)
	}
}

// A session whose first turn died on a usage limit still received its task. The
// history lists that turn with its state and error, so an orchestrator cannot
// read "0 turns" as "never delivered"; a handoff note stays completed turns only.
func TestChatTurnsKeepUnfinishedTurnsForHistoryOnly(t *testing.T) {
	const limit = "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)"
	turns := []domain.ConversationTurn{
		{ID: "t1", State: domain.TurnStateFailed, ErrorMessage: limit},
		{ID: "t2", State: domain.TurnStateRunning},
		{ID: "t3", State: domain.TurnStateInterrupted},
		{ID: "t4", State: domain.TurnStateQueued},
	}
	messages := []domain.ConversationMessage{
		{Sequence: 1, TurnID: "t1", Role: domain.MessageRoleUser, Origin: domain.MessageOriginAutomation, Text: "build the dashboard"},
		{Sequence: 2, TurnID: "t1", Role: domain.MessageRoleAssistant, Origin: domain.MessageOriginProvider, Text: "started on it"},
		{Sequence: 3, TurnID: "t2", Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman, Text: "carry on"},
		{Sequence: 4, TurnID: "t3", Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman, Text: "stop that"},
		{Sequence: 5, TurnID: "t4", Role: domain.MessageRoleUser, Origin: domain.MessageOriginHuman, Text: "not delivered yet"},
	}

	if got := chatHandoffTurns(turns, messages, nil); len(got) != 0 {
		t.Fatalf("a handoff note must carry completed turns only, got %+v", got)
	}
	got := chatTurns(turns, messages, nil, true)
	want := []domain.HandoffTurn{
		{UserRequest: "build the dashboard", AgentResponse: "started on it", State: "failed", Error: limit},
		{UserRequest: "carry on", State: "running"},
		{UserRequest: "stop that", State: "interrupted"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history turns =\n%+v\nwant\n%+v", got, want)
	}
}
