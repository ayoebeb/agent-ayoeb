package permissionmemory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrRuleNotFound reports a revoke for a rule the project does not have.
var ErrRuleNotFound = errors.New("permission rule not found")

// Store is the durable list of a project's remembered rules.
type Store interface {
	UpsertProjectPermissionRule(ctx context.Context, rule domain.ProjectPermissionRule) (bool, error)
	ListProjectPermissionRules(ctx context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error)
	DeleteProjectPermissionRule(ctx context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, bool, error)
}

// Sessions looks up the session a decision or a new workspace belongs to, and
// the live sessions of a project.
type Sessions interface {
	GetSession(ctx context.Context, id domain.SessionID) (domain.SessionRecord, bool, error)
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
}

// Options builds a Service. AddRules and RemoveRules edit a Claude Code
// workspace's permissions.allow list; they are functions so this package stays
// independent of the Claude adapter.
type Options struct {
	Store       Store
	Sessions    Sessions
	AddRules    func(workspacePath string, rules []string) error
	RemoveRules func(workspacePath string, rules []string) error
	Now         func() time.Time
	Log         *slog.Logger
}

// Service remembers, applies and revokes a project's permission rules.
type Service struct {
	store    Store
	sessions Sessions
	add      func(string, []string) error
	remove   func(string, []string) error
	now      func() time.Time
	log      *slog.Logger
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{
		store: opts.Store, sessions: opts.Sessions, add: opts.AddRules, remove: opts.RemoveRules,
		now: opts.Now, log: opts.Log,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// RememberDecision remembers the rules the user's "Always allow" choice applied,
// for every future session of the project. raw is the chosen option's own
// encoding. Rules the policy does not allow project-wide are not stored, and the
// choice still holds in its own workspace as before.
func (s *Service) RememberDecision(ctx context.Context, project domain.ProjectID, session domain.SessionID, raw []byte) error {
	if project == "" {
		return nil
	}
	var firstErr error
	for _, c := range candidatesFromDecision(raw) {
		kind, label, ok := classify(c)
		if !ok {
			continue
		}
		rule := domain.ProjectPermissionRule{
			ID: domain.NewProjectPermissionRuleID(project, c.tool, c.content), ProjectID: project,
			Tool: c.tool, Content: c.content, Kind: kind, Label: label,
			SourceSessionID: session, CreatedAt: s.now(),
		}
		if _, err := s.store.UpsertProjectPermissionRule(ctx, rule); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ApplyToWorkspace writes the project's remembered rules into a new Claude Code
// session workspace. Other harnesses and sessions without a project are left
// alone.
func (s *Service) ApplyToWorkspace(ctx context.Context, session domain.SessionID, workspacePath string) error {
	if workspacePath == "" || s.add == nil {
		return nil
	}
	record, ok, err := s.sessions.GetSession(ctx, session)
	if err != nil {
		return fmt.Errorf("load session %s: %w", session, err)
	}
	if !ok || record.ProjectID == "" || record.Harness != domain.HarnessClaudeCode {
		return nil
	}
	rules, err := s.store.ListProjectPermissionRules(ctx, record.ProjectID)
	if err != nil || len(rules) == 0 {
		return err
	}
	return s.add(workspacePath, settingsRules(rules))
}

// List returns a project's remembered rules, oldest first.
func (s *Service) List(ctx context.Context, project domain.ProjectID) ([]domain.ProjectPermissionRule, error) {
	return s.store.ListProjectPermissionRules(ctx, project)
}

// Revoke forgets one rule. Future sessions no longer get it, and it is taken out
// of the workspaces of the project's live Claude Code sessions too. That also
// removes the same rule when the person chose it again inside one of those
// sessions, which is the safe side of the two.
func (s *Service) Revoke(ctx context.Context, project domain.ProjectID, id string) (domain.ProjectPermissionRule, error) {
	rule, ok, err := s.store.DeleteProjectPermissionRule(ctx, project, id)
	if err != nil {
		return domain.ProjectPermissionRule{}, err
	}
	if !ok {
		return domain.ProjectPermissionRule{}, ErrRuleNotFound
	}
	if s.remove == nil {
		return rule, nil
	}
	sessions, err := s.sessions.ListSessions(ctx, project)
	if err != nil {
		s.log.Warn("permission rule revoked; live sessions not updated", "project", project, "error", err)
		return rule, nil
	}
	for _, session := range sessions {
		path := session.Metadata.WorkspacePath
		if session.IsTerminated || session.Harness != domain.HarnessClaudeCode || path == "" {
			continue
		}
		if err := s.remove(path, []string{rule.SettingsRule()}); err != nil {
			s.log.Warn("permission rule revoked; could not update a live session", "session", session.ID, "error", err)
		}
	}
	return rule, nil
}

func settingsRules(rules []domain.ProjectPermissionRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.SettingsRule())
	}
	return out
}
