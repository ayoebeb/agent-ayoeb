package shellterm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// A command run executes one command the user clicked Run on in Chat: once,
// non-interactively (no stdin), in the session's workspace, with the user's
// own shell and environment. It is deliberately not a terminal: there is no
// PTY to attach to, nothing is persisted, and the output streams back to the
// one client that asked for it.
//
// Elevation is out of scope. A future "Run as administrator" must be a
// separate, desktop-local entry point (see the elevated terminal work), never
// a flag on this one: RunCommand is reachable from paired remote clients.

// MaxCommandRunBytes bounds a command's text. PowerShell receives the command
// on its command line, which Windows caps at 32767 UTF-16 units.
const MaxCommandRunBytes = 8000

var (
	// commandRunTimeout stops a run that never finishes. Variables so tests
	// need not wait them out.
	commandRunTimeout = 15 * time.Minute
	// commandRunOutputLimit caps what one run sends back; the command keeps
	// running past it and the exit reports how much was dropped.
	commandRunOutputLimit int64 = 1 << 20
	// commandRunFlushInterval coalesces a chatty command's writes into fewer,
	// larger output events.
	commandRunFlushInterval = 40 * time.Millisecond
	// commandRunWaitDelay bounds how long a finished command's leftover
	// children may hold its output pipe open.
	commandRunWaitDelay = 2 * time.Second
)

// maxConcurrentCommandRuns bounds runs across all sessions on this daemon.
const maxConcurrentCommandRuns = 8

// RunCommandInput is one user-requested command run.
type RunCommandInput struct {
	SessionID domain.SessionID
	// Shell is a terminal shell selector (auto, pwsh, powershell, cmd,
	// git-bash, or a path), the same vocabulary as the Settings shell picker.
	Shell   string
	Command string
}

// CommandRunStart describes a run that started.
type CommandRunStart struct {
	RunID      string
	Shell      string
	ShellPath  string
	WorkingDir string
	StartedAt  time.Time
}

// CommandRunExit is how a run ended.
type CommandRunExit struct {
	ExitCode     int
	DurationMs   int64
	Stopped      bool
	TimedOut     bool
	Truncated    bool
	DroppedBytes int64
}

// OpenCommandInTerminalInput types a command into a new session shell, for a
// command that needs input or a real terminal.
type OpenCommandInTerminalInput struct {
	SessionID domain.SessionID
	Shell     string
	Command   string
}

type activeCommandRun struct {
	sessionID domain.SessionID
	cancel    context.CancelFunc
	mu        sync.Mutex
	stopped   bool
}

type commandRunRegistry struct {
	mu   sync.Mutex
	runs map[string]*activeCommandRun
}

func (r *commandRunRegistry) add(id string, run *activeCommandRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = map[string]*activeCommandRun{}
	}
	if len(r.runs) >= maxConcurrentCommandRuns {
		return apierr.Conflict("COMMAND_RUN_LIMIT",
			fmt.Sprintf("%d commands are already running; stop one and try again", maxConcurrentCommandRuns), nil)
	}
	r.runs[id] = run
	return nil
}

func (r *commandRunRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.runs, id)
	r.mu.Unlock()
}

func (r *commandRunRegistry) get(id string) *activeCommandRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[id]
}

// validateRunCommand rejects text that cannot be a command for any shell.
func validateRunCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return apierr.Invalid("COMMAND_RUN_COMMAND_REQUIRED", "A command is required", nil)
	}
	if len(command) > MaxCommandRunBytes {
		return apierr.Invalid("COMMAND_RUN_COMMAND_TOO_LONG",
			fmt.Sprintf("A command must be at most %d bytes", MaxCommandRunBytes), nil)
	}
	if strings.ContainsRune(command, 0) || !utf8.ValidString(command) {
		return apierr.Invalid("COMMAND_RUN_COMMAND_INVALID", "The command contains invalid characters", nil)
	}
	return nil
}

