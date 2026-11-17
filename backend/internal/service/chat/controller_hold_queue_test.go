package chat_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// assertTurnStaysQueued fails if the named turn leaves queued within a short settle
// window. A drain that should not have run would move it to running almost at once.
func assertTurnStaysQueued(t *testing.T, h *harness, text string) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		snapshot, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		if got := turnStateByText(t, snapshot)[text]; got != domain.TurnStateQueued {
			t.Fatalf("queued turn %q became %q; a non-success terminal turn must hold the queue", text, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A failed primary turn must not release queued work into the same outage; only a
// completed turn may. Regression for issue #4861.
func TestFailedPrimaryTurnHoldsQueuedWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "automation", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue automation: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateFailed,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateFailed
	})

	assertTurnStaysQueued(t, h, "automation")
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("provider received %v, want only the root prompt", got)
	}
}

// A recovered turn is terminal but carries no portable outcome, so it is no proof
// the next turn will succeed. It holds the queue like a failed turn.
func TestRecoveredPrimaryTurnHoldsQueuedWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "automation", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue automation: %v", err)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateRecovered,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRecovered
	})

	assertTurnStaysQueued(t, h, "automation")
	if got := h.conv.sentTexts(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("provider received %v, want only the root prompt", got)
	}
}

// A failed turn (a usage-limit stop, say) holds the queue until something moves
// it. The usage-limit watcher moves it with one "continue" send: that send must
// start the work already queued (the two go out together, oldest first) rather
// than only adding itself behind it, or the queue would sit for as long as
// nobody wrote to the session.
func TestSendAfterAFailedTurnReleasesTheWorkQueuedBehindIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "root", ClientMessageID: "c1", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("send root: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "orchestrator follow-up", ClientMessageID: "c2", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("queue the follow-up: %v", err)
	}
	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
		TurnState: domain.TurnStateFailed,
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["root"] == domain.TurnStateFailed
	})
	assertTurnStaysQueued(t, h, "orchestrator follow-up")

	// The watcher's resume.
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{
		Text: "continue where you left off", ClientMessageID: "usage-limit-resume:turn-1", Origin: domain.MessageOriginAutomation,
	}); err != nil {
		t.Fatalf("resume send: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		states := turnStateByText(t, s)
		return states["orchestrator follow-up"] != domain.TurnStateQueued && states["continue where you left off"] != domain.TurnStateQueued
	})
	// The two queued messages go to the provider together, in enqueue order, as one turn.
	got := h.conv.sentTexts()
	if len(got) != 2 || got[0] != "root" {
		t.Fatalf("provider received %v, want the root prompt and then one batch", got)
	}
	followUp := strings.Index(got[1], "orchestrator follow-up")
	resume := strings.Index(got[1], "continue where you left off")
	if followUp < 0 || resume < 0 || followUp > resume {
		t.Fatalf("the released batch must hold the queued follow-up and then the resume message: %q", got[1])
	}
}
