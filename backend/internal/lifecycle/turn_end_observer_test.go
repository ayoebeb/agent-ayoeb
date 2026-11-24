package lifecycle

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type turnEndRecorder struct{ ended []domain.SessionRecord }

func (r *turnEndRecorder) SessionTurnEnded(rec domain.SessionRecord) { r.ended = append(r.ended, rec) }

// Autopilot sends the task again when a turn ends, so every observer must hear
// the moment a session goes from working to idle, once, with the stored state.
func TestTurnEndObserversHearEveryTurnThatEnds(t *testing.T) {
	st := newFakeStore()
	m := New(st, nil)
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	m.clock = func() time.Time { return now }
	first, second := &turnEndRecorder{}, &turnEndRecorder{}
	m.AddTurnEndObserver(first)
	m.AddTurnEndObserver(second)
	m.AddTurnEndObserver(nil) // ignored
	st.sessions["mer-orchestrator-1"] = orchestratorRecord(now, domain.ActivityActive)

	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	for name, rec := range map[string]*turnEndRecorder{"first": first, "second": second} {
		if len(rec.ended) != 1 || rec.ended[0].ID != "mer-orchestrator-1" || rec.ended[0].Activity.State != domain.ActivityIdle {
			t.Fatalf("%s observer heard %+v, want one turn end carrying the idle record", name, rec.ended)
		}
	}

	// Going idle again while already idle is not another turn.
	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	// Starting a turn is not the end of one.
	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: domain.ActivityActive}); err != nil {
		t.Fatal(err)
	}
	if len(first.ended) != 1 {
		t.Fatalf("turn ends = %d after an idle repeat and a turn start, want still 1", len(first.ended))
	}
	if err := m.ApplyActivitySignal(ctx, "mer-orchestrator-1", ports.ActivitySignal{Valid: true, State: domain.ActivityIdle}); err != nil {
		t.Fatal(err)
	}
	if len(first.ended) != 2 {
		t.Fatalf("turn ends = %d, want 2 after the next turn finished", len(first.ended))
	}
}
