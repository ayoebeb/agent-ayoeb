//go:build !windows

package autopilot

import "io/fs"

// isReparsePoint is only meaningful on Windows; symbolic links are caught by mode.
func isReparsePoint(fs.FileInfo) bool { return false }
