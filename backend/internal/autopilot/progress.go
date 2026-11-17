package autopilot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	gitTimeout = 15 * time.Second
	// maxDiffBytes bounds how much of `git diff` is hashed. A change that only
	// differs past the cap still shows up in the status line list.
	maxDiffBytes = 4 << 20
)

// GitRunner runs git in a directory and returns its output. Tests replace it.
type GitRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// GitProgress fingerprints a session's workspace from git: the checked-out
// commit, the status of every file, and the content of the uncommitted changes.
type GitProgress struct {
	Run GitRunner
}

// Fingerprint implements Progress. A session without a workspace has no
// fingerprint, which never counts as stuck.
func (g GitProgress) Fingerprint(ctx context.Context, rec domain.SessionRecord) (string, error) {
	dir := rec.Metadata.WorkspacePath
	if dir == "" {
		return "", nil
	}
	run := g.Run
	if run == nil {
		run = runGit
	}
	head, err := run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	status, err := run(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	diff, err := run(ctx, dir, "diff", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes]
	}
	sum := sha256.Sum256(diff)
	return fingerprintOf(strings.TrimSpace(string(head)), string(status), hex.EncodeToString(sum[:8])), nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
