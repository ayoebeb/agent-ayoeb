package autopilot

import (
	"path"
	"regexp"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Deny by default for what cannot be undone.
//
// The parser in policy.go understands a lot of shell, but a parser can always be
// walked around. So a command that deletes or merges is never allowed because
// the parser found nothing wrong with it: it is allowed only when it has one
// narrow shape AO can prove (canonicalDelete, canonicalMerge). Every other
// command that deletes or merges is refused with its hard limit, which parks
// that one item for the user.
//
// Merging goes only through AO's own guarded command:
//
//	ao pr merge <number> --url <full pull request URL> --expected-head-sha <sha>
//
// where the daemon itself re-reads the pull request, requires the AO review and
// the checks to be green on that exact head, and refuses a changed head. Autopilot
// never parses `gh pr merge` (global flags, GH_REPO, hosts, aliases): every `gh`
// merge, whatever its shape, is parked.

// deleteWords are the words that mean "remove files" wherever they appear in a
// command, quoted or not, in a nested script or a substitution.
var deleteWords = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "erase": true, "rd": true, "remove-item": true, "ri": true,
	"unlink": true, "shred": true, "-delete": true, "rimraf": true, "trash": true,
}

// dynamicWords run text that is only known when the command runs, so AO cannot
// say what they delete or merge.
var dynamicWords = map[string]bool{"eval": true, "invoke-expression": true, "iex": true}

var (
	wordPattern = regexp.MustCompile(`[a-z0-9_.\-]+`)
	// commandFromVariable is a command whose program comes from a variable or a
	// substitution: `$TOOL -rf data`, `$(which rm) data`.
	commandFromVariable = regexp.MustCompile("(^|[;&|(\\n{]|\\bdo|\\bthen|\\belse)\\s*[$`]")
	// plainCommand rejects everything that chains, redirects, groups, expands or
	// globs; a canonical command is one plain command.
	plainCommand = regexp.MustCompile("[;&|<>()$`{}*?\\[\\]~%#\\n\\r!]")
	// powershellSyntax adds what PowerShell reads as syntax inside an argument:
	// splatting (@), drive and parameter-value colons, and comma lists, which make
	// one word several paths. A canonical delete has none of them.
	powershellSyntax = regexp.MustCompile("[@:,]")
	// executableExtensions are stripped from a program name before it is compared.
	executableExtensions = []string{".exe", ".cmd", ".bat", ".ps1", ".com"}
)

