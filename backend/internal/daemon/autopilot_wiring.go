package daemon

import (
	"context"

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
