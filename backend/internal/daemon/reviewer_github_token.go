package daemon

import (
	"context"
	"os"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

// reviewerGitHubToken reads the gh login to share with sandboxed reviewers.
// Only the Windows sandbox runs as another user, so elsewhere it is nil and the
// reviewer finds the login itself.
func reviewerGitHubToken(goos string) reviewcore.GitHubTokenFunc {
	if goos != "windows" {
		return nil
	}
	return func(ctx context.Context, env map[string]string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := aoprocess.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
		cmd.Env = os.Environ()
		// The reviewer's own GH_CONFIG_DIR picks the login, as it would in a worker.
		if dir := env["GH_CONFIG_DIR"]; dir != "" {
			cmd.Env = append(cmd.Env, "GH_CONFIG_DIR="+dir)
		}
		out, err := cmd.Output()
		return string(out), err
	}
}