// RunCommand runs in.Command and blocks until it ends. onStart is called once
// the process is running; onOutput receives its combined stdout and stderr as
// UTF-8 text, in order, never after RunCommand returns. An error means the
// command never started; a started command always returns its exit.
//
// Cancelling ctx (the client went away) stops the command and its children.
func (s *Service) RunCommand(ctx context.Context, in RunCommandInput, onStart func(CommandRunStart), onOutput func(string)) (CommandRunExit, error) {
	if err := validateRunCommand(in.Command); err != nil {
		return CommandRunExit{}, err
	}
	if in.SessionID == "" {
		return CommandRunExit{}, apierr.Invalid("COMMAND_RUN_SESSION_REQUIRED", "A session is required", nil)
	}
	workingDir, projectID, err := s.resolveShellTerminalWorkingDir(ctx, "", in.SessionID)
	if err != nil {
		return CommandRunExit{}, err
	}
	shellArgv, usedFallback := resolveUserLoginShell(in.Shell)
	if usedFallback {
		return CommandRunExit{}, apierr.Invalid("SHELL_TERMINAL_SHELL_UNAVAILABLE",
			fmt.Sprintf("The selected shell is unavailable: %s", in.Shell), nil)
	}
	if len(shellArgv) == 0 {
		return CommandRunExit{}, apierr.Internal("SHELL_TERMINAL_NO_SHELL", "Could not determine a shell to run the command")
	}
	invocation, err := commandRunInvocation(shellArgv[0], in.Command)
	if err != nil {
		return CommandRunExit{}, err
	}
	var projectEnv map[string]string
	if projectID != "" && s.projects != nil {
		projectEnv, err = s.projects.ProjectEnv(ctx, projectID)
		if err != nil {
			return CommandRunExit{}, fmt.Errorf("run command: resolve project environment: %w", err)
		}
	}
	env := s.pinnedEnv(projectEnv)
	if env == nil {
		env = map[string]string{}
	}
	// The same environment a session shell tab gets: the command is the user's,
	// not the agent's turn's, and never carries a worker capability.
	env["AO_BROWSER_CAPABILITY"] = ""
	env["AO_PREVIEW_CAPABILITY"] = ""
	env[caller.ShellTerminalEnv] = "1"
	env["AO_SESSION_ID"] = string(in.SessionID)

	runID, err := newCommandRunID()
	if err != nil {
		return CommandRunExit{}, fmt.Errorf("run command: id: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, commandRunTimeout)
	defer cancel()
	active := &activeCommandRun{sessionID: in.SessionID, cancel: cancel}
	if err := s.commandRuns.add(runID, active); err != nil {
		return CommandRunExit{}, err
	}
	defer s.commandRuns.remove(runID)

	output := newCommandRunOutput(commandRunOutputLimit)
	cmd := process.CommandContext(runCtx, invocation.path, invocation.args...)
	process.ConfigureTreeCancellation(cmd)
	setRawCommandLine(cmd, invocation.rawCommandLine)
	cmd.Dir = workingDir
	cmd.Env = mergeProcessEnv(os.Environ(), env)
	cmd.Stdin = nil
	// One writer for both streams: os/exec then gives the child a single pipe,
	// so stdout and stderr interleave in the order the command wrote them.
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = commandRunWaitDelay

	started := s.now()
	if err := cmd.Start(); err != nil {
		return CommandRunExit{}, apierr.Internal("COMMAND_RUN_START_FAILED", "Could not start the shell: "+err.Error())
	}
	if onStart != nil {
		onStart(CommandRunStart{
			RunID:      runID,
			Shell:      shellDisplayName(invocation.path),
			ShellPath:  invocation.path,
			WorkingDir: workingDir,
			StartedAt:  started,
		})
	}
	streamed := make(chan struct{})
	go func() {
		defer close(streamed)
		output.stream(onOutput)
	}()
	waitErr := cmd.Wait()
	output.close()
	<-streamed

	exit := CommandRunExit{
		ExitCode:   -1,
		DurationMs: s.now().Sub(started).Milliseconds(),
	}
	if cmd.ProcessState != nil {
		exit.ExitCode = cmd.ProcessState.ExitCode()
	}
	active.mu.Lock()
	exit.Stopped = active.stopped
	active.mu.Unlock()
	if !exit.Stopped && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		exit.TimedOut = true
	}
	if !exit.Stopped && !exit.TimedOut && ctx.Err() != nil {
		// The client left; nobody will read the exit, but say what happened.
		exit.Stopped = true
	}
	if waitErr != nil && exit.ExitCode == 0 && (exit.Stopped || exit.TimedOut) {
		exit.ExitCode = -1
	}
	exit.Truncated, exit.DroppedBytes = output.truncation()
	return exit, nil
}

// StopCommandRun stops a running command of the session. A run that already
// ended answers not found.
func (s *Service) StopCommandRun(_ context.Context, sessionID domain.SessionID, runID string) error {
	run := s.commandRuns.get(strings.TrimSpace(runID))
	if run == nil || run.sessionID != sessionID {
		return apierr.NotFound("COMMAND_RUN_NOT_FOUND", "No such running command")
	}
	run.mu.Lock()
	run.stopped = true
	run.mu.Unlock()
	run.cancel()
	return nil
}

// OpenCommandInTerminal opens a new shell tab in the session and types the
// command there, for commands that need input or a real terminal. The shell
// is a normal user shell, exactly as if the user had typed the command.
func (s *Service) OpenCommandInTerminal(ctx context.Context, in OpenCommandInTerminalInput) (ShellTerminal, error) {
	if err := validateRunCommand(in.Command); err != nil {
		return ShellTerminal{}, err
	}
	if in.SessionID == "" {
		return ShellTerminal{}, apierr.Invalid("COMMAND_RUN_SESSION_REQUIRED", "A session is required", nil)
	}
	if s.sessions == nil {
		return ShellTerminal{}, apierr.Internal("SHELL_TERMINAL_NO_SESSION_LOOKUP", "Session lookup is unavailable")
	}
	// Validate before allocating a gate, as OpenShellTerminal does.
	if _, _, err := s.sessions.SessionWorkspace(ctx, in.SessionID); err != nil {
		return ShellTerminal{}, fmt.Errorf("open command in terminal: resolve session %s: %w", in.SessionID, err)
	}
	release, err := s.acquireSessionGate(ctx, in.SessionID)
	if err != nil {
		return ShellTerminal{}, err
	}
	defer release()
	workingDir, projectID, err := s.resolveShellTerminalWorkingDir(ctx, "", in.SessionID)
	if err != nil {
		return ShellTerminal{}, err
	}
	return s.typeCommandInNewShell(ctx, workingDir, projectID, in.SessionID, in.Shell, in.Command)
}

type commandRunInvocationSpec struct {
	path string
	args []string
	// rawCommandLine, when set, is the exact Windows command line: cmd.exe
	// does not parse the argv quoting Go would otherwise produce.
	rawCommandLine string
}

// powershellRunPrelude makes PowerShell write UTF-8 to the pipe (it uses the
// OEM code page otherwise) and keeps progress bars out of captured output.
const powershellRunPrelude = "[Console]::OutputEncoding=[Text.Encoding]::UTF8;$ProgressPreference='SilentlyContinue'\n"

// powershellRunTrailer reports a failed last command's own exit code; plain
// -Command reports 1 for any failure, even a native command's exit 3.
const powershellRunTrailer = "\nif(-not $?){if($LASTEXITCODE){exit $LASTEXITCODE};exit 1}"

// commandRunInvocation builds the non-interactive form of a shell. PowerShell
// takes the command with -Command (-EncodedCommand makes Windows PowerShell
// write errors as CLIXML, and -File would need an execution policy change);
// cmd.exe takes a raw /c line; every POSIX-style shell takes -c.
func commandRunInvocation(shellPath, command string) (commandRunInvocationSpec, error) {
	name := strings.ToLower(filepath.Base(shellPath))
	switch name {
	case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return commandRunInvocationSpec{
			path: shellPath,
			args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powershellRunPrelude + command + powershellRunTrailer},
		}, nil
	case "cmd.exe", "cmd":
		line := cmdRunLine(command)
		return commandRunInvocationSpec{
			path:           shellPath,
			args:           []string{"/d", "/s", "/c", line},
			rawCommandLine: `"` + shellPath + `" /d /s /c "` + line + `"`,
		}, nil
	case "wsl.exe", "wsl":
		return commandRunInvocationSpec{}, apierr.Invalid("COMMAND_RUN_SHELL_UNSUPPORTED",
			"WSL cannot run commands from Chat yet; choose another shell or open it in a terminal", nil)
	default:
		return commandRunInvocationSpec{path: shellPath, args: []string{"-c", command}}, nil
	}
}

