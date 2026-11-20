package processalive

import (
	"errors"
	"time"
)

var (
	// ErrAccessDenied means the process exists but cannot be queried by this user.
	ErrAccessDenied = errors.New("processalive: access denied")
	// ErrNotFound means no process has that pid.
	ErrNotFound = errors.New("processalive: process not found")
	// ErrUnsupported means start times are not available on this platform.
	ErrUnsupported = errors.New("processalive: start time unsupported")
)

// Reused decides, from a StartTime probe result, whether a recorded pid now
// identifies a different process than the one that was started. observed and
// err are StartTime's results. recorded is the creation time captured at
// spawn (zero when unknown, e.g. entries written by older builds) and
// registeredAt is when the owner was registered (the process is always spawned
// before that). mustBeQueryable is true for a process this user must be able
// to query, so ErrAccessDenied proves the pid belongs to someone else.
//
// Gone or unknown (other errors) is not "reused": plain liveness owns that.
func Reused(observed time.Time, err error, recorded, registeredAt time.Time, mustBeQueryable bool) bool {
	if err != nil {
		return errors.Is(err, ErrAccessDenied) && mustBeQueryable
	}
	if !recorded.IsZero() {
		return !observed.Equal(recorded)
	}
	if !registeredAt.IsZero() {
		return observed.After(registeredAt.Add(time.Second))
	}
	return false
}
