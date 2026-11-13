package shellterm

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestCommandRunInvocationPerShell(t *testing.T) {
	// A shell path is a path on the host that runs the command: filepath.Base
	// does not split a Windows path on a POSIX host, and a POSIX host is never
	// handed one. So each executable is spelled in the host's own path syntax,
	// and every case runs on every host; the shell-by-name mapping under test
	// is the same everywhere.
	hostPath := func(windows, posix string) string {
		if runtime.GOOS == "windows" {
			return windows
		}
		return posix
	}
	pwsh := hostPath(`C:\Program Files\PowerShell\7\pwsh.exe`, "/opt/microsoft/powershell/7/pwsh.exe")
	windowsPowerShell := hostPath(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, "/opt/windows/System32/WindowsPowerShell/v1.0/powershell.exe")
	cmd := hostPath(`C:\Windows\system32\cmd.exe`, "/opt/windows/system32/cmd.exe")
	gitBash := hostPath(`C:\Program Files\Git\bin\bash.exe`, "/opt/git/bin/bash.exe")
	wsl := hostPath(`C:\Windows\system32\wsl.exe`, "/opt/windows/system32/wsl.exe")
	for _, tc := range []struct {
		shell    string
		wantArgs []string
		wantRaw  string
		wantErr  string
	}{
		{shell: pwsh, wantArgs: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powershellRunPrelude + "echo hi" + powershellRunTrailer}},
		{shell: windowsPowerShell, wantArgs: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powershellRunPrelude + "echo hi" + powershellRunTrailer}},
		{shell: "/usr/local/bin/pwsh", wantArgs: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powershellRunPrelude + "echo hi" + powershellRunTrailer}},
		{shell: cmd, wantArgs: []string{"/d", "/s", "/c", "chcp 65001>nul & echo hi"}, wantRaw: `"` + cmd + `" /d /s /c "chcp 65001>nul & echo hi"`},
		{shell: gitBash, wantArgs: []string{"-c", "echo hi"}},
		{shell: "/bin/zsh", wantArgs: []string{"-c", "echo hi"}},
		{shell: "/opt/homebrew/bin/fish", wantArgs: []string{"-c", "echo hi"}},
		{shell: wsl, wantErr: "COMMAND_RUN_SHELL_UNSUPPORTED"},
	} {
		t.Run(filepath.Base(tc.shell), func(t *testing.T) {
			got, err := commandRunInvocation(tc.shell, "echo hi")
			if tc.wantErr != "" {
				var apiErr *apierr.Error
				if !errors.As(err, &apiErr) || apiErr.Code != tc.wantErr {
					t.Fatalf("err = %v, want %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.path != tc.shell || strings.Join(got.args, "\x00") != strings.Join(tc.wantArgs, "\x00") || got.rawCommandLine != tc.wantRaw {
				t.Fatalf("invocation = %+v, err = %v", got, err)
			}
		})
	}
}

func TestCmdRunLineRunsEachLineAndHonoursCarets(t *testing.T) {
	got := cmdRunLine("cd sub\r\n\r\necho one ^\n  two\necho \"q\"\n")
	want := `chcp 65001>nul & cd sub & echo one   two & echo "q"`
	if got != want {
		t.Fatalf("cmdRunLine = %q, want %q", got, want)
	}
}

func TestValidateRunCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		code    string
	}{
		{"  \n", "COMMAND_RUN_COMMAND_REQUIRED"},
		{strings.Repeat("x", MaxCommandRunBytes+1), "COMMAND_RUN_COMMAND_TOO_LONG"},
		{"echo a\x00b", "COMMAND_RUN_COMMAND_INVALID"},
		{"echo \xff", "COMMAND_RUN_COMMAND_INVALID"},
	} {
		var apiErr *apierr.Error
		if err := validateRunCommand(tc.command); !errors.As(err, &apiErr) || apiErr.Code != tc.code {
			t.Fatalf("validate %q = %v, want %s", tc.command, err, tc.code)
		}
	}
	if err := validateRunCommand("echo ok"); err != nil {
		t.Fatal(err)
	}
}

