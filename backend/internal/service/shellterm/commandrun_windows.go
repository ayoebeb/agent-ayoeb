//go:build windows

package shellterm

import (
	"os/exec"
	"syscall"
)

// setRawCommandLine hands cmd.exe its command line verbatim. cmd does not
// understand the backslash-escaped quotes Go writes for argv, so a command
// containing quotes would otherwise reach it mangled.
func setRawCommandLine(cmd *exec.Cmd, line string) {
	if line == "" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = line
}
