//go:build !windows

package shellterm

import "os/exec"

// setRawCommandLine is Windows-only: elsewhere argv reaches the shell as is.
func setRawCommandLine(*exec.Cmd, string) {}
