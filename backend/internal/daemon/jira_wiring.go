package daemon

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	jirasvc "github.com/aoagents/agent-orchestrator/backend/internal/service/jira"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

// projectLookup is the one store read the Jira service needs.
type projectLookup interface {
	GetProject(ctx context.Context, id string) (domain.ProjectRecord, bool, error)
}

// newJiraService wires the Jira service to the registered projects and the
// project orchestrators. Its connections live in the daemon's data dir.
func newJiraService(dataDir string, store projectLookup, sessions *sessionsvc.Service) *jirasvc.Service {
	return jirasvc.New(jirasvc.Deps{
		DataDir:       dataDir,
		Projects:      jiraProjects{store: store},
		Orchestrators: jiraOrchestrators{sessions: sessions},
	})
}

type jiraProjects struct{ store projectLookup }

func (p jiraProjects) ProjectExists(ctx context.Context, projectID string) (bool, error) {
	_, ok, err := p.store.GetProject(ctx, projectID)
	return ok, err
}

// jiraOrchestrators adapts the session service to jirasvc.Orchestrators.
type jiraOrchestrators struct{ sessions *sessionsvc.Service }

func (o jiraOrchestrators) ActiveOrchestrator(ctx context.Context, projectID string) (string, error) {
	active := true
	found, err := o.sessions.List(ctx, sessionsvc.ListFilter{ProjectID: domain.ProjectID(projectID), Active: &active, OrchestratorOnly: true})
	if err != nil || len(found) == 0 {
		return "", err
	}
	newest := found[0]
	for _, sess := range found[1:] {
		if sess.CreatedAt.After(newest.CreatedAt) {
			newest = sess
		}
	}
	return string(newest.ID), nil
}

func (o jiraOrchestrators) SpawnOrchestrator(ctx context.Context, projectID string) (string, error) {
	sess, err := o.sessions.SpawnOrchestrator(ctx, domain.ProjectID(projectID), false, "", "")
	if err != nil {
		return "", err
	}
	return string(sess.ID), nil
}

// SendTask delivers the ticket as the person's own message, so the orchestrator
// treats it as an instruction rather than agent-to-agent traffic.
func (o jiraOrchestrators) SendTask(ctx context.Context, sessionID, text string) error {
	return o.sessions.SendWithOptions(ctx, domain.SessionID(sessionID), text, nil, ports.MessageDeliveryOptions{AuthoredByUser: true, InteractionAt: time.Now().UTC()})
}