func TestMergeProcessEnvReplacesEveryKeySpelling(t *testing.T) {
	got := mergeProcessEnv([]string{"Path=C:\\old", "HOME=/home/u", "KEEP=1", "=C:=C:\\"}, map[string]string{"PATH": "C:\\new", "AO_SESSION_ID": "s1"})
	joined := "\n" + strings.Join(got, "\n") + "\n"
	if !strings.Contains(joined, "\nPATH=C:\\new\n") || !strings.Contains(joined, "\nKEEP=1\n") || !strings.Contains(joined, "\nAO_SESSION_ID=s1\n") {
		t.Fatalf("merged env = %q", got)
	}
	if hasOld := strings.Contains(joined, "\nPath=C:\\old\n"); hasOld != (runtime.GOOS != "windows") {
		t.Fatalf("old Path kept = %v on %s: %q", hasOld, runtime.GOOS, got)
	}
}

func TestCommandRunOutputHoldsBackSplitCharactersAndCapsOutput(t *testing.T) {
	out := newCommandRunOutput(1 << 10)
	euro := []byte("€") // three bytes
	_, _ = out.Write(append([]byte("a"), euro[:2]...))
	if text, closed := out.take(); text != "a" || closed {
		t.Fatalf("first take = %q, %v; want the complete prefix only", text, closed)
	}
	_, _ = out.Write(euro[2:])
	if text, _ := out.take(); text != "€" {
		t.Fatalf("second take = %q, want the joined character", text)
	}

	capped := newCommandRunOutput(4)
	if n, err := capped.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Fatalf("write = %d, %v; a capped writer must not fail the command", n, err)
	}
	capped.close()
	text, closed := capped.take()
	truncated, dropped := capped.truncation()
	if text != "abcd" || !closed || !truncated || dropped != 4 {
		t.Fatalf("capped = %q closed=%v truncated=%v dropped=%d", text, closed, truncated, dropped)
	}
}

// runTestShell picks a shell every CI runner has and the commands to drive it.
type runTestCommands struct {
	shell, echoExit3, sessionEnv, sleep, flood string
}

func commandRunTestShell(t *testing.T) runTestCommands {
	t.Helper()
	if runtime.GOOS == "windows" {
		return runTestCommands{
			shell:      "cmd",
			echoExit3:  "echo hello-run & exit /b 3",
			sessionEnv: "echo [%AO_SESSION_ID%][%AO_SHELL_TERMINAL%]",
			sleep:      "ping -n 30 127.0.0.1 >nul",
			flood:      "for /L %i in (1,1,200) do @echo 0123456789",
		}
	}
	return runTestCommands{
		shell:      "/bin/sh",
		echoExit3:  "echo hello-run; exit 3",
		sessionEnv: `echo "[$AO_SESSION_ID][$AO_SHELL_TERMINAL]"`,
		sleep:      "sleep 30",
		flood:      `i=0; while [ $i -lt 200 ]; do echo 0123456789; i=$((i+1)); done`,
	}
}

func newCommandRunTestService(t *testing.T) (*Service, string) {
	t.Helper()
	workspace := t.TempDir()
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"session": {workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityBlocked},
	}}
	svc := newTestServiceWithSessions(newFakeShellRuntime(), &fakeShellTerminalStore{}, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": t.TempDir()}}, sessions)
	svc.dataDir = t.TempDir()
	svc.now = time.Now
	return svc, workspace
}

type recordedRun struct {
	mu     sync.Mutex
	start  *CommandRunStart
	output strings.Builder
}

func (r *recordedRun) onStart(start CommandRunStart) {
	r.mu.Lock()
	r.start = &start
	r.mu.Unlock()
}

func (r *recordedRun) onOutput(text string) {
	r.mu.Lock()
	r.output.WriteString(text)
	r.mu.Unlock()
}

func (r *recordedRun) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.output.String()
}

func TestRunCommandStreamsOutputAndExitCodeFromTheSessionWorkspace(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, workspace := newCommandRunTestService(t)
	var rec recordedRun
	exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.echoExit3}, rec.onStart, rec.onOutput)
	if err != nil {
		t.Fatal(err)
	}
	if exit.ExitCode != 3 || exit.Stopped || exit.TimedOut || exit.Truncated {
		t.Fatalf("exit = %+v, want code 3", exit)
	}
	if !strings.Contains(rec.text(), "hello-run") {
		t.Fatalf("output = %q", rec.text())
	}
	if rec.start == nil || rec.start.WorkingDir != workspace || !strings.HasPrefix(rec.start.RunID, "cmdrun-") || rec.start.ShellPath == "" {
		t.Fatalf("start = %+v, want the session workspace %s", rec.start, workspace)
	}
	if svc.commandRuns.get(rec.start.RunID) != nil {
		t.Fatal("a finished run stays registered")
	}
}

