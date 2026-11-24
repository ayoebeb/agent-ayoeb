package daemon

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
)

// autopilotSender is the session service's message delivery, Chat or terminal.
type autopilotSender interface {
	Send(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment) error
}

// autopilotMessenger sends Autopilot's messages (the task, the status block, the
// stop notice) to a session the way any other daemon message is sent.
type autopilotMessenger struct{ sessions autopilotSender }

func (m autopilotMessenger) Send(ctx context.Context, id domain.SessionID, message string) error {
	return m.sessions.Send(ctx, id, message, nil)
}

// autopilotApprovals hands a Chat session's permission requests to Autopilot.
type autopilotApprovals struct{ svc *autopilot.Service }

func (a autopilotApprovals) ApprovalRequested(ctx context.Context, id domain.SessionID, n chatsvc.ApprovalNotice) {
	a.svc.HandleApproval(ctx, id, autopilot.ApprovalNotice{
		RequestID: n.RequestID, Summary: n.Summary, Detail: n.Detail, Decisions: n.Decisions,
	})
}

// autopilotLimitReader reads the latest failed turn of a session.
type autopilotLimitReader interface {
	ListLatestFailedTurns(ctx context.Context, ids []domain.SessionID) ([]domain.LimitStopTurn, error)
}

// autopilotUsageLimited reports whether a session's latest turn stopped on a
// provider usage limit that has not reset yet, which is when Autopilot must not
// nudge it: a message would only bounce off the limit and burn another turn.
// A limit with no known reset time counts as active until the session runs a
// newer turn. The read fails open: an unreadable store never freezes a run.
func autopilotUsageLimited(store autopilotLimitReader, now func() time.Time) autopilot.UsageLimitCheck {
	return func(ctx context.Context, id domain.SessionID) (bool, error) {
		turns, err := store.ListLatestFailedTurns(ctx, []domain.SessionID{id})
		if err != nil {
			return false, err
		}
		at := now()
		for _, turn := range turns {
			limit := domain.BuildSessionUsageLimit(turn, at)
			if limit == nil {
				continue
			}
			if limit.ResetsAt == nil || limit.ResetsAt.After(at) {
				return true, nil
			}
		}
		return false, nil
	}
}