// cmdRunLine turns a cmd block into one /c line: switch the code page to UTF-8
// so output is not mojibake, then run each line in turn as a batch file would.
// A trailing caret continues a line onto the next.
func cmdRunLine(command string) string {
	var lines []string
	var current strings.Builder
	for _, raw := range strings.Split(strings.ReplaceAll(command, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if continued, ok := strings.CutSuffix(line, "^"); ok {
			// cmd splices the next line on as written, as here.
			current.WriteString(continued)
			continue
		}
		current.WriteString(line)
		if joined := strings.TrimSpace(current.String()); joined != "" {
			lines = append(lines, joined)
		}
		current.Reset()
	}
	if rest := strings.TrimSpace(current.String()); rest != "" {
		lines = append(lines, rest)
	}
	return "chcp 65001>nul & " + strings.Join(lines, " & ")
}

// mergeProcessEnv overlays overrides on a process environment. Windows keys
// are case-insensitive, so an override replaces every spelling of its key.
func mergeProcessEnv(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key == "" || overridden(overrides, key) {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func overridden(overrides map[string]string, key string) bool {
	if _, ok := overrides[key]; ok {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	for candidate := range overrides {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}

func newCommandRunID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "cmdrun-" + hex.EncodeToString(buf), nil
}

// commandRunOutput collects a command's output up to a limit and hands it on
// in coalesced UTF-8 chunks. The command never blocks on a slow reader: past
// the limit, output is counted and dropped.
type commandRunOutput struct {
	mu      sync.Mutex
	pending []byte
	kept    int64
	dropped int64
	limit   int64
	closed  bool
	notify  chan struct{}
}

func newCommandRunOutput(limit int64) *commandRunOutput {
	return &commandRunOutput{limit: limit, notify: make(chan struct{}, 1)}
}

func (o *commandRunOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	room := o.limit - o.kept
	take := int64(len(p))
	if take > room {
		take = max(room, 0)
	}
	o.pending = append(o.pending, p[:take]...)
	o.kept += take
	o.dropped += int64(len(p)) - take
	o.mu.Unlock()
	o.signal()
	return len(p), nil
}

func (o *commandRunOutput) signal() {
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

func (o *commandRunOutput) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.signal()
}

func (o *commandRunOutput) truncation() (bool, int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped > 0, o.dropped
}

// take returns the text collected so far. Until the output is closed it holds
// back a UTF-8 sequence cut off at the end, so a character split across two
// writes is never sent as two replacement characters.
func (o *commandRunOutput) take() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data := o.pending
	if !o.closed {
		cut := incompleteUTF8Suffix(data)
		o.pending = append([]byte(nil), data[len(data)-cut:]...)
		data = data[:len(data)-cut]
	} else {
		o.pending = nil
	}
	if len(data) == 0 {
		return "", o.closed
	}
	return strings.ToValidUTF8(string(data), "�"), o.closed
}

func (o *commandRunOutput) stream(onOutput func(string)) {
	for {
		<-o.notify
		// Let a burst of small writes gather into one event.
		time.Sleep(commandRunFlushInterval)
		// Once closed, take hands over everything left, partial sequence included.
		text, closed := o.take()
		if text != "" && onOutput != nil {
			onOutput(text)
		}
		if closed {
			return
		}
	}
}

// incompleteUTF8Suffix is the length of a UTF-8 sequence started but not
// finished at the end of data (0 when data ends on a boundary).
func incompleteUTF8Suffix(data []byte) int {
	for i := 1; i <= 3 && i <= len(data); i++ {
		b := data[len(data)-i]
		if b&0xC0 == 0x80 {
			continue // continuation byte; keep looking for the lead
		}
		if b < 0x80 {
			return 0
		}
		var need int
		switch {
		case b&0xE0 == 0xC0:
			need = 2
		case b&0xF0 == 0xE0:
			need = 3
		case b&0xF8 == 0xF0:
			need = 4
		default:
			return 0
		}
		if need > i {
			return i
		}
		return 0
	}
	return 0
}
