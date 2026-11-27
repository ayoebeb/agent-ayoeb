package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeModelCatalog struct {
	models []ports.AgentModelInfo
	err    error
}

func (f fakeModelCatalog) ModelCatalog(context.Context, string, string, bool) (ports.AgentModelCatalog, error) {
	return ports.AgentModelCatalog{Models: f.models}, f.err
}

func TestReviewerDefaultModel(t *testing.T) {
	discovered := []ports.AgentModelInfo{{ID: "gpt-6.2-astra"}, {ID: "gpt-6.2-sol", IsDefault: true}}
	for _, tc := range []struct {
		name       string
		harness    domain.ReviewerHarness
		catalog    fakeModelCatalog
		custom     bool
		configured string
		want       string
		wantErr    bool
	}{
		{name: "unsupported config.toml model is replaced by the discovered default", configured: "gpt-5.5", catalog: fakeModelCatalog{models: discovered}, want: "gpt-6.2-sol"},
		{name: "no config.toml model takes the discovered default", catalog: fakeModelCatalog{models: discovered}, want: "gpt-6.2-sol"},
		{name: "supported config.toml model is left to codex", configured: "gpt-6.2-astra", catalog: fakeModelCatalog{models: discovered}},
		{name: "no flagged default takes the first", catalog: fakeModelCatalog{models: discovered[:1]}, want: "gpt-6.2-astra"},
		{name: "custom provider is left alone", custom: true, configured: "local-model", catalog: fakeModelCatalog{models: discovered}},
		{name: "other reviewers are left alone", harness: domain.ReviewerClaudeCode},
		{name: "unreadable catalog fails", catalog: fakeModelCatalog{err: errors.New("offline")}, wantErr: true},
		{name: "empty catalog fails", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harness := tc.harness
			if harness == "" {
				harness = domain.ReviewerCodex
			}
			resolve := reviewerDefaultModel(tc.catalog, func() bool { return tc.custom }, func() string { return tc.configured }, slog.New(slog.NewTextHandler(io.Discard, nil)))
			got, err := resolve(context.Background(), harness)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "reviewers[0].agentConfig.model") {
					t.Fatalf("error = %v, want one naming the setting", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("model = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
