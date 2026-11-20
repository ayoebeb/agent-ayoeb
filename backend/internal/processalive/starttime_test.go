package processalive

import (
	"errors"
	"testing"
	"time"
)

func TestReused(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)
	tests := []struct {
		name       string
		observed   time.Time
		err        error
		recorded   time.Time
		registered time.Time
		queryable  bool
		want       bool
	}{
		{"same creation time is the host", t0, nil, t0, t0.Add(time.Second), true, false},
		{"different creation time is pid reuse", later, nil, t0, t0.Add(time.Second), true, true},
		{"legacy entry: created after registration is pid reuse", later, nil, time.Time{}, t0, true, true},
		{"legacy entry: created before registration is the host", t0, nil, time.Time{}, t0.Add(time.Second), true, false},
		{"no identity recorded cannot prove reuse", later, nil, time.Time{}, time.Time{}, true, false},
		{"access denied on a foreign process means reuse", time.Time{}, ErrAccessDenied, t0, t0, true, true},
		{"access denied on an elevated host is not reuse", time.Time{}, ErrAccessDenied, t0, t0, false, false},
		{"missing process is left to the liveness probe", time.Time{}, ErrNotFound, t0, t0, true, false},
		{"unknown error is not reuse", time.Time{}, errors.New("boom"), t0, t0, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Reused(tt.observed, tt.err, tt.recorded, tt.registered, tt.queryable); got != tt.want {
				t.Fatalf("Reused = %v, want %v", got, tt.want)
			}
		})
	}
}
