package conpty

import "testing"

func TestCmdExeRawLine(t *testing.T) {
	line := `"C:\Program Files\nodejs\npm.cmd" run dev -- --config "config/dev config.js" "a&b"`
	got, ok := cmdExeRawLine(`C:\Windows\System32\cmd.exe`, []string{"/d", "/s", "/c", line})
	want := `"C:\Windows\System32\cmd.exe" /d /s /c ""C:\Program Files\nodejs\npm.cmd" run dev -- --config "config/dev config.js" "a&b""`
	if !ok || got != want {
		t.Fatalf("cmdExeRawLine = %q, %v\nwant %q", got, ok, want)
	}
	for name, c := range map[string]struct {
		shell string
		args  []string
	}{
		"other program":      {`C:\tools\node.exe`, []string{"/d", "/s", "/c", "x"}},
		"missing /s":         {`cmd.exe`, []string{"/d", "/c", "x"}},
		"extra argument":     {`cmd.exe`, []string{"/d", "/s", "/c", "x", "y"}},
		"interactive cmd":    {`cmd.exe`, nil},
		"cmd-like directory": {`C:\cmd.exe\tool.exe`, []string{"/d", "/s", "/c", "x"}},
	} {
		if line, ok := cmdExeRawLine(c.shell, c.args); ok {
			t.Errorf("%s: got raw line %q, want the argv left alone", name, line)
		}
	}
}
