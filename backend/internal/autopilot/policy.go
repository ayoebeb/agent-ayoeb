package autopilot

import (
	"encoding/base64"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Verdict is what the permission policy decides about one request.
type Verdict struct {
	Allow bool
	// Limit is the hard limit that made the policy deny; empty for an allow and
	// for a plain refusal (such as a write outside the workspace) that is not one
	// of the hard limits.
	Limit domain.HardLimit
	// Reason is one plain sentence for the log.
	Reason string
	// MergeRefs lists every `gh pr merge` target the request holds (an empty
	// string means the current branch's pull request): the service decides each
	// one against the stored PR facts, because a merge is allowed once AO review
	// approved the exact head and checks are green. A compound request is allowed
	// only if every one of them is.
	MergeRefs []string
	// Merge reports that the request holds at least one merge.
	Merge bool
}

// Action is one permission request reduced to what the policy reads.
type Action struct {
	// Tool is the provider's tool name (Bash, Edit, Read, mcp__server__tool).
	Tool string
	// Kind is the provider-neutral kind of what is asked: execute, edit, delete,
	// move, read, search, fetch, mcp_tool, or empty.
	Kind string
	// Command is the shell command of an execute request.
	Command string
	// Paths are the files a file tool touches.
	Paths []string
	// Workspace is the session's own workspace root.
	Workspace string
	// Cwd is the directory the provider says the command runs in; empty means
	// the workspace.
	Cwd string
	// Summary is the provider's one-line label for the request.
	Summary string
}

// ParseApproval reads the tool, kind, command, directory and paths out of the
// provider payload a chat approval carries: the ACP shape (`input`,
// `providerToolName`, `toolKind`, `subjectKind`) and the Codex shape (`method`,
// `command`, `rawCommand`, `cwd`). It never fails: a payload it cannot read
// yields an Action with nothing identified, which Evaluate refuses.
func ParseApproval(summary string, detail []byte, workspace string) Action {
	a := Action{Summary: strings.TrimSpace(summary), Workspace: workspace}
	var doc map[string]any
	if len(detail) > 0 {
		_ = json.Unmarshal(detail, &doc)
	}
	if v, ok := doc["providerToolName"].(string); ok {
		a.Tool = v
	}
	a.Kind = strings.ToLower(firstString(doc["toolKind"], doc["subjectKind"]))
	if a.Tool == "" {
		a.Tool = firstString(doc["toolName"], doc["tool"])
	}
	a.Cwd = firstString(doc["cwd"])
	// Codex names the request by its JSON-RPC method and puts the command at the
	// top level: `rawCommand` is the full original, `command` the unwrapped script.
	method := strings.ToLower(firstString(doc["method"]))
	if raw := firstString(doc["rawCommand"], doc["command"]); raw != "" {
		a.Command = raw
		if a.Tool == "" && a.Kind == "" {
			a.Kind = "execute"
		}
	}
	switch {
	case strings.Contains(method, "commandexecution") || strings.Contains(method, "execcommand"):
		if a.Kind == "" {
			a.Kind = "execute"
		}
	case strings.Contains(method, "filechange") || strings.Contains(method, "applypatch"):
		if a.Kind == "" {
			a.Kind = "edit"
		}
		for _, key := range []string{"paths", "files"} {
			if list, ok := doc[key].([]any); ok {
				for _, item := range list {
					if p, ok := item.(string); ok && strings.TrimSpace(p) != "" {
						a.Paths = append(a.Paths, strings.TrimSpace(p))
					}
				}
			}
		}
		if root := firstString(doc["grantRoot"]); root != "" {
			a.Paths = append(a.Paths, root)
		}
	}
	switch in := doc["input"].(type) {
	case map[string]any:
		if cmd := firstString(in["command"], in["cmd"], in["script"]); cmd != "" {
			a.Command = cmd
		}
		for _, key := range []string{"file_path", "path", "notebook_path", "filePath", "target_file", "old_path", "new_path"} {
			if p, ok := in[key].(string); ok && strings.TrimSpace(p) != "" {
				a.Paths = append(a.Paths, strings.TrimSpace(p))
			}
		}
		if list, ok := in["paths"].([]any); ok {
			for _, item := range list {
				if p, ok := item.(string); ok && strings.TrimSpace(p) != "" {
					a.Paths = append(a.Paths, strings.TrimSpace(p))
				}
			}
		}
	case string:
		if isShellTool(a.Tool, a.Kind) {
			a.Command = in
		}
	}
	if a.Command == "" && isShellTool(a.Tool, a.Kind) {
		// Some providers only put the command in the label ("Run `git status`").
		a.Command = strings.Trim(strings.TrimPrefix(a.Summary, "Run"), " `")
	}
	return a
}

func firstString(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// Evaluate decides one request. It allows anything inside the session's own
// workspace and denies only what the hard limits forbid, so an Autopilot session
// never waits on a prompt the policy can answer.
func Evaluate(a Action) Verdict {
	switch {
	case a.Tool == "" && a.Kind == "" && a.Command == "" && len(a.Paths) == 0:
		// Nothing in the request says what it does, so it cannot be judged.
		return Verdict{Reason: "The request does not say what it does, so AO cannot check it against the hard limits."}
	case isShellTool(a.Tool, a.Kind):
		if strings.TrimSpace(a.Command) == "" {
			return Verdict{Reason: "The request runs a command AO could not read."}
		}
		return evaluateCommandIn(a.Command, a.Workspace, a.Cwd)
	case isMCPTool(a.Tool):
		if limit, ok := classifyMCPTool(a.Tool); ok {
			return deny(limit, "The tool "+a.Tool+" would "+limit.Description()+".")
		}
		return allow("An MCP tool with no hard-limit effect.")
	case isDeleteTool(a.Tool, a.Kind):
		if len(a.Paths) == 0 {
			return Verdict{Reason: "The request deletes files without saying which."}
		}
		for _, p := range a.Paths {
			if limit, ok := credentialStorePath(p); ok {
				return deny(limit, "The request touches a credential store: "+p+".")
			}
			if !insideWorkspace(a.Workspace, p) {
				return deny(domain.HardLimitDataDelete, "The request deletes "+p+", which is outside the workspace.")
			}
		}
		return allow("Deletes only inside the workspace.")
	case isWriteTool(a.Tool, a.Kind):
		if len(a.Paths) == 0 {
			return Verdict{Reason: "The request changes files without saying which, so AO cannot tell they stay inside the workspace."}
		}
		for _, p := range a.Paths {
			if limit, ok := credentialStorePath(p); ok {
				return deny(limit, "The request touches a credential store: "+p+".")
			}
			if !insideWorkspace(a.Workspace, p) {
				return Verdict{Reason: "The request writes " + p + ", which is outside the workspace."}
			}
		}
		return allow("Writes only inside the workspace.")
	default:
		for _, p := range a.Paths {
			if limit, ok := credentialStorePath(p); ok {
				return deny(limit, "The request reads a credential store: "+p+".")
			}
		}
		return allow("Reads, searches and other tools with no hard-limit effect.")
	}
}

func allow(reason string) Verdict { return Verdict{Allow: true, Reason: reason} }

func deny(limit domain.HardLimit, reason string) Verdict {
	return Verdict{Limit: limit, Reason: reason}
}

func isShellTool(tool, kind string) bool {
	switch strings.ToLower(tool) {
	case "bash", "shell", "powershell", "pwsh", "terminal", "sh", "zsh", "cmd", "execute", "run_command", "run_terminal_cmd":
		return true
	}
	return kind == "execute" || kind == "command"
}

func isMCPTool(tool string) bool { return strings.HasPrefix(strings.ToLower(tool), "mcp__") }

func isDeleteTool(tool, kind string) bool {
	return kind == "delete" || strings.EqualFold(tool, "delete") || strings.EqualFold(tool, "deletefile")
}

func isWriteTool(tool, kind string) bool {
	switch kind {
	case "edit", "move", "write", "file_change":
		return true
	}
	switch strings.ToLower(tool) {
	case "edit", "write", "multiedit", "notebookedit", "apply_patch", "applypatch", "str_replace_editor", "create_file":
		return true
	}
	return false
}

var (
	mcpMessaging   = regexp.MustCompile(`(?i)(send|post|reply|message|mail|comment|notify|slack|discord|telegram|sms|whatsapp|invite|dm_)`)
	mcpMoney       = regexp.MustCompile(`(?i)(pay|charge|purchase|billing|subscribe|invoice|refund|checkout)`)
	mcpCredentials = regexp.MustCompile(`(?i)(password|credential|secret|api_?key|token|oauth|login|rotate_key)`)
)

// classifyMCPTool judges an MCP tool by its name: the server and tool names are
// all AO can read before the call.
func classifyMCPTool(tool string) (domain.HardLimit, bool) {
	name := strings.ToLower(tool)
	switch {
	case mcpMoney.MatchString(name):
		return domain.HardLimitMoney, true
	case mcpCredentials.MatchString(name):
		return domain.HardLimitCredentials, true
	case mcpMessaging.MatchString(name):
		return domain.HardLimitMessaging, true
	}
	return "", false
}

// credentialStore matches the places a person's logins live: a known credential
// file or folder directly under a home directory. A project's own .npmrc is not
// one, so only home-anchored paths count. Any request that names one is a
// credentials request, whether it reads or writes.
var credentialStore = regexp.MustCompile(`(?i)(~|\$home|\$\{home\}|%userprofile%|%homepath%|/home/[^/\s"']+|/users/[^/\s"']+|/root|[a-z]:[/\\]users[/\\][^/\\\s"']+)[/\\](\.ssh|\.aws|\.gnupg|\.git-credentials|\.netrc|\.npmrc|\.pypirc|\.kube[/\\]config|\.docker[/\\]config\.json|\.config[/\\]gh|\.azure|\.config[/\\]gcloud)([/\\]|$|\s|["'])`)

func credentialStorePath(p string) (domain.HardLimit, bool) {
	if credentialStore.MatchString(p) {
		return domain.HardLimitCredentials, true
	}
	return "", false
}

// deletionOutside says why a deletion target is not safely inside the workspace,
// or returns "" when it is. A relative target is placed against the directory the
// command really runs in (after any `cd`), and one that cannot be placed is
// refused rather than assumed to be the workspace.
func deletionOutside(env cmdEnv, p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"'`))
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "$") || strings.HasPrefix(p, "%") {
		return "which AO cannot place inside the workspace."
	}
	if !isAbsolute(normalizePath(p)) {
		abs := resolveIn(env.dir, p)
		if abs == "" {
			return "from a directory AO cannot determine, so it cannot be shown to stay inside the workspace."
		}
		p = abs
	}
	if !insideWorkspace(env.workspace, p) {
		return "which is outside the workspace."
	}
	return ""
}

// insideWorkspace reports whether p resolves to the workspace or something below
// it. A relative path is taken as relative to the workspace. A path that starts
// with ~, $VAR or %VAR% cannot be resolved here, so it counts as outside: the
// policy would rather refuse than guess.
func insideWorkspace(workspace, p string) bool {
	p = strings.TrimSpace(strings.Trim(p, `"'`))
	if p == "" {
		return true
	}
	if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "$") || strings.HasPrefix(p, "%") {
		return false
	}
	ws := normalizePath(workspace)
	if ws == "" {
		return false
	}
	target := normalizePath(p)
	if !isAbsolute(target) {
		target = path.Clean(ws + "/" + target)
	}
	if caseInsensitive(ws) {
		ws, target = strings.ToLower(ws), strings.ToLower(target)
	}
	return target == ws || strings.HasPrefix(target, strings.TrimSuffix(ws, "/")+"/")
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return path.Clean(strings.ReplaceAll(p, `\`, "/"))
}

var driveLetter = regexp.MustCompile(`^[A-Za-z]:`)

func isAbsolute(p string) bool { return strings.HasPrefix(p, "/") || driveLetter.MatchString(p) }

// caseInsensitive reports a Windows path, whose comparison ignores case.
func caseInsensitive(p string) bool { return driveLetter.MatchString(p) }

// ---- shell commands ----

// splitSegments cuts a command line at its separators (&&, ||, ;, |, newline),
// but not inside quotes: `bash -c "cd x && rm y"` is one command whose argument is
// a script, which innerCommands then reads as a whole.
func splitSegments(command string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	runes := []rune(command)
	flush := func() {
		if seg := strings.TrimSpace(cur.String()); seg != "" {
			out = append(out, seg)
		}
		cur.Reset()
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ';' || r == '\n' || r == '\r':
			flush()
		case r == '&' && i+1 < len(runes) && runes[i+1] == '&':
			flush()
			i++
		case r == '|':
			flush()
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// cmdEnv is where a command runs: the session's workspace, and the directory
// the shell is in. dir is empty when it cannot be known (after `cd "$X"`, `cd -`
// or `cd ~`), in which case a relative destructive target cannot be placed.
type cmdEnv struct {
	workspace string
	dir       string
}

// evaluateCommandIn judges a command that starts in cwd (the workspace when cwd
// is empty). It reads every command of a pipeline or list, so a forbidden step
// hidden behind `&&` or `;` is still found.
func evaluateCommandIn(command, workspace, cwd string) Verdict {
	dir := strings.TrimSpace(cwd)
	if dir == "" {
		dir = workspace
	}
	dir = normalizePath(dir)
	if dir != "" && !isAbsolute(dir) {
		dir = path.Join(normalizePath(workspace), dir)
	}
	return evaluateCommandDepth(command, cmdEnv{workspace: workspace, dir: dir}, 0)
}

// maxNesting bounds how deep a command inside a command (a shell -c string, an
// eval, a $(...) substitution) is read. Past it the command is refused as
// unreadable rather than waved through.
const maxNesting = 4

// innerRun is a command found inside another, with the directory it starts in.
type innerRun struct {
	command string
	dir     string
}

func evaluateCommandDepth(command string, env cmdEnv, depth int) Verdict {
	command = strings.TrimSpace(command)
	if command == "" {
		return allow("An empty command.")
	}
	if limit, ok := credentialStorePath(command); ok {
		return deny(limit, "The command touches a credential store.")
	}
	if limit, ok := messagingEndpoint(command); ok {
		return deny(limit, "The command sends a message through an outside service.")
	}
	var nested []innerRun
	for _, sub := range substitutions(command) {
		nested = append(nested, innerRun{sub, env.dir})
	}
	// merges collects every `gh pr merge` target of the request, so the service
	// can judge each one: a later approved merge must not authorize an earlier one.
	var merges []string
	for _, segment := range splitSegments(command) {
		tokens := tokenize(segment)
		tokens = stripWrappers(tokens)
		tokens = stripSubshell(tokens)
		if len(tokens) == 0 {
			continue
		}
		if dir, changed := changeDirectory(tokens, env); changed {
			env.dir = dir
			continue
		}
		for _, inner := range innerCommands(tokens) {
			nested = append(nested, innerRun{inner, env.dir})
		}
		v := classifySegment(tokens, env)
		if v.Merge {
			merges = append(merges, v.MergeRefs...)
			continue
		}
		if !v.Allow {
			return v
		}
	}
	if len(nested) > 0 && depth >= maxNesting {
		return Verdict{Reason: "The command nests other commands too deeply to read."}
	}
	for _, inner := range nested {
		v := evaluateCommandDepth(inner.command, cmdEnv{workspace: env.workspace, dir: inner.dir}, depth+1)
		switch {
		case v.Merge:
			merges = append(merges, v.MergeRefs...)
		case !v.Allow:
			return v
		}
	}
	if len(merges) > 0 {
		return Verdict{Merge: true, MergeRefs: merges, Reason: "A merge is decided against the PR's review and checks."}
	}
	return allow("A command with no hard-limit effect.")
}

// stripSubshell drops the "(" or "{" that opens a group, so `(cd x && rm y)` is
// read like `cd x && rm y`.
func stripSubshell(tokens []string) []string {
	for len(tokens) > 0 {
		head := strings.TrimLeft(tokens[0], "({")
		if head == tokens[0] {
			break
		}
		if head == "" {
			tokens = tokens[1:]
			continue
		}
		tokens = append([]string{head}, tokens[1:]...)
	}
	return tokens
}

// changeDirectory follows `cd`, `pushd`, `chdir` and PowerShell's `Set-Location`.
// It reports the new directory ("" when it cannot be known) and whether the
// segment was a directory change at all.
func changeDirectory(tokens []string, env cmdEnv) (string, bool) {
	switch baseProgram(tokens[0]) {
	case "cd", "chdir", "pushd", "set-location", "sl":
	case "popd":
		return "", true
	default:
		return "", false
	}
	target := ""
	for _, a := range tokens[1:] {
		if isFlagToken(a) {
			continue
		}
		target = a
		break
	}
	target = strings.TrimSpace(target)
	if target == "" || target == "-" || strings.ContainsAny(target[:1], "~$%") || strings.ContainsAny(target, "$`") {
		return "", true
	}
	return resolveIn(env.dir, target), true
}

// resolveIn places p relative to dir. It returns "" when the result cannot be
// known: a relative p with no known dir.
func resolveIn(dir, p string) string {
	p = normalizePath(p)
	if isAbsolute(p) {
		return p
	}
	if dir == "" {
		return ""
	}
	return path.Clean(dir + "/" + p)
}

var (
	dollarSubstitution = regexp.MustCompile(`\$\(([^()]*)\)`)
	backtickSubst      = regexp.MustCompile("`([^`]*)`")
)

// substitutions returns the commands inside $(...) and `...`, which a shell runs
// before the command that holds them.
func substitutions(command string) []string {
	dollars := dollarSubstitution.FindAllStringSubmatch(command, -1)
	ticks := backtickSubst.FindAllStringSubmatch(command, -1)
	out := make([]string, 0, len(dollars)+len(ticks))
	for _, m := range dollars {
		out = append(out, m[1])
	}
	for _, m := range ticks {
		out = append(out, m[1])
	}
	return out
}

// innerCommands returns the command string a shell, PowerShell, cmd or eval is
// asked to run, so `bash -c "gh pr merge 1"` is judged as `gh pr merge 1`.
func innerCommands(tokens []string) []string {
	prog := baseProgram(tokens[0])
	args := tokens[1:]
	switch prog {
	case "bash", "sh", "zsh", "dash", "ksh", "fish", "ash":
		for i, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") && i+1 < len(args) {
				return []string{args[i+1]}
			}
		}
	case "powershell", "pwsh":
		for i, a := range args {
			switch strings.ToLower(a) {
			case "-command", "-c":
				return []string{strings.Join(args[i+1:], " ")}
			case "-encodedcommand", "-e", "-ec":
				if i+1 < len(args) {
					if decoded, ok := decodePowerShell(args[i+1]); ok {
						return []string{decoded}
					}
					return []string{"__undecodable__ " + args[i+1]}
				}
			}
		}
	case "cmd":
		for i, a := range args {
			if la := strings.ToLower(a); la == "/c" || la == "/k" || la == "/r" {
				return []string{strings.Join(args[i+1:], " ")}
			}
		}
	case "eval":
		return []string{strings.Join(args, " ")}
	}
	return nil
}

