package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/usagelimit"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type failingSemanticSession struct{ err error }

func (s failingSemanticSession) SendSemantic(context.Context, domain.SessionID, string, string) error {
	return s.err
}
func (failingSemanticSession) InterruptTUI(context.Context, domain.SessionID) error { return nil }

// A session that was killed or no longer exists stops the watcher's retries; any
// other refusal (a busy composer, say) stays retryable.
func TestUsageLimitSenderSeparatesGoneSessionsFromRetryableRefusals(t *testing.T) {
	for _, gone := range []error{sessionmanager.ErrNotFound, sessionmanager.ErrTerminated} {
		err := usageLimitSender{sessions: failingSemanticSession{err: gone}}.SendSemantic(context.Background(), "s", "m", "k")
		if !errors.Is(err, usagelimit.ErrSessionGone) || !errors.Is(err, gone) {
			t.Fatalf("%v mapped to %v", gone, err)
		}
	}
	busy := sessionmanager.ErrComposerBusy
	if err := (usageLimitSender{sessions: failingSemanticSession{err: busy}}).SendSemantic(context.Background(), "s", "m", "k"); errors.Is(err, usagelimit.ErrSessionGone) || !errors.Is(err, busy) {
		t.Fatalf("a busy composer must stay retryable: %v", err)
	}
	if err := (usageLimitSender{sessions: failingSemanticSession{}}).SendSemantic(context.Background(), "s", "m", "k"); err != nil {
		t.Fatalf("accepted message returned %v", err)
	}
}
