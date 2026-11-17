package chat

import (
	"context"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type recordingReplyObserver struct{ sessions []domain.SessionID }

func (r *recordingReplyObserver) UserReplied(_ context.Context, id domain.SessionID) {
	r.sessions = append(r.sessions, id)
}

// Only a message the human wrote closes an orchestrator's question. A worker
// steering the orchestrator arrives with human origin too, so the session that
// sent it is what tells them apart; automation and the daemon are never the
// human.
func TestNotifyHumanReply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		msg     ports.ChatUserMessage
		replied bool
	}{
		{"typed by the human", ports.ChatUserMessage{Origin: domain.MessageOriginHuman}, true},
		{"origin not set", ports.ChatUserMessage{}, true},
		{"a worker steering with human origin", ports.ChatUserMessage{Origin: domain.MessageOriginHuman, SenderSessionID: "mer-1"}, false},
		{"a worker message with blank sender is the human", ports.ChatUserMessage{Origin: domain.MessageOriginHuman, SenderSessionID: "  "}, true},
		{"automation", ports.ChatUserMessage{Origin: domain.MessageOriginAutomation}, false},
		{"daemon", ports.ChatUserMessage{Origin: domain.MessageOriginDaemon}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &recordingReplyObserver{}
			svc := &Service{}
			svc.SetHumanReplyObserver(observer)
			svc.notifyHumanReply(context.Background(), "mer-orchestrator-1", tc.msg)
			if got := len(observer.sessions) == 1 && observer.sessions[0] == "mer-orchestrator-1"; got != tc.replied {
				t.Fatalf("replied = %v (calls %v), want %v", got, observer.sessions, tc.replied)
			}
		})
	}
	// No observer is the normal case for a daemon without the feature wired.
	(&Service{}).notifyHumanReply(context.Background(), "mer-orchestrator-1", ports.ChatUserMessage{})
}
