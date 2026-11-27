package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestRuntimeEnvSharesTheGitHubLoginWithCodexReviewers(t *testing.T) {
	login := func(context.Context, map[string]string) (string, error) { return "gho_login\n", nil }
	noLogin := func(context.Context, map[string]string) (string, error) { return "", errors.New("not logged in") }
	for _, tc := range []struct {
		name       string
		harness    domain.ReviewerHarness
		login      GitHubTokenFunc
		daemonEnv  string
		projectEnv map[string]string
		want       string
	}{
		{name: "codex gets the gh login", harness: domain.ReviewerCodex, login: login, want: "gho_login"},
		{name: "project token wins", harness: domain.ReviewerCodex, login: login, projectEnv: map[string]string{"GH_TOKEN": "gho_project"}, want: "gho_project"},
		{name: "inherited token is left alone", harness: domain.ReviewerCodex, login: login, daemonEnv: "gho_daemon"},
		{name: "unsandboxed reviewer needs nothing", harness: domain.ReviewerClaudeCode, login: login},
		{name: "no login", harness: domain.ReviewerCodex, login: noLogin},
		{name: "not wired", harness: domain.ReviewerCodex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", tc.daemonEnv)
			t.Setenv("GITHUB_TOKEN", "")
			launcher := NewLauncher(nil, nil, t.TempDir(), WithGitHubToken(tc.login)).(*agentLauncher)
			env := launcher.runtimeEnv(context.Background(), LaunchSpec{Harness: tc.harness, ProjectEnv: tc.projectEnv}, nil, nil)
			if got := env["GH_TOKEN"]; got != tc.want {
				t.Fatalf("GH_TOKEN = %q, want %q", got, tc.want)
			}
		})
	}
}
