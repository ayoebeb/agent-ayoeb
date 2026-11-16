package domain

import (
	"testing"
	"time"
)

func TestUsageLimitStopMessageNamesTheWindow(t *testing.T) {
	for _, tc := range []struct {
		message string
		window  string
		ok      bool
	}{
		{"You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)", "5-hour", true},
		{"You've hit your weekly limit · resets Mon 9am (UTC)", "weekly", true},
		{"You've hit your Opus weekly limit", "Opus weekly", true},
		{"You’ve reached your usage limit", "usage", true},
		{"You've hit your usage limit.", "usage", true},
		{"Usage limit reached", "", true},
		{"controller ended before the turn completed", "", false},
		{"I hit your 5-hour limit in the tests", "", false},
	} {
		window, ok := UsageLimitStopMessage(tc.message)
		if window != tc.window || ok != tc.ok {
			t.Errorf("UsageLimitStopMessage(%q) = %q, %v; want %q, %v", tc.message, window, ok, tc.window, tc.ok)
		}
	}
}

func TestUsageLimitWindowKeyGroupsSessionsOfOneWindow(t *testing.T) {
	reset := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)
	nearlySame := reset.Add(20 * time.Second)
	later := reset.Add(5 * time.Hour)
	stopped := reset.Add(-90 * time.Minute)
	if UsageLimitWindowKey("a", &reset, stopped) != UsageLimitWindowKey("a", &nearlySame, stopped) {
		t.Error("resets seconds apart must share a window")
	}
	if UsageLimitWindowKey("a", &reset, stopped) == UsageLimitWindowKey("b", &reset, stopped) {
		t.Error("two accounts must not share a window")
	}
	if UsageLimitWindowKey("a", &reset, stopped) == UsageLimitWindowKey("a", &later, stopped) {
		t.Error("two windows of one account must differ")
	}
	if UsageLimitWindowKey("a", nil, stopped) != UsageLimitWindowKey("a", nil, stopped.Add(10*time.Minute)) {
		t.Error("stops with no reset time in the same hour must share a window")
	}
}

func TestBuildSessionUsageLimit(t *testing.T) {
	failed := time.Date(2026, 11, 16, 14, 20, 0, 0, time.UTC) // 15:20 in Amsterdam
	reset := time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC)   // 6pm there
	turn := func(edit func(*LimitStopTurn)) LimitStopTurn {
		turn := LimitStopTurn{
			SessionID: "s", TurnID: "t", ErrorMessage: "You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)",
			CompletedAt: &failed, RequestedAt: failed.Add(-time.Hour),
		}
		if edit != nil {
			edit(&turn)
		}
		return turn
	}
	before := failed.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		turn   LimitStopTurn
		now    time.Time
		resume UsageLimitResume
		reason string
	}{
		{"scheduled before the reset", turn(nil), before, UsageLimitResumeScheduled, ""},
		{"waiting once the reset passed", turn(nil), reset.Add(time.Minute), UsageLimitResumeWaiting, ""},
		{"pending stop is scheduled", turn(func(t *LimitStopTurn) { t.ResumeState = UsageLimitStopPending }), before, UsageLimitResumeScheduled, ""},
		{"setting off", turn(func(t *LimitStopTurn) { t.ResumeState, t.SkipReason = UsageLimitStopSkipped, UsageLimitOffDisabled }), before, UsageLimitResumeOff, UsageLimitOffDisabled},
		{"cancelled", turn(func(t *LimitStopTurn) { t.ResumeState, t.SkipReason = UsageLimitStopSkipped, UsageLimitOffCancelled }), before, UsageLimitResumeOff, UsageLimitOffCancelled},
		{"a retry of the turn is armed", turn(func(t *LimitStopTurn) { t.RetryArmed = true }), before, UsageLimitResumeOff, UsageLimitOffRetryArmed},
		{"no reset time", turn(func(t *LimitStopTurn) { t.ErrorMessage = "You've hit your usage limit." }), before, UsageLimitResumeOff, UsageLimitOffUnknownReset},
		{"too old to act on", turn(nil), failed.Add(UsageLimitResumeHorizon + time.Hour), UsageLimitResumeOff, UsageLimitOffExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSessionUsageLimit(tc.turn, tc.now)
			if got == nil || got.Resume != tc.resume || got.ResumeOffReason != tc.reason {
				t.Fatalf("got %+v, want resume %q reason %q", got, tc.resume, tc.reason)
			}
		})
	}

	got := BuildSessionUsageLimit(turn(nil), before)
	if got.Window != "5-hour" || got.ResetsAt == nil || !got.ResetsAt.Equal(reset) || got.TurnID != "t" || !got.StoppedAt.Equal(failed) {
		t.Fatalf("fields = %+v", got)
	}
	// The reset time the provider driver stored wins over the one parsed from the message.
	soon := reset.Add(time.Hour)
	if got := BuildSessionUsageLimit(turn(func(t *LimitStopTurn) { t.StoredResetsAt = &soon }), before); got.ResetsAt == nil || !got.ResetsAt.Equal(soon) {
		t.Fatalf("stored reset ignored: %+v", got)
	}
	// A failure that is not a usage limit is not one.
	if BuildSessionUsageLimit(turn(func(t *LimitStopTurn) { t.ErrorMessage = "controller ended before the turn completed" }), before) != nil {
		t.Fatal("a crash read as a usage limit")
	}
	// A person stopping or moving on from a session leaves no resume to show.
	stopped := turn(func(t *LimitStopTurn) { t.ResumeState, t.SkipReason = UsageLimitStopSkipped, UsageLimitSkipStopped })
	if got := BuildSessionUsageLimit(stopped, before); got.Resume != UsageLimitResumeScheduled {
		t.Fatalf("a stopped-session skip must not be shown as a reason: %+v", got)
	}
}
