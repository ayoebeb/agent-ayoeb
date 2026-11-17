package chat

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PermissionMemory remembers the tool permission a user chose "Always allow" for,
// for every future session of the project. raw is the chosen option's own
// encoding; the implementation decides which of its rules may be remembered.
type PermissionMemory interface {
	RememberDecision(ctx context.Context, project domain.ProjectID, session domain.SessionID, raw []byte) error
}

// noteRememberableDecisions keeps the raw encoding of a pending approval's
// "always" options. The approval row stores only each option's id, label and
// kind, so the rule a choice applies would otherwise be gone by the time the
// user picks one.
func (c *Controller) noteRememberableDecisions(event ports.ChatEvent) {
	if c.permissionMemory == nil || c.harness != domain.HarnessClaudeCode || event.RequestID == "" {
		return
	}
	var options map[string][]byte
	for _, decision := range event.Decisions {
		if decision.Kind != ports.ChatDecisionAllowAlways || len(decision.Raw) == 0 {
			continue
		}
		if options == nil {
			options = map[string][]byte{}
		}
		options[decision.ID] = decision.Raw
	}
	if options == nil {
		return
	}
	c.rememberableMu.Lock()
	defer c.rememberableMu.Unlock()
	if c.rememberable == nil {
		c.rememberable = map[string]map[string][]byte{}
	}
	c.rememberable[event.RequestID] = options
}

// forgetRememberableDecisions drops the options of an approval that ended
// without the user choosing one.
func (c *Controller) forgetRememberableDecisions(requestID string) {
	c.rememberableMu.Lock()
	defer c.rememberableMu.Unlock()
	delete(c.rememberable, requestID)
}

// chosenDecisionRaw returns the raw encoding of the option about to be chosen, or
// nil when it is not an "always" option. It is read before the provider is told:
// the provider's own resolved event can reach forgetRememberableDecisions first.
func (c *Controller) chosenDecisionRaw(requestID, decisionID string) []byte {
	c.rememberableMu.Lock()
	defer c.rememberableMu.Unlock()
	return c.rememberable[requestID][decisionID]
}

// rememberChosenDecision remembers the rule the chosen option applies for the
// whole project. It runs only after the provider accepted the decision, and a
// failure never undoes the choice: the rule still holds in its own workspace.
func (c *Controller) rememberChosenDecision(ctx context.Context, raw []byte) {
	if c.permissionMemory == nil || len(raw) == 0 {
		return
	}
	if err := c.permissionMemory.RememberDecision(ctx, c.conversation.ProjectID, c.sessionID, raw); err != nil {
		c.log.Warn("could not remember an always-allow choice for the project",
			"session", c.sessionID, "error", err)
	}
}