func TestRunCommandMarksTheShellAsTheUsersOwn(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	var rec recordedRun
	if _, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.sessionEnv}, nil, rec.onOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.text(), "[session][1]") {
		t.Fatalf("output = %q, want the session id and the user-shell marker", rec.text())
	}
}

func TestRunCommandWindowsPowerShellReportsNativeExitCodeAndUTF8(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell")
	}
	svc, _ := newCommandRunTestService(t)
	var rec recordedRun
	exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: "powershell", Command: "Write-Output 'héllo €'\ncmd /c exit 5"}, nil, rec.onOutput)
	if err != nil {
		t.Fatal(err)
	}
	if exit.ExitCode != 5 || !strings.Contains(rec.text(), "héllo €") {
		t.Fatalf("exit = %+v output = %q", exit, rec.text())
	}
}

func TestStopCommandRunEndsTheCommandAndItsChildren(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	started := make(chan string, 1)
	done := make(chan CommandRunExit, 1)
	go func() {
		exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.sleep},
			func(start CommandRunStart) { started <- start.RunID }, nil)
		if err != nil {
			t.Error(err)
		}
		done <- exit
	}()
	runID := <-started
	var apiErr *apierr.Error
	if err := svc.StopCommandRun(context.Background(), "other-session", runID); !errors.As(err, &apiErr) || apiErr.Code != "COMMAND_RUN_NOT_FOUND" {
		t.Fatalf("stop from another session = %v, want not found", err)
	}
	if err := svc.StopCommandRun(context.Background(), "session", runID); err != nil {
		t.Fatal(err)
	}
	select {
	case exit := <-done:
		if !exit.Stopped || exit.TimedOut {
			t.Fatalf("exit = %+v, want stopped", exit)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("stopped command kept running")
	}
	if err := svc.StopCommandRun(context.Background(), "session", runID); !errors.As(err, &apiErr) || apiErr.Code != "COMMAND_RUN_NOT_FOUND" {
		t.Fatalf("stop after exit = %v, want not found", err)
	}
}

func TestRunCommandStopsWhenTheClientLeaves(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	exit, err := svc.RunCommand(ctx, RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.sleep}, func(CommandRunStart) { cancel() }, nil)
	if err != nil || !exit.Stopped {
		t.Fatalf("exit = %+v, err = %v; want stopped", exit, err)
	}
}

func TestSessionTeardownStopsRunningCommandsBeforeReturning(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	started := make(chan struct{})
	done := make(chan CommandRunExit, 1)
	go func() {
		exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.sleep},
			func(CommandRunStart) { close(started) }, nil)
		if err != nil {
			t.Error(err)
		}
		done <- exit
	}()
	<-started
	release, err := svc.BeginSessionTeardown(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// The worktree is free once teardown returns: the run has already ended.
	// RunCommand drops a run from the registry just before it closes the done
	// channel teardown waits on, so a run still registered here means teardown
	// returned while the command was still running in the worktree.
	if runs := svc.commandRuns.forSession("session"); len(runs) != 0 {
		t.Fatalf("BeginSessionTeardown returned while %d command(s) still ran in the worktree", len(runs))
	}
	// The exit reaches this test a moment after the run is done: RunCommand
	// still has to return and the goroutine above to send it. Wait for it with a
	// bound instead of sampling the channel, which failed whenever that
	// goroutine had not been scheduled yet.
	select {
	case exit := <-done:
		if !exit.Stopped {
			t.Fatalf("exit = %+v, want stopped by teardown", exit)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("RunCommand did not return after session teardown stopped it")
	}
}

func TestRunCommandWaitsForASessionTeardownInProgress(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	release, err := svc.BeginSessionTeardown(context.Background(), "session")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	startedDuringTeardown := false
	_, err = svc.RunCommand(ctx, RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.echoExit3},
		func(CommandRunStart) { startedDuringTeardown = true }, nil)
	if !errors.Is(err, context.DeadlineExceeded) || startedDuringTeardown {
		t.Fatalf("err = %v started=%v; want the run held at the session gate", err, startedDuringTeardown)
	}
	release()
	exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.echoExit3}, nil, nil)
	if err != nil || exit.ExitCode != 3 {
		t.Fatalf("after teardown: exit = %+v, err = %v", exit, err)
	}
}

