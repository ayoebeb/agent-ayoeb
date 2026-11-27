package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

// agentModelCatalog reads an agent's discovered models.
type agentModelCatalog interface {
	ModelCatalog(ctx context.Context, agentID, projectID string, refresh bool) (ports.AgentModelCatalog, error)
}

const codexReviewerModelSetting = "choose one in Project settings > Agents > Reviewer > Model (reviewers[0].agentConfig.model)"

// reviewerDefaultModel picks the model for a Codex reviewer with none set in
// AO. Left alone, Codex launches with its config.toml model, which the signed-in
// account may not offer, and every review then fails.
func reviewerDefaultModel(catalogs agentModelCatalog, customProvider func() bool, configuredModel func() string, log *slog.Logger) reviewcore.DefaultModelFunc {
	return func(ctx context.Context, harness domain.ReviewerHarness) (string, error) {
		// Another provider's models are not in Codex's account catalog.
		if harness != domain.ReviewerCodex || customProvider() {
			return "", nil
		}
		catalog, err := catalogs.ModelCatalog(ctx, string(domain.HarnessCodex), "", false)
		var offered []ports.AgentModelInfo
		for _, model := range catalog.Models {
			if model.ID != "" && model.UnavailableReason == "" {
				offered = append(offered, model)
			}
		}
		if err == nil && len(offered) == 0 {
			err = errors.New("the list is empty")
		}
		if err != nil {
			return "", fmt.Errorf("no model is set for the Codex reviewer and Codex's model list could not be read; %s: %w", codexReviewerModelSetting, err)
		}
		configured := configuredModel()
		if slices.ContainsFunc(offered, func(m ports.AgentModelInfo) bool { return m.ID == configured }) {
			return "", nil
		}
		picked := offered[max(slices.IndexFunc(offered, func(m ports.AgentModelInfo) bool { return m.IsDefault }), 0)].ID
		log.Warn("no model is set for the Codex reviewer; using a discovered one", "model", picked, "codexConfigModel", configured, "hint", codexReviewerModelSetting)
		return picked, nil
	}
}
