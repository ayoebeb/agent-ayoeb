// Package permissionmemory remembers the tool permissions a user chose "Always
// allow" for, per project. The rules live in the daemon's own store; every new
// Claude Code session workspace of the project gets them written into its
// .claude/settings.local.json, and revoking one removes it again.
package permissionmemory

import (
	"encoding/json"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// maxLabelLen keeps a pasted multi-line command from flooding the list.
const maxLabelLen = 96

// candidate is one rule a decision applies, before the policy has judged it.
type candidate struct {
	tool    string
	content string
}

// candidatesFromDecision reads the permission rules the chosen "always" option applies
// from the option's raw ACP encoding (claude-agent-acp puts them in
// _meta.permission). Only rules Claude Code persists to the session workspace
// qualify: an in-memory rule or a permission-mode switch lives and dies with its
// process and is never remembered.
func candidatesFromDecision(raw []byte) []candidate {
	var option struct {
		Kind string `json:"kind"`
		Meta struct {
			Permission struct {
				Changes []struct {
					Type         string `json:"type"`
					Operation    string `json:"operation"`
					RuleBehavior string `json:"ruleBehavior"`
					Lifetime     struct {
						Scope string `json:"scope"`
					} `json:"lifetime"`
					Targets []struct {
						Type     string `json:"type"`
						ToolName string `json:"toolName"`
						Matcher  *struct {
							Provider string `json:"provider"`
							Value    string `json:"value"`
						} `json:"matcher"`
					} `json:"targets"`
				} `json:"changes"`
			} `json:"permission"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &option); err != nil || option.Kind != "allow_always" {
		return nil
	}
	var out []candidate
	for _, change := range option.Meta.Permission.Changes {
		if change.Type != "policy_rule" || change.Operation != "add" ||
			change.RuleBehavior != "allow" || change.Lifetime.Scope != "persistent" {
			continue
		}
		for _, target := range change.Targets {
			if target.Type != "tool" || target.ToolName == "" {
				continue
			}
			content := ""
			if target.Matcher != nil {
				if target.Matcher.Provider != "claudeCode" {
					continue
				}
				content = target.Matcher.Value
			}
			out = append(out, candidate{tool: target.ToolName, content: content})
		}
	}
	return out
}

// classify decides whether a rule may be remembered for the whole project and
// how to show it. This is the hard line between what one session may grant
// itself and what every future session of the project inherits, so it is an
// allowlist: anything it does not recognize stays in the workspace it was
// chosen in. In particular a wildcard for a program that deletes or moves data,
// reaches the network, or runs another command line (rm, mv, chmod, git push,
// curl, ssh, shells, ...) is never remembered.
func classify(c candidate) (domain.ProjectPermissionRuleKind, string, bool) {
	switch {
	case c.tool == "Bash":
		return classifyBash(c.content)
	case strings.HasPrefix(c.tool, "mcp__"):
		return classifyMCP(c)
	default:
		return "", "", false
	}
}

func classifyMCP(c candidate) (domain.ProjectPermissionRuleKind, string, bool) {
	if c.content != "" {
		return "", "", false
	}
	parts := strings.Split(c.tool, "__")
	switch {
	case len(parts) == 2 && parts[1] != "":
		return domain.ProjectPermissionRuleServer, label(parts[1]), true
	case len(parts) >= 3 && parts[1] != "" && parts[2] != "":
		return domain.ProjectPermissionRuleTool, label(parts[1] + " / " + strings.Join(parts[2:], "__")), true
	default:
		return "", "", false
	}
}

func classifyBash(content string) (domain.ProjectPermissionRuleKind, string, bool) {
	content = strings.TrimSpace(content)
	// An unescaping rule for a backslash is not known, and a rule that does not
	// round-trip into a settings file would silently allow nothing.
	if content == "" || strings.Contains(content, `\`) || strings.ContainsAny(content, "\r\n") {
		return "", "", false
	}
	if !strings.Contains(content, "*") {
		return domain.ProjectPermissionRuleExact, label(content), true
	}
	prefix, ok := strings.CutSuffix(content, " *")
	if !ok {
		prefix, ok = strings.CutSuffix(content, ":*")
	}
	if !ok || strings.Contains(prefix, "*") || !commandKindAllowed(strings.Fields(prefix)) {
		return "", "", false
	}
	return domain.ProjectPermissionRuleCommand, label(strings.Join(strings.Fields(prefix), " ") + " ..."), true
}

// interpreters are the programs AO offers a command-kind choice for ("node -e *",
// "python script.py *"): the person means "this kind of run".
var interpreters = map[string]bool{"node": true, "python": true, "python3": true, "py": true, "pytest": true}

// subcommandKinds are the build, test and read-or-stage subcommands whose
// "<program> <subcommand> *" rule Claude Code itself suggests and that cannot
// delete, publish or run an arbitrary command line.
var subcommandKinds = map[string]bool{
	"git add": true, "git commit": true, "git status": true, "git diff": true, "git log": true, "git show": true,
	"npm run": true, "npm test": true, "pnpm run": true, "pnpm test": true, "yarn test": true,
	"go test": true, "go build": true, "go vet": true,
	"cargo test": true, "cargo build": true, "cargo check": true,
}

func commandKindAllowed(words []string) bool {
	if len(words) < 2 {
		return false
	}
	program := strings.TrimSuffix(strings.ToLower(words[0]), ".exe")
	switch {
	case interpreters[program]:
		return true
	case program == "make":
		return len(words) == 2
	default:
		return len(words) == 2 && subcommandKinds[program+" "+strings.ToLower(words[1])]
	}
}

func label(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > maxLabelLen {
		return string(runes[:maxLabelLen-3]) + "..."
	}
	return text
}
