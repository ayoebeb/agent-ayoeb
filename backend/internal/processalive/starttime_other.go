//go:build !windows

package processalive

import "time"

// StartTime is not tracked on this platform: callers fall back to plain
// liveness. It returns ErrNotFound only when pid is not positive.
func StartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, ErrNotFound
	}
	return time.Time{}, ErrUnsupported
}
