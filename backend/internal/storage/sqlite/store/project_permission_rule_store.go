package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const projectPermissionRuleColumns = `id, project_id, tool, content, kind, label, source_session_id, created_at`

// UpsertProjectPermissionRule remembers a rule for a project. Remembering a rule
// that is already stored keeps the original row, so the list shows when and from
// which session the user first chose it. It reports whether a row was added.
func (s *Store) UpsertProjectPermissionRule(ctx context.Context, rule domain.ProjectPermissionRule) (bool, error) {
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = time.Now()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO project_permission_rules (`+projectPermissionRuleColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (project_id, tool, content) DO NOTHING`,
		rule.ID, string(rule.ProjectID), rule.Tool, rule.Content, string(rule.Kind), rule.Label,
		string(rule.SourceSessionID), rule.CreatedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("remember permission rule for %s: %w", rule.ProjectID, err)
	}
	added, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("remember permission rule for %s: %w", rule.ProjectID, err)
	}
	return added > 0, nil
}

// ListProjectPermissionRules returns a project's remembered rules, oldest first.
func (s *Store) ListProjectPermissionRules(ctx context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+projectPermissionRuleColumns+` FROM project_permission_rules
		 WHERE project_id = ? ORDER BY created_at, id`, string(project))
	if err != nil {
		return nil, fmt.Errorf("list permission rules for %s: %w", project, err)
	}
	defer func() { _ = rows.Close() }()
	rules := []domain.ProjectPermissionRule{}
	for rows.Next() {
		rule, err := scanProjectPermissionRule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan permission rule for %s: %w", project, err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list permission rules for %s: %w", project, err)
	}
	return rules, nil
}

// DeleteProjectPermissionRule forgets one rule of a project and returns it, so
// the caller can take it out of workspaces that already carry it. ok is false
// when the project has no such rule.
func (s *Store) DeleteProjectPermissionRule(ctx context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rule, err := scanProjectPermissionRule(s.writeDB.QueryRowContext(ctx,
		`SELECT `+projectPermissionRuleColumns+` FROM project_permission_rules
		 WHERE project_id = ? AND id = ?`, string(project), id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectPermissionRule{}, false, nil
	}
	if err != nil {
		return domain.ProjectPermissionRule{}, false, fmt.Errorf("find permission rule %s: %w", id, err)
	}
	if _, err := s.writeDB.ExecContext(ctx,
		`DELETE FROM project_permission_rules WHERE project_id = ? AND id = ?`, string(project), id); err != nil {
		return domain.ProjectPermissionRule{}, false, fmt.Errorf("forget permission rule %s: %w", id, err)
	}
	return rule, true, nil
}

func scanProjectPermissionRule(scan func(dest ...any) error) (domain.ProjectPermissionRule, error) {
	var (
		rule    domain.ProjectPermissionRule
		project string
		kind    string
		source  string
	)
	if err := scan(&rule.ID, &project, &rule.Tool, &rule.Content, &kind, &rule.Label, &source, &rule.CreatedAt); err != nil {
		return domain.ProjectPermissionRule{}, err
	}
	rule.ProjectID = domain.ProjectID(project)
	rule.Kind = domain.ProjectPermissionRuleKind(kind)
	rule.SourceSessionID = domain.SessionID(source)
	return rule, nil
}