func TestRunCommandTimesOut(t *testing.T) {
	previous := commandRunTimeout
	commandRunTimeout = 500 * time.Millisecond
	t.Cleanup(func() { commandRunTimeout = previous })
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.sleep}, nil, nil)
	if err != nil || !exit.TimedOut || exit.Stopped {
		t.Fatalf("exit = %+v, err = %v; want timed out", exit, err)
	}
}

func TestRunCommandCapsOutput(t *testing.T) {
	previous := commandRunOutputLimit
	commandRunOutputLimit = 64
	t.Cleanup(func() { commandRunOutputLimit = previous })
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	var rec recordedRun
	exit, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: cmds.flood}, nil, rec.onOutput)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.text()) > 64 || !exit.Truncated || exit.DroppedBytes <= 0 || exit.ExitCode != 0 {
		t.Fatalf("output %d bytes, exit = %+v; want at most 64 bytes and a truncated exit", len(rec.text()), exit)
	}
}

func TestRunCommandRejectsBeforeStarting(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	for _, tc := range []struct {
		name string
		in   RunCommandInput
		code string
	}{
		{"empty", RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: " "}, "COMMAND_RUN_COMMAND_REQUIRED"},
		{"no session", RunCommandInput{Shell: cmds.shell, Command: "echo"}, "COMMAND_RUN_SESSION_REQUIRED"},
		{"unknown session", RunCommandInput{SessionID: "missing", Shell: cmds.shell, Command: "echo"}, "SESSION_NOT_FOUND"},
		{"unavailable shell", RunCommandInput{SessionID: "session", Shell: filepath.Join(t.TempDir(), "no-such-shell"), Command: "echo"}, "SHELL_TERMINAL_SHELL_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			startedCalled := false
			_, err := svc.RunCommand(context.Background(), tc.in, func(CommandRunStart) { startedCalled = true }, nil)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code || startedCalled {
				t.Fatalf("err = %v started=%v, want %s before start", err, startedCalled, tc.code)
			}
		})
	}
}

func TestRunCommandLimitsConcurrentRuns(t *testing.T) {
	cmds := commandRunTestShell(t)
	svc, _ := newCommandRunTestService(t)
	for i := range maxConcurrentCommandRuns {
		if err := svc.commandRuns.add(string(rune('a'+i)), &activeCommandRun{sessionID: "session", cancel: func() {}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := svc.RunCommand(context.Background(), RunCommandInput{SessionID: "session", Shell: cmds.shell, Command: "echo hi"}, nil, nil)
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "COMMAND_RUN_LIMIT" {
		t.Fatalf("err = %v, want COMMAND_RUN_LIMIT", err)
	}
}

func TestOpenCommandInTerminalTypesIntoANewSessionShell(t *testing.T) {
	workspace := t.TempDir()
	rt := newFakeShellRuntime()
	rt.cueReady = true
	st := &fakeShellTerminalStore{}
	// A session waiting on the user is exactly when a command may need a
	// terminal, so unlike command cues this does not refuse blocked sessions.
	sessions := &fakeSessionWorkspaceLocator{sessions: map[domain.SessionID]fakeSessionWorkspace{
		"session": {workspacePath: workspace, projectID: "portfolio", activity: domain.ActivityBlocked},
	}}
	svc := newTestServiceWithSessions(rt, st, &fakeProjectRootLocator{roots: map[domain.ProjectID]string{"portfolio": t.TempDir()}}, sessions)
	svc.dataDir = t.TempDir()
	term, err := svc.OpenCommandInTerminal(context.Background(), OpenCommandInTerminalInput{SessionID: "session", Shell: cueTestShell(t), Command: "npm login"})
	if err != nil {
		t.Fatal(err)
	}
	if term.SessionID != "session" || term.WorkingDir != workspace || len(rt.created) != 1 || st.records[0].Transient {
		t.Fatalf("terminal = %+v, creates = %+v", term, rt.created)
	}
	assertCueDelivery(t, rt, term.HandleID, "npm login")

	var apiErr *apierr.Error
	if _, err := svc.OpenCommandInTerminal(context.Background(), OpenCommandInTerminalInput{SessionID: "missing", Command: "npm login"}); !errors.As(err, &apiErr) || apiErr.Code != "SESSION_NOT_FOUND" {
		t.Fatalf("unknown session = %v", err)
	}
	if _, err := svc.OpenCommandInTerminal(context.Background(), OpenCommandInTerminalInput{SessionID: "session", Command: ""}); !errors.As(err, &apiErr) || apiErr.Code != "COMMAND_RUN_COMMAND_REQUIRED" {
		t.Fatalf("empty command = %v", err)
	}
}
