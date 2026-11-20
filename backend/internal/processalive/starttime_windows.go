//go:build windows

package processalive

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

// StartTime returns the kernel creation time of pid. It returns ErrAccessDenied
// when the process exists but the current user cannot query it, and
// ErrNotFound when no such process exists.
func StartTime(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, ErrNotFound
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return time.Time{}, ErrAccessDenied
		case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
			return time.Time{}, ErrNotFound
		}
		return time.Time{}, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}
