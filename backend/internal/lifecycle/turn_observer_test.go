package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recordingTurnObserver struct {
	messages []time.Time
	idles    []string
}

func (o *recordingTurnObserver) SessionMessageReceived(_ domain.SessionID, at time.Time) {
	o.messages = append(o.messages, at)
}

func (o *recordingTurnObserver) SessionTurnIdle(_ domain.SessionRecord, event, final string) {
	o.idles = append(o.idles, event+"|"+final)
}

// Terminal and Chat workers reach the observer through the same reducer: a
// message that starts a turn, then the running turn ending idle.
func TestTurnObserverHearsMessageAndTurnEndForTerminalAndChat(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	for name, c := range map[string]struct {
		mode       domain.SessionMode
		gen        string
		start, end string
	}{
		"terminal": {domain.SessionModeTUI, "", "user-prompt-submit", "stop"},
		"chat":     {domain.SessionModeChat, "gen-1", "chat.turn.started", "chat.turn.completed"},
	} {
		t.Run(name, func(t *testing.T) {
			st := newFakeStore()
			m := New(st, nil)
			m.clock = func() time.Time { return now }
			obs := &recordingTurnObserver{}
			m.SetTurnObserver(obs)
			st.sessions["w-1"] = domain.SessionRecord{
				ID: "w-1", ProjectID: "p", Kind: domain.KindWorker, Mode: c.mode,
				Metadata:      domain.SessionMetadata{ControllerGeneration: c.gen},
				Activity:      domain.Activity{State: domain.ActivityIdle, LastActivityAt: now.Add(-time.Hour)},
				FirstSignalAt: now.Add(-time.Hour),
			}
			signal := func(state domain.ActivityState, event, final string, at time.Time) {
				t.Helper()
				err := m.ApplyActivitySignal(ctx, "w-1", ports.ActivitySignal{
					Valid: true, State: state, Event: event, Timestamp: at,
					ControllerGeneration: c.gen, FinalAssistantMessage: final,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			signal(domain.ActivityActive, c.start, "", now.Add(time.Minute))
			signal(domain.ActivityIdle, c.end, "all done", now.Add(2*time.Minute))

			if len(obs.messages) != 1 || !obs.messages[0].Equal(now.Add(time.Minute)) {
				t.Fatalf("messages = %v", obs.messages)
			}
			if len(obs.idles) != 1 || obs.idles[0] != c.end+"|all done" {
				t.Fatalf("idles = %v", obs.idles)
			}
		})
	}
}

// A pause for input is not a turn end, and idle that was not preceded by a
// running turn is not one either.
func TestTurnObserverIgnoresNeedsInputAndIdleWithoutATurn(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	st := newFakeStore()
	m := New(st, nil)
	m.clock = func() time.Time { return now }
	obs := &recordingTurnObserver{}
	m.SetTurnObserver(obs)
	st.sessions["w-1"] = domain.SessionRecord{
		ID: "w-1", ProjectID: "p", Kind: domain.KindWorker,
		Activity:      domain.Activity{State: domain.ActivityActive, LastActivityAt: now.Add(-time.Minute)},
		FirstSignalAt: now.Add(-time.Minute),
	}
	apply := func(state domain.ActivityState) {
		t.Helper()
		if err := m.ApplyActivitySignal(ctx, "w-1", ports.ActivitySignal{Valid: true, State: state, Timestamp: now}); err != nil {
			t.Fatal(err)
		}
	}
	apply(domain.ActivityWaitingInput)
	apply(domain.ActivityIdle) // from waiting_input, not from a running turn
	if len(obs.idles) != 0 {
		t.Fatalf("idles = %v, want none", obs.idles)
	}
}
