package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeLimitReader struct {
	turns []domain.LimitStopTurn
	err   error
}

func (f fakeLimitReader) ListLatestFailedTurns(context.Context, []domain.SessionID) ([]domain.LimitStopTurn, error) {
	return f.turns, f.err
}

func TestAutopilotUsageLimitedHoldsOnlyWhileTheLimitIsActive(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	limitTurn := func(resets *time.Time) domain.LimitStopTurn {
		stopped := now.Add(-time.Minute)
		return domain.LimitStopTurn{
			SessionID: "s1", TurnID: "t1", CompletedAt: &stopped, StoredResetsAt: resets,
			ErrorMessage: "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)",
		}
	}
	later, earlier := now.Add(time.Hour), now.Add(-time.Minute)
	cases := []struct {
		name   string
		reader fakeLimitReader
		want   bool
		wantEr bool
	}{
		{"no failed turn", fakeLimitReader{}, false, false},
		{"another failure", fakeLimitReader{turns: []domain.LimitStopTurn{{SessionID: "s1", ErrorMessage: "boom"}}}, false, false},
		{"limit that has not reset", fakeLimitReader{turns: []domain.LimitStopTurn{limitTurn(&later)}}, true, false},
		{"limit that already reset", fakeLimitReader{turns: []domain.LimitStopTurn{limitTurn(&earlier)}}, false, false},
		{"unreadable store fails open", fakeLimitReader{err: errors.New("db locked")}, false, true},
	}
	for _, c := range cases {
		got, err := autopilotUsageLimited(c.reader, clock)(context.Background(), "s1")
		if got != c.want || (err != nil) != c.wantEr {
			t.Errorf("%s: limited=%v err=%v, want %v (err %v)", c.name, got, err, c.want, c.wantEr)
		}
	}
}
