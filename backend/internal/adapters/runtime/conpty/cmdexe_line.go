package conpty

import "strings"

// cmdExeRawLine returns the exact Windows command line for a command-interpreter
// launch of the form `cmd.exe /d /s /c <line>`, where <line> is a command string
// that is already quoted for cmd.exe (see previewserver's batch-shim handling).
//
// Composing that argv with the native executable quoting rules would add quote
// pairs that cmd.exe's /s rule then strips from the wrong place, breaking spaced
// shim paths and letting & or | in an argument split the command. cmd.exe wants
// the line verbatim inside one pair of quotes, so the caller sets it as the
// process command line instead of composing it. Any other argv is not touched.
func cmdExeRawLine(shellCmd string, args []string) (string, bool) {
	if len(args) != 4 || args[0] != "/d" || args[1] != "/s" || args[2] != "/c" {
		return "", false
	}
	base := strings.ToLower(shellCmd[strings.LastIndexAny(shellCmd, `\/`)+1:])
	if base != "cmd.exe" && base != "cmd" {
		return "", false
	}
	return `"` + shellCmd + `" /d /s /c "` + args[3] + `"`, true
}
