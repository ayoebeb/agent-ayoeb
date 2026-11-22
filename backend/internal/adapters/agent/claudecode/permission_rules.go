package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
)

// AddPermissionRules adds rules (in Claude Code's own spelling, such as
// `Bash(node -e *)`) to the permissions.allow list of the workspace's
// .claude/settings.local.json, the file Claude Code itself writes "always allow"
// choices to. A rule already present is not added twice, and every other key,
// including AO's hooks, is kept as it was.
func AddPermissionRules(workspacePath string, rules []string) error {
	return editAllowRules(workspacePath, true, func(allow []json.RawMessage) ([]json.RawMessage, error) {
		present := allowedStrings(allow)
		for _, rule := range rules {
			if present[rule] {
				continue
			}
			encoded, err := json.Marshal(rule)
			if err != nil {
				return nil, err
			}
			allow = append(allow, encoded)
			present[rule] = true
		}
		return allow, nil
	})
}

// RemovePermissionRules takes rules out of the same list. A workspace without
// the file or the rule is left alone.
func RemovePermissionRules(workspacePath string, rules []string) error {
	drop := make(map[string]bool, len(rules))
	for _, rule := range rules {
		drop[rule] = true
	}
	return editAllowRules(workspacePath, false, func(allow []json.RawMessage) ([]json.RawMessage, error) {
		kept := allow[:0:0]
		for _, entry := range allow {
			var rule string
			if json.Unmarshal(entry, &rule) == nil && drop[rule] {
				continue
			}
			kept = append(kept, entry)
		}
		return kept, nil
	})
}

func allowedStrings(allow []json.RawMessage) map[string]bool {
	present := make(map[string]bool, len(allow))
	for _, entry := range allow {
		var rule string
		if json.Unmarshal(entry, &rule) == nil {
			present[rule] = true
		}
	}
	return present
}

// editAllowRules reads the workspace's settings.local.json, hands its
// permissions.allow entries to edit, and writes the file back atomically when
// they changed. create says whether a missing file may be created.
func editAllowRules(workspacePath string, create bool, edit func([]json.RawMessage) ([]json.RawMessage, error)) error {
	if strings.TrimSpace(workspacePath) == "" {
		return errors.New("claude-code permission rules: workspace path is required")
	}
	path := claudeSettingsPath(workspacePath)
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(path) //nolint:gosec // path built from the session's own workspace directory
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return nil
		}
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	case strings.TrimSpace(string(data)) != "":
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	permissions := map[string]json.RawMessage{}
	if raw, ok := top["permissions"]; ok {
		if err := json.Unmarshal(raw, &permissions); err != nil {
			return fmt.Errorf("parse permissions in %s: %w", path, err)
		}
	}
	var allow []json.RawMessage
	if raw, ok := permissions["allow"]; ok {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return fmt.Errorf("parse permissions.allow in %s: %w", path, err)
		}
	}
	before := len(allow)
	allow, err = edit(allow)
	if err != nil {
		return err
	}
	if len(allow) == before {
		return nil
	}
	if len(allow) == 0 {
		delete(permissions, "allow")
	} else {
		if permissions["allow"], err = json.Marshal(allow); err != nil {
			return fmt.Errorf("encode permissions.allow: %w", err)
		}
	}
	if len(permissions) == 0 {
		delete(top, "permissions")
	} else {
		if top["permissions"], err = json.Marshal(permissions); err != nil {
			return fmt.Errorf("encode permissions: %w", err)
		}
	}
	encoded, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := hookutil.AtomicWriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return hookutil.EnsureWorkspaceGitignore(filepath.Dir(path), filepath.Base(path))
}