// normalizeProgram reduces a program word to the name the shell would run, so
// `RM.EXE`, `/bin/rm`, `C:\Tools\rm.cmd` and `rm` are all `rm`: the last path
// element, lower case, without an executable extension.
func normalizeProgram(word string) string {
	word = strings.ToLower(strings.TrimSpace(word))
	word = strings.ReplaceAll(word, `\`, "/")
	word = path.Base(word)
	for changed := true; changed; {
		changed = false
		for _, ext := range executableExtensions {
			if strings.HasSuffix(word, ext) && len(word) > len(ext) {
				word = strings.TrimSuffix(word, ext)
				changed = true
			}
		}
	}
	return word
}

// commandWords cuts a command into normalized words. Two readings are taken and
// joined: one with quotes, backslashes and carets removed (so `r""m` and `r\m`
// read as `rm`), one that treats backslashes as path separators (so
// `C:\Tools\rm.exe` reads as `rm`).
func commandWords(command string) []string {
	lower := strings.ToLower(command)
	stripped := strings.NewReplacer("'", "", `"`, "", "`", "", `\`, "", "^", "").Replace(lower)
	pathy := strings.NewReplacer("'", "", `"`, "", "`", "", "^", "", `\`, "/").Replace(lower)
	var out []string
	for _, text := range []string{stripped, pathy} {
		for _, w := range wordPattern.FindAllString(text, -1) {
			out = append(out, w, normalizeProgram(w))
		}
	}
	return out
}

// destructiveKinds reports what a command, anywhere in its text, would do.
func destructiveKinds(command string) (deletes, merges, dynamic bool) {
	set := map[string]bool{}
	for _, w := range commandWords(command) {
		set[w] = true
		if deleteWords[w] {
			deletes = true
		}
		if dynamicWords[w] {
			dynamic = true
		}
	}
	if set["git"] && set["clean"] {
		deletes = true
	}
	merges = set["merge"] && (set["gh"] || (set["ao"] && set["pr"]))
	if commandFromVariable.MatchString(command) {
		dynamic = true
	}
	return deletes, merges, dynamic
}

// gateDestructive applies the deny-by-default rule to a shell command the parser
// allowed. A refusal the parser already made stands.
func gateDestructive(a Action, v Verdict) Verdict {
	if !v.Allow {
		return v
	}
	deletes, merges, dynamic := destructiveKinds(a.Command)
	switch {
	case !deletes && !merges && !dynamic:
		return v
	case deletes && merges:
		return deny(domain.HardLimitDataDelete, "The command both deletes and merges; AO allows only one plain delete or one `ao pr merge`, so this one is parked for you.")
	case merges:
		if target, ok := canonicalMerge(a.Command); ok {
			return Verdict{Merge: true, MergeTargets: []MergeTarget{target}, Reason: "AO's own guarded merge: the daemon checks the review and the checks on the exact head."}
		}
		return deny(domain.HardLimitMainPush, "Autopilot merges only with `ao pr merge <number> --url <pull request URL> --expected-head-sha <sha>`; every other merge, `gh pr merge` included, is parked for you.")
	case deletes:
		if targets, ok := canonicalDeleteTargets(a); ok {
			v := allow("A plain delete of literal targets inside the workspace.")
			v.DeleteTargets = targets
			return v
		}
		return deny(domain.HardLimitDataDelete, "AO only lets Autopilot delete with one plain rm, rmdir, del or Remove-Item on literal targets inside the workspace, with no cd, chaining, substitution, variables or PowerShell syntax; any other delete is parked for you.")
	default:
		return deny(domain.HardLimitDataDelete, "The command takes its program from a variable, a substitution or eval, so AO cannot tell that it does not delete or merge; it is parked for you.")
	}
}

// deleteFlags are the options a canonical delete may carry. They only say how
// hard to delete, never where.
var deleteFlags = map[string]bool{
	"-r": true, "-f": true, "-rf": true, "-fr": true, "-v": true, "-d": true, "-i": true,
	"-rv": true, "-fv": true, "-rfv": true, "-frv": true,
	"--recursive": true, "--force": true, "--verbose": true, "--dir": true,
	"-recurse": true, "-force": true,
	"/s": true, "/q": true, "/f": true,
}

// deletePathFlags are the PowerShell parameters that carry a literal path as the
// next word.
var deletePathFlags = map[string]bool{"-path": true, "-literalpath": true}

// deleteProgramAliases maps every way to say "delete" to the one name it is
// allowed under; a program that is not one of these is not a delete AO allows.
var deleteProgramAliases = map[string]string{
	"rm": "rm", "rmdir": "rm", "rd": "rm", "del": "rm", "erase": "rm", "unlink": "rm",
	"ri": "rm", "remove-item": "rm",
}

// hasParentSegment reports whether a path, as written, has a ".." segment.
func hasParentSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// absoluteTarget is the lexically cleaned absolute path a target names, relative
// targets taken from the directory the command runs in (the provider's cwd, else
// the workspace).
func absoluteTarget(workspace, cwd, target string) string {
	dir := normalizePath(strings.TrimSpace(cwd))
	if dir == "" {
		dir = normalizePath(workspace)
	}
	if !isAbsolute(dir) {
		dir = path.Join(normalizePath(workspace), dir)
	}
	if abs := resolveIn(dir, target); abs != "" {
		return abs
	}
	return normalizePath(target)
}

// canonicalDeleteTargets reports whether the command is one plain delete and, if
// so, the absolute paths it deletes. A bare
// rm/rmdir/del/erase/rd/ri/Remove-Item written as exactly that word, only the
// flags above (and -Path or -LiteralPath with its value), and at least one
// literal target that lies strictly inside the workspace. The directory it runs
// in is the one the provider named (or the workspace), never one the command
// changes. A command with cd, chaining, a subshell, a substitution, an
// environment assignment, a glob, PowerShell syntax ($ @ ` : ( ) { } ; | ,) or a
// directory or extension on the program name is not canonical.
func canonicalDeleteTargets(a Action) ([]string, bool) {
	command := strings.TrimSpace(a.Command)
	if command == "" || plainCommand.MatchString(command) || powershellSyntax.MatchString(command) {
		return nil, false
	}
	tokens := tokenize(command)
	if len(tokens) < 2 || !literalWords(command, tokens, 1) {
		return nil, false
	}
	// The program is the bare word: no directory, no extension. A name with
	// either is normalized (and gated) but never auto-approved, because a file
	// by that name in the working directory would be run instead of the tool.
	if strings.ContainsAny(tokens[0], `/\=.`) || deleteProgramAliases[strings.ToLower(tokens[0])] == "" {
		return nil, false
	}
	dir := strings.TrimSpace(a.Cwd)
	if dir == "" {
		dir = a.Workspace
	}
	dir = normalizePath(dir)
	if dir == "" {
		return nil, false
	}
	if !isAbsolute(dir) {
		dir = path.Join(normalizePath(a.Workspace), dir)
	}
	if hasParentSegment(a.Cwd) {
		return nil, false
	}
	root := normalizePath(a.Workspace)
	var targets []string
	target := func(tok string) bool {
		// A ".." is never cleaned away before the disk is consulted: cleaning
		// `link/../x` to `x` forgets that the link may lead elsewhere, so the
		// target would be judged somewhere it is not. A target that names a
		// parent directory is parked as written.
		if hasParentSegment(tok) {
			return false
		}
		abs := resolveIn(dir, tok)
		if abs == "" || !insideWorkspace(a.Workspace, abs) || sameDir(abs, root) {
			return false
		}
		targets = append(targets, abs)
		return true
	}
	for i := 1; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case deletePathFlags[strings.ToLower(tok)]:
			if i+1 >= len(tokens) || strings.HasPrefix(tokens[i+1], "-") || !target(tokens[i+1]) {
				return nil, false
			}
			i++
		case strings.HasPrefix(tok, "-") || (len(tok) == 2 && strings.HasPrefix(tok, "/")):
			if !deleteFlags[strings.ToLower(tok)] {
				return nil, false
			}
		default:
			if !target(tok) {
				return nil, false
			}
		}
	}
	return targets, len(targets) > 0
}

