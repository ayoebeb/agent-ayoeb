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
// walked around (a conditional cd, a global flag, an environment variable that
// picks the repository, a host that is not github.com). So a command that deletes
// or merges is never allowed because the parser found nothing wrong with it: it
// is allowed only when it has one narrow shape AO can prove (see
// canonicalDelete and canonicalMerge). Every other command that deletes or
// merges is refused with its hard limit, which parks that one item for the user.

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
)

// words lower-cases a command and cuts it into words, with quotes, backslashes
// and carets removed first so `r""m` and `r\m` read as `rm`.
func commandWords(command string) []string {
	cleaned := strings.NewReplacer("'", "", `"`, "", "`", "", `\`, "", "^", "").Replace(strings.ToLower(command))
	return wordPattern.FindAllString(cleaned, -1)
}

// destructiveKinds reports what a command, anywhere in its text, would do.
func destructiveKinds(command string, parsedMerge bool) (deletes, merges, dynamic bool) {
	words := commandWords(command)
	set := make(map[string]bool, len(words))
	for _, w := range words {
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
	merges = parsedMerge || ((set["gh"] || set["gh.exe"]) && set["merge"])
	if commandFromVariable.MatchString(command) {
		dynamic = true
	}
	return deletes, merges, dynamic
}

// gateDestructive applies the deny-by-default rule to a shell command the parser
// allowed or recognised as a merge. A refusal the parser already made stands.
func gateDestructive(a Action, v Verdict) Verdict {
	if !v.Allow && !v.Merge {
		return v
	}
	deletes, merges, dynamic := destructiveKinds(a.Command, v.Merge)
	switch {
	case !deletes && !merges && !dynamic:
		return v
	case deletes && merges:
		return deny(domain.HardLimitDataDelete, "The command both deletes and merges; AO allows only a plain single delete or a plain single merge, so this one is parked for you.")
	case merges:
		if target, ok := canonicalMerge(a.Command); ok {
			return Verdict{Merge: true, MergeTargets: []MergeTarget{target}, Reason: "A merge is decided against the PR's review and checks."}
		}
		return deny(domain.HardLimitMainPush, "AO only lets Autopilot merge with exactly `gh pr merge <number or pull request URL>` and --squash, --merge, --rebase or --match-head-commit; any other merge is parked for you.")
	case deletes:
		if canonicalDelete(a) {
			return allow("A plain delete of literal targets inside the workspace.")
		}
		return deny(domain.HardLimitDataDelete, "AO only lets Autopilot delete with one plain rm, rmdir, del or Remove-Item on literal targets inside the workspace, with no cd, chaining, substitution or variables; any other delete is parked for you.")
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

var deletePrograms = map[string]bool{
	"rm": true, "rmdir": true, "del": true, "erase": true, "rd": true, "remove-item": true, "ri": true, "unlink": true,
}

// canonicalDelete reports whether the command is one plain delete: a bare
// rm/rmdir/del/erase/rd/Remove-Item, only the flags above, and at least one
// literal target that lies strictly inside the workspace. The directory it runs
// in is the one the provider named (or the workspace), never one the command
// changes: a command with cd, chaining, a subshell, a substitution, an
// environment assignment or a glob is not canonical.
func canonicalDelete(a Action) bool {
	command := strings.TrimSpace(a.Command)
	if command == "" || plainCommand.MatchString(command) {
		return false
	}
	tokens := tokenize(command)
	if len(tokens) < 2 || !literalWords(command, tokens, 1) || strings.ContainsAny(tokens[0], `/\=`) || !deletePrograms[strings.ToLower(tokens[0])] {
		return false
	}
	dir := strings.TrimSpace(a.Cwd)
	if dir == "" {
		dir = a.Workspace
	}
	dir = normalizePath(dir)
	if dir == "" {
		return false
	}
	if !isAbsolute(dir) {
		dir = path.Join(normalizePath(a.Workspace), dir)
	}
	root := normalizePath(a.Workspace)
	targets := 0
	for _, tok := range tokens[1:] {
		if strings.HasPrefix(tok, "-") || (len(tok) == 2 && strings.HasPrefix(tok, "/")) {
			if !deleteFlags[strings.ToLower(tok)] {
				return false
			}
			continue
		}
		abs := resolveIn(dir, tok)
		if abs == "" || !insideWorkspace(a.Workspace, abs) || sameDir(abs, root) {
			return false
		}
		targets++
	}
	return targets > 0
}

// literalWords reports whether the first n words of the command are written
// exactly as the tokenizer read them: no quote, backslash or caret inside the
// program or the subcommand, so `r""m` and `g\h` are not taken for rm and gh.
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
	prURL    = regexp.MustCompile(`^https://[A-Za-z0-9.\-]+(:\d+)?/[A-Za-z0-9_.\-]+/[A-Za-z0-9_.\-]+/pull/\d+/?$`)
	headSHA  = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

// canonicalMerge reads `gh pr merge <number or pull request URL>` with only
// --squash, --merge, --rebase or --match-head-commit <sha>, and nothing else:
// no global flag before the subcommand, no environment assignment, no -R/--repo,
// no chaining. The repository and host are then the ones the number or URL
// names, which the service compares to the tracked pull request.
func canonicalMerge(command string) (MergeTarget, bool) {
	command = strings.TrimSpace(command)
	if command == "" || plainCommand.MatchString(command) {
		return MergeTarget{}, false
	}
	tokens := tokenize(command)
	if len(tokens) < 4 || !literalWords(command, tokens, 3) || tokens[0] != "gh" || tokens[1] != "pr" || tokens[2] != "merge" {
		return MergeTarget{}, false
	}
	ref := tokens[3]
	if !prNumber.MatchString(ref) && !prURL.MatchString(ref) {
		return MergeTarget{}, false
	}
	rest := tokens[4:]
	for i := 0; i < len(rest); i++ {
		switch flag := rest[i]; {
		case flag == "--squash" || flag == "--merge" || flag == "--rebase":
		case flag == "--match-head-commit":
			if i+1 >= len(rest) || !headSHA.MatchString(rest[i+1]) {
				return MergeTarget{}, false
			}
			i++
		case strings.HasPrefix(flag, "--match-head-commit="):
			if !headSHA.MatchString(strings.TrimPrefix(flag, "--match-head-commit=")) {
				return MergeTarget{}, false
			}
		default:
			return MergeTarget{}, false
		}
	}
	return MergeTarget{Ref: ref}, true
}