// decodePowerShell reads a -EncodedCommand argument: base64 of UTF-16LE text.
func decodePowerShell(encoded string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw)%2 != 0 || len(raw) == 0 {
		return "", false
	}
	runes := make([]rune, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		runes = append(runes, rune(uint16(raw[i])|uint16(raw[i+1])<<8))
	}
	return string(runes), true
}

// tokenize splits a command on whitespace, keeping quoted text together and
// dropping the quotes.
func tokenize(segment string) []string {
	var tokens []string
	var cur strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			tokens = append(tokens, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, r := range segment {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	flush()
	return tokens
}

var envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// ciSkipEnv are variables that switch repository hooks and CI shortcuts off.
var ciSkipEnv = map[string]bool{"HUSKY": true, "SKIP": true, "SKIP_CI": true, "SKIP_CHECKS": true, "SKIP_HOOKS": true, "HUSKY_SKIP_HOOKS": true}

// stripWrappers removes environment assignments and the wrappers that only run
// another program (env, nohup, time, command, exec), so the program that matters
// is first. A hook-skipping assignment is kept visible by returning a marker.
func stripWrappers(tokens []string) []string {
	for len(tokens) > 0 {
		head := baseProgram(tokens[0])
		switch {
		case envAssignment.MatchString(tokens[0]):
			name := strings.SplitN(tokens[0], "=", 2)[0]
			if ciSkipEnv[strings.ToUpper(name)] {
				return []string{"__skip_hooks__"}
			}
			tokens = tokens[1:]
		case head == "env" || head == "nohup" || head == "time" || head == "command" || head == "exec" || head == "nice" || head == "stdbuf":
			tokens = tokens[1:]
		default:
			return tokens
		}
	}
	return tokens
}

// baseProgram is the program name: lower-case, without directory or extension.
func baseProgram(token string) string {
	token = strings.ReplaceAll(token, `\`, "/")
	token = strings.ToLower(path.Base(token))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		token = strings.TrimSuffix(token, ext)
	}
	return token
}

func hasFlag(tokens []string, flags ...string) bool {
	for _, t := range tokens {
		for _, f := range flags {
			if t == f {
				return true
			}
		}
	}
	return false
}

func containsAny(tokens []string, words ...string) bool {
	for _, t := range tokens {
		lt := strings.ToLower(t)
		for _, w := range words {
			if lt == w {
				return true
			}
		}
	}
	return false
}

var cloudCLIs = map[string]bool{"aws": true, "gcloud": true, "az": true, "doctl": true, "heroku": true, "vercel": true, "flyctl": true, "fly": true}

func classifySegment(tokens []string, env cmdEnv) Verdict {
	prog := baseProgram(tokens[0])
	if strings.HasPrefix(prog, "mkfs.") {
		prog = "mkfs"
	}
	args := tokens[1:]
	lowerArgs := make([]string, len(args))
	for i, a := range args {
		lowerArgs[i] = strings.ToLower(a)
	}
	switch prog {
	case "__undecodable__":
		return Verdict{Reason: "The command runs an encoded script AO cannot read."}
	case "__skip_hooks__":
		return deny(domain.HardLimitCI, "The command switches repository hooks or CI checks off.")
	case "sudo", "doas", "su", "runas", "gsudo", "pkexec":
		return deny(domain.HardLimitAdmin, "The command asks for administrator rights.")
	case "shutdown", "reboot", "halt", "poweroff", "bcdedit", "takeown", "icacls", "chown", "visudo", "usermod", "useradd", "userdel", "net1":
		return deny(domain.HardLimitAdmin, "The command changes the machine or its accounts.")
	case "net":
		if containsAny(lowerArgs, "user", "localgroup", "accounts") {
			return deny(domain.HardLimitAdmin, "The command changes the machine's accounts.")
		}
		if containsAny(lowerArgs, "use") {
			return deny(domain.HardLimitOtherPCs, "The command connects to another computer.")
		}
	case "reg":
		if containsAny(lowerArgs, "add", "delete", "import") {
			return deny(domain.HardLimitAdmin, "The command edits the registry.")
		}
	case "sc", "schtasks":
		if containsAny(lowerArgs, "create", "config", "delete", "/create", "/delete", "/change") {
			return deny(domain.HardLimitAdmin, "The command changes services or scheduled tasks.")
		}
	case "set-executionpolicy", "new-localuser", "add-localgroupmember", "enable-psremoting":
		return deny(domain.HardLimitAdmin, "The command changes the machine's security settings.")
	case "start-process":
		if containsAny(lowerArgs, "runas") {
			return deny(domain.HardLimitAdmin, "The command starts a process with elevated rights.")
		}
	case "mkfs", "format", "diskpart", "fdisk", "wipefs", "shred":
		return deny(domain.HardLimitDataDelete, "The command erases a disk or file system.")
	case "dd":
		for _, a := range lowerArgs {
			if strings.HasPrefix(a, "of=/dev/") || strings.HasPrefix(a, "of=\\\\.\\") {
				return deny(domain.HardLimitDataDelete, "The command writes straight to a disk.")
			}
		}
	case "rm", "rmdir", "del", "erase", "rd", "remove-item", "ri", "unlink":
		for _, a := range args {
			if isFlagToken(a) {
				continue
			}
			if reason := deletionOutside(env, a); reason != "" {
				return deny(domain.HardLimitDataDelete, "The command deletes "+a+", "+reason)
			}
		}
	case "find":
		if containsAny(lowerArgs, "-delete") {
			for _, a := range args {
				if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
					break
				}
				if reason := deletionOutside(env, a); reason != "" {
					return deny(domain.HardLimitDataDelete, "The command deletes files under "+a+", "+reason)
				}
			}
		}
	case "git":
		return classifyGit(args, lowerArgs)
	case "gh":
		return classifyGH(args, lowerArgs)
	case "ssh", "scp", "sftp", "mosh", "telnet", "psexec", "mstsc", "enter-pssession", "new-pssession", "nc", "ncat", "netcat":
		return deny(domain.HardLimitOtherPCs, "The command reaches another computer.")
	case "rsync":
		for _, a := range args {
			if strings.Contains(a, ":") && !strings.HasPrefix(a, "-") && !driveLetter.MatchString(a) {
				return deny(domain.HardLimitOtherPCs, "The command copies to or from another computer.")
			}
		}
	case "invoke-command":
		if containsAny(lowerArgs, "-computername", "-session", "-hostname") {
			return deny(domain.HardLimitOtherPCs, "The command runs on another computer.")
		}
	case "sendmail", "mail", "mailx", "mutt", "msmtp", "swaks", "mailsend", "blat", "send-mailmessage":
		return deny(domain.HardLimitMessaging, "The command sends email.")
	case "stripe", "paypal", "braintree":
		return deny(domain.HardLimitMoney, "The command talks to a payment service.")
	case "npm", "yarn", "pnpm", "bun":
		if containsAny(lowerArgs, "login", "adduser", "logout", "token", "owner") ||
			(containsAny(lowerArgs, "config") && containsAny(lowerArgs, "set") && hasSubstring(lowerArgs, "authtoken", "_auth", "password")) {
			return deny(domain.HardLimitCredentials, "The command changes package-registry logins.")
		}
		if containsAny(lowerArgs, "publish") {
			return deny(domain.HardLimitMessaging, "The command publishes a package for other people.")
		}
	case "docker", "podman":
		if containsAny(lowerArgs, "login", "logout") {
			return deny(domain.HardLimitCredentials, "The command changes a container-registry login.")
		}
	case "passwd", "chpasswd", "cmdkey", "security", "setx", "vault", "pass", "gpg", "ssh-add", "ssh-keygen", "keytool":
		if prog == "setx" && !hasSubstring(lowerArgs, "token", "key", "secret", "password", "credential") {
			return allow("An environment variable with no credential in its name.")
		}
		if prog == "security" && !containsAny(lowerArgs, "add-generic-password", "add-internet-password", "delete-generic-password", "delete-internet-password") {
			return allow("A keychain read-only command.")
		}
		if prog == "gpg" && !containsAny(lowerArgs, "--import", "--delete-secret-keys", "--delete-keys", "--gen-key", "--full-generate-key") {
			return allow("A gpg command that does not change keys.")
		}
		if prog == "pass" && !containsAny(lowerArgs, "insert", "generate", "rm", "edit") {
			return allow("A password-store read.")
		}
		if prog == "vault" && !containsAny(lowerArgs, "write", "delete", "login", "token") {
			return allow("A vault read.")
		}
		if prog == "ssh-keygen" && !hasSubstring(lowerArgs, ".ssh") {
			return allow("A key written to an explicit path outside the credential stores.")
		}
		if prog == "ssh-add" {
			return deny(domain.HardLimitCredentials, "The command loads a key into the agent.")
		}
		return deny(domain.HardLimitCredentials, "The command changes logins, keys or passwords.")
	case "aws", "gcloud", "az", "doctl", "heroku", "vercel", "flyctl", "fly", "kubectl":
		if cloudCLIs[prog] && cloudSpendsMoney(prog, lowerArgs) {
			return deny(domain.HardLimitMoney, "The command changes billing or buys something.")
		}
		if containsAny(lowerArgs, "configure", "login", "logout", "auth", "set-credentials", "sso") {
			return deny(domain.HardLimitCredentials, "The command changes a cloud login.")
		}
	case "curl", "wget", "http", "https", "invoke-webrequest", "invoke-restmethod", "iwr", "irm":
		// Hosts that send messages were already refused for the whole command.
	}
	return allow("A command with no hard-limit effect.")
}

// cloudSpendsMoney reads the service and operation words of a cloud CLI, the
// first two non-option tokens, so a file that merely has "billing" in its name
// does not count. It flags buying (purchase-, buy-, subscribe) and the billing
// service itself.
func cloudSpendsMoney(prog string, lowerArgs []string) bool {
	var words []string
	for _, a := range lowerArgs {
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
		if len(words) == 3 {
			break
		}
	}
	for i, w := range words {
		switch {
		case w == "billing" || w == "payment" || w == "payments":
			return true
		case prog == "aws" && i >= 1 && (strings.HasPrefix(w, "purchase-") || strings.HasPrefix(w, "buy-") || strings.Contains(w, "subscribe")):
			return true
		case prog != "aws" && (strings.HasPrefix(w, "purchase") || w == "subscribe"):
			return true
		}
	}
	return false
}

// isFlagToken reports an option (-rf, --force, -Recurse) or a Windows switch
// (/q, /s), which names no path.
func isFlagToken(a string) bool {
	return strings.HasPrefix(a, "-") || (len(a) == 2 && strings.HasPrefix(a, "/"))
}

func hasSubstring(tokens []string, words ...string) bool {
	for _, t := range tokens {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
	}
	return false
}

// protectedBranches are the branches whose history the user owns.
var protectedBranches = map[string]bool{"main": true, "master": true, "trunk": true, "production": true, "prod": true, "release": true}

func protectedRef(ref string) bool {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "+"), "refs/heads/")
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		ref = strings.TrimPrefix(strings.TrimPrefix(ref[i+1:], "refs/heads/"), "+")
	}
	ref = strings.TrimPrefix(ref, "origin/")
	return protectedBranches[strings.ToLower(ref)]
}

func classifyGit(args, lower []string) Verdict {
	// Skip git's global options to find the subcommand.
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "-C" || args[i] == "-c" || args[i] == "--git-dir" || args[i] == "--work-tree" || args[i] == "--namespace":
			i += 2
		case strings.HasPrefix(args[i], "-"):
			i++
		default:
			goto found
		}
	}
	return allow("A git command with no hard-limit effect.")
found:
	sub := lower[i]
	rest := args[i+1:]
	restLower := lower[i+1:]
	switch sub {
	case "push":
		if hasFlag(restLower, "--no-verify") {
			return deny(domain.HardLimitCI, "The push skips the repository's pre-push checks.")
		}
		if hasFlag(restLower, "--mirror", "--all") {
			return deny(domain.HardLimitMainPush, "The push would update the default branch along with the others.")
		}
		var refs []string
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				refs = append(refs, a)
			}
		}
		// refs[0] is the remote; everything after it is a refspec.
		for _, ref := range refs {
			if protectedRef(ref) {
				return deny(domain.HardLimitMainPush, "The push targets the default branch.")
			}
		}
		if hasFlag(restLower, "--delete", "-d") {
			for _, ref := range refs {
				if protectedRef(ref) {
					return deny(domain.HardLimitMainPush, "The command deletes the default branch.")
				}
			}
		}
	case "commit":
		if hasFlag(restLower, "--no-verify", "-n") {
			return deny(domain.HardLimitCI, "The commit skips the repository's commit checks.")
		}
		for _, a := range restLower {
			if strings.Contains(a, "[skip ci]") || strings.Contains(a, "[ci skip]") || strings.Contains(a, "[no ci]") || strings.Contains(a, "skip-checks:") {
				return deny(domain.HardLimitCI, "The commit message tells CI to skip this change.")
			}
		}
	case "config":
		if hasSubstring(restLower, "credential", "password", "extraheader", "sshcommand", "askpass") {
			return deny(domain.HardLimitCredentials, "The command changes git logins.")
		}
	case "credential", "credential-manager", "credential-cache", "credential-store":
		return deny(domain.HardLimitCredentials, "The command reads or changes stored git logins.")
	case "merge":
		// A local merge into the session's own branch is ordinary work.
	}
	return allow("A git command with no hard-limit effect.")
}

var ghWriteMethod = regexp.MustCompile(`(?i)(-X|--method)[ =]?(POST|PUT|PATCH|DELETE)`)

func classifyGH(args, lower []string) Verdict {
	if len(lower) == 0 {
		return allow("A gh command with no hard-limit effect.")
	}
	group := lower[0]
	sub := ""
	if len(lower) > 1 {
		sub = lower[1]
	}
	rest := args[min(2, len(args)):]
	switch group {
	case "auth":
		if sub != "status" {
			return deny(domain.HardLimitCredentials, "The command changes the GitHub login.")
		}
	case "pr":
		switch sub {
		case "merge":
			if hasFlag(lower, "--admin") {
				return deny(domain.HardLimitCI, "The merge bypasses the branch's required checks and reviews.")
			}
			ref := ""
			for _, a := range rest {
				if !strings.HasPrefix(a, "-") {
					ref = a
					break
				}
			}
			return Verdict{Merge: true, MergeRefs: []string{ref}, Reason: "A merge is decided against the PR's review and checks."}
		case "comment", "review":
			return deny(domain.HardLimitMessaging, "The command posts on a pull request other people read.")
		}
	case "issue":
		switch sub {
		case "create", "comment", "delete", "transfer", "lock", "pin":
			return deny(domain.HardLimitMessaging, "The command posts on an issue other people read.")
		}
	case "workflow":
		if sub == "disable" {
			return deny(domain.HardLimitCI, "The command switches a CI workflow off.")
		}
	case "run":
		if sub == "cancel" || sub == "delete" {
			return deny(domain.HardLimitCI, "The command cancels or removes CI runs.")
		}
	case "secret", "ssh-key", "gpg-key", "variable":
		if sub == "set" || sub == "add" || sub == "delete" || sub == "remove" {
			return deny(domain.HardLimitCredentials, "The command changes repository secrets or keys.")
		}
	case "repo":
		switch sub {
		case "delete":
			return deny(domain.HardLimitDataDelete, "The command deletes a repository.")
		case "deploy-key", "edit", "archive", "rename":
			return deny(domain.HardLimitAdmin, "The command changes repository administration.")
		}
	case "release":
		if sub == "delete" || sub == "create" || sub == "upload" {
			return deny(domain.HardLimitMessaging, "The command publishes or removes a release other people see.")
		}
	case "api":
		joined := strings.ToLower(strings.Join(args, " "))
		write := ghWriteMethod.MatchString(joined) || hasFlag(lower, "-f", "-F", "--field", "--raw-field", "--input")
		switch {
		case strings.Contains(joined, "/protection") || strings.Contains(joined, "/rulesets"):
			if write {
				return deny(domain.HardLimitCI, "The call changes branch protection.")
			}
		case strings.Contains(joined, "/billing"):
			return deny(domain.HardLimitMoney, "The call touches billing.")
		case strings.Contains(joined, "/keys") || strings.Contains(joined, "/secrets") || strings.Contains(joined, "/hooks"):
			if write {
				return deny(domain.HardLimitCredentials, "The call changes keys, secrets or webhooks.")
			}
		case strings.Contains(joined, "/collaborators") || strings.Contains(joined, "/invitations") || strings.Contains(joined, "/memberships"):
			if write {
				return deny(domain.HardLimitAdmin, "The call changes who has access.")
			}
		case strings.Contains(joined, "/comments") || strings.Contains(joined, "/reviews") || strings.Contains(joined, "/issues"):
			if write {
				return deny(domain.HardLimitMessaging, "The call posts where other people read.")
			}
		}
	}
	return allow("A gh command with no hard-limit effect.")
}

// messagingHosts are the services whose endpoints send messages to people.
var messagingHost = regexp.MustCompile(`(?i)(hooks\.slack\.com|slack\.com/api|api\.telegram\.org|discord(app)?\.com/api/webhooks|api\.twilio\.com|api\.sendgrid\.com|api\.mailgun\.net|api\.postmarkapp\.com|graph\.microsoft\.com/[^ ]*sendmail|smtp://|smtps://|api\.github\.com/repos/[^ ]*/(comments|reviews)|api\.whatsapp\.com)`)

func messagingEndpoint(command string) (domain.HardLimit, bool) {
	if messagingHost.MatchString(command) {
		return domain.HardLimitMessaging, true
	}
	return "", false
}

// ClassifyText reports the hard limit a decision's choice would cross, judged by
// the words alone. It is a backstop for `ao autopilot decide`, not the main
// control: the instructions the agent was given are. A choice that says it will
// not do the thing (not, never, without, instead of, rather than) is never
// flagged, so refusing a limit in writing does not trip it.
func ClassifyText(text string) (domain.HardLimit, bool) {
	t := strings.ToLower(text)
	if negated.MatchString(t) {
		return "", false
	}
	for _, rule := range textRules {
		if rule.pattern.MatchString(t) {
			return rule.limit, true
		}
	}
	return "", false
}

var negated = regexp.MustCompile(`\b(not|never|don't|dont|do not|without|instead of|rather than|avoid|no need to|skip the idea|won't|refrain)\b`)

var textRules = []struct {
	limit   domain.HardLimit
	pattern *regexp.Regexp
}{
	// A few words may sit between the verb and what it buys ("upgrade the paid CI
	// plan", "buy more CI minutes"); the objects are the things that cost money.
	{domain.HardLimitMoney, regexp.MustCompile(`\b(buy|purchase|subscribe to|pay for|renew|top up|upgrade)\b[^.]{0,40}\b(plan|subscription|licen[cs]es?|credits?|minutes|seats?|tier|billing|payment|card)\b|\badd a (credit|payment) card\b|\bchange (the )?billing\b|\benter (a )?payment\b`)},
	{domain.HardLimitCI, regexp.MustCompile(`\b(disable|skip|bypass|turn off|remove|weaken|silence|ignore)\s+(the\s+)?(\w+\s+){0,2}(ci|checks?|pipeline|workflow|hooks|pre-commit|review)\b|--no-verify|\[skip ci\]|\badmin merge\b|\bforce[- ]merge\b`)},
	{domain.HardLimitMainPush, regexp.MustCompile(`\b(push|commit|merge)\b[^.]{0,40}\b(directly )?(to|into|on) (the )?(main|master|default branch)\b[^.]{0,30}\b(without|no) (a )?(review|pr|pull request)\b|\bforce[- ]push\b`)},
	{domain.HardLimitDataDelete, regexp.MustCompile(`\b(delete|wipe|erase|remove|format)\b[^.]{0,40}\b(outside|home directory|my documents|other projects?|another repo(sitory)?|the database|production data|backups?)\b`)},
	{domain.HardLimitAdmin, regexp.MustCompile(`\b(run as admin(istrator)?|use sudo|with sudo|elevate|elevated rights|administrator rights|grant admin)\b`)},
	{domain.HardLimitMessaging, regexp.MustCompile(`\b(email|e-mail|message|dm|slack|post a comment|comment on (the )?(issue|pr)|notify|tell)\b[^.]{0,30}\b(the )?(customer|client|team|colleague|reviewer|maintainer|everyone|stakeholders?|them|him|her|people)\b`)},
	{domain.HardLimitCredentials, regexp.MustCompile(`\b(change|reset|rotate|replace|update|log in with|login with|share|paste|commit) (the |a |my |your )?(password|token|credentials?|api key|secret|login)s?\b`)},
	{domain.HardLimitOtherPCs, regexp.MustCompile(`\b(ssh|log in|login|connect) (in)?to (the |another |a )?(other |second |remote )?(pc|computer|machine|server|laptop)\b|\banother pc'?s? credentials?\b`)},
}
