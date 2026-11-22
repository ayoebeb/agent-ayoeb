//go:build windows

package previewserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPTYArgvQuotesBatchShimForCmdExe(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "dev server.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\nsetlocal enabledelayedexpansion\r\nset \"a=%~1\"\r\nset \"b=%~2\"\r\necho first=!a!\r\necho second=!b!\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"run dev", "a&b"}
	argv := ptyArgv(shim, args)
	if len(argv) != 5 || argv[1] != "/d" || argv[2] != "/s" || argv[3] != "/c" {
		t.Fatalf("argv = %q, want [cmd /d /s /c <line>]", argv)
	}
	if want := windowsBatchCommandLine(shim, args); argv[4] != want {
		t.Fatalf("command line = %q, want %q", argv[4], want)
	}

	// The ConPTY host launches this argv with the line as the verbatim process
	// command line (conpty.cmdExeRawLine). Prove cmd.exe runs it intact: a spaced
	// shim path, a spaced argument, and a literal & that must not split.
	cmd := exec.Command(argv[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + argv[0] + `" /d /s /c "` + argv[4] + `"`}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run through cmd.exe: %v\n%s", err, output)
	}
	got := strings.ReplaceAll(string(output), "\r\n", "\n")
	if !strings.Contains(got, "first=run dev\n") || !strings.Contains(got, "second=a&b\n") {
		t.Fatalf("output = %q, want both arguments preserved", got)
	}
}

func TestPTYArgvLeavesNativeExecutablesAlone(t *testing.T) {
	argv := ptyArgv(os.Args[0], []string{"-x", "a b"})
	if len(argv) != 3 || argv[0] != os.Args[0] || argv[2] != "a b" {
		t.Fatalf("argv = %q", argv)
	}
}
