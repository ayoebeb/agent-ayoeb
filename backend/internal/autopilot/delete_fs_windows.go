//go:build windows

package autopilot

import (
	"io/fs"
	"syscall"
)

// isReparsePoint reports a Windows reparse point: a junction, a symbolic link, a
// mount point or a cloud placeholder. Any of them may lead outside the workspace.
func isReparsePoint(info fs.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		// Attributes that cannot be read are treated as a reparse point.
		return true
	}
	return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