// literalWords reports whether the first n words of the command are written
// exactly as the tokenizer read them: no quote, backslash or caret inside the
// program or the subcommand, so `r""m` and `a\o` are not taken for rm and ao.
func literalWords(command string, tokens []string, n int) bool {
	fields := strings.Fields(command)
	if len(fields) < n || len(tokens) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if fields[i] != tokens[i] {
			return false
		}
	}
	return true
}

// sameDir compares two cleaned paths, ignoring case on Windows drive paths.
func sameDir(a, b string) bool {
	if caseInsensitive(a) || caseInsensitive(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

var (
	prNumber = regexp.MustCompile(`^\d+$`)
	prURL    = regexp.MustCompile(`^https://[A-Za-z0-9.\-]+(:\d+)?/[A-Za-z0-9_.\-]+/[A-Za-z0-9_.\-]+/pull/(\d+)$`)
	headSHA  = regexp.MustCompile(`^[0-9a-fA-F]{40}$|^[0-9a-fA-F]{64}$`)
)

// canonicalMerge reads AO's own merge command and nothing else:
//
//	ao pr merge <number> --url <full pull request URL> --expected-head-sha <sha>
//
// with the two flags in either order, once each, the number equal to the one in
// the URL and the SHA a full commit id. No other flag, no environment
// assignment, no wrapper, no chaining. The daemon behind the command re-reads
// the pull request and refuses unless the AO review and the checks are green on
// exactly that head; the service additionally checks that the pull request is
// one this run tracks.
func canonicalMerge(command string) (MergeTarget, bool) {
	command = strings.TrimSpace(command)
	if command == "" || plainCommand.MatchString(command) {
		return MergeTarget{}, false
	}
	tokens := tokenize(command)
	if len(tokens) != 8 || !literalWords(command, tokens, 3) || tokens[0] != "ao" || tokens[1] != "pr" || tokens[2] != "merge" {
		return MergeTarget{}, false
	}
	number := tokens[3]
	if !prNumber.MatchString(number) {
		return MergeTarget{}, false
	}
	var url, sha string
	for i := 4; i < len(tokens); i += 2 {
		switch tokens[i] {
		case "--url":
			if url != "" {
				return MergeTarget{}, false
			}
			url = tokens[i+1]
		case "--expected-head-sha":
			if sha != "" {
				return MergeTarget{}, false
			}
			sha = tokens[i+1]
		default:
			return MergeTarget{}, false
		}
	}
	m := prURL.FindStringSubmatch(url)
	if len(m) < 3 || m[2] != number || !headSHA.MatchString(sha) {
		return MergeTarget{}, false
	}
	return MergeTarget{Ref: url, Number: number, HeadSHA: strings.ToLower(sha)}, true
}
