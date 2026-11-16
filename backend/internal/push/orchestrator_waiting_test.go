package push

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func waitingRecord() domain.NotificationRecord {
	return domain.NotificationRecord{
		ID: "ntf_1", SessionID: "mer-orchestrator-1", ProjectID: "mer", Type: domain.NotificationOrchestratorWaiting,
		Title: "Mercury: orchestrator wacht op u", Body: "Merge PR 42 now, or wait?",
	}
}

// The phone banner for an orchestrator that is waiting carries the project in
// the title and the question as the body, and the data blob routes a tap to the
// orchestrator's session.
func TestDispatcherPushesAnOrchestratorWait(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{{Token: "ExponentPushToken[a]"}}}
	sender := newFakeSender([]Ticket{{Status: "ok"}})
	d := NewDispatcher(sub, store, sender, "h_machine_a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- created(waitingRecord())
	sender.waitSent(t)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	m := sender.gotMsgs[0]
	if m.Title != "h_machine_ · Mercury: orchestrator wacht op u" || m.Body != "Merge PR 42 now, or wait?" {
		t.Fatalf("copy = %+v", m)
	}
	if m.Priority != "high" || m.Sound != "default" {
		t.Fatalf("an orchestrator wait must be a high-priority push with sound: %+v", m)
	}
	if m.Data["type"] != "orchestrator_waiting" || m.Data["sessionId"] != "mer-orchestrator-1" || m.Data["projectId"] != "mer" {
		t.Fatalf("data blob = %+v", m.Data)
	}
}

// The single reminder reaches the phone like the first announcement did.
func TestDispatcherPushesTheReminder(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{{Token: "ExponentPushToken[a]"}}}
	sender := newFakeSender([]Ticket{{Status: "ok"}})
	d := NewDispatcher(sub, store, sender, "h_machine_a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- domain.NotificationEvent{Kind: domain.NotificationReminder, Record: waitingRecord()}
	sender.waitSent(t)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.gotMsgs) != 1 || sender.gotMsgs[0].Data["notificationId"] != "ntf_1" {
		t.Fatalf("messages = %+v", sender.gotMsgs)
	}
}
