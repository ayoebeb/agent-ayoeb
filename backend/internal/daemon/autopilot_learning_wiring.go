package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/autopilotlearn"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// autopilotParkDigestEvery is how long the oldest parked item waits before one
// notification lists everything parked, instead of one notification per item.
const autopilotParkDigestEvery = 15 * time.Minute

// newAutopilotLearning opens the local store that holds what Autopilot learned
// about how the owner decides. It is stored under the data dir; the Sonnet call
// that rewrites the profile is where its decision texts are sent. A failure
// returns nil: Autopilot then runs as before, without learned advice.
func newAutopilotLearning(dataDir string, st *store.Store, log *slog.Logger) *autopilotlearn.Service {
	ls, err := autopilotlearn.Open(dataDir, nil)
	if err != nil {
		log.Warn("autopilot learning is off: cannot open its store", "err", err)
		return nil
	}
	work := filepath.Join(dataDir, autopilotlearn.DirName, "work")
	_ = os.MkdirAll(work, 0o700)
	learner := autopilotlearn.NewLearner(ls, autopilotlearn.ClaudeCLI{Binary: os.Getenv("AO_AUTOPILOT_LEARN_CLAUDE"), Dir: work}, nil)
	home, _ := os.UserHomeDir()
	return autopilotlearn.NewService(autopilotlearn.ServiceDeps{
		Store:   ls,
		Learner: learner,
		Roots:   func() []string { return autopilotlearn.DefaultRoots(home, dataDir) },
		LastAgentText: func(ctx context.Context, session string) string {
			conv, err := st.ConversationForSession(ctx, domain.SessionID(session))
			if err != nil {
				return ""
			}
			msgs, err := st.ConversationMessages(ctx, conv.ID)
			if err != nil {
				return ""
			}
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == domain.MessageRoleAssistant && msgs[i].Text != "" {
					return msgs[i].Text
				}
			}
			return ""
		},
	})
}

// autopilotAdvisor lets Autopilot read the learned profile and learn from undos.
type autopilotAdvisor struct{ svc *autopilotlearn.Service }

func (a autopilotAdvisor) Advise(_ context.Context, project, question string, options []string) string {
	return a.svc.Advise(project, question, options).Text
}

func (a autopilotAdvisor) Describe(ids []string) []string { return a.svc.Describe(ids) }

func (a autopilotAdvisor) Correct(ctx context.Context, project, session, question, choice string) {
	a.svc.RecordCorrection(ctx, project, session, question, choice)
}

// usageLimitProfile decides a usage limit for sessions on Autopilot: wait for the
// reset, unless the owner's profile clearly allows another account.
type usageLimitProfile struct {
	ap    *autopilot.Service
	learn *autopilotlearn.Service
}

func (p usageLimitProfile) UsageLimit(ctx context.Context, id domain.SessionID) (covered, switchAccount bool) {
	if !p.ap.CoversSession(ctx, id) {
		return false, false
	}
	return true, p.learn.UsageLimitSwitches()
}
