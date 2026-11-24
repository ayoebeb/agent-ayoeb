package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type autopilotAdviceRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type autopilotAdviceResponse struct {
	Advice string `json:"advice"`
}

type autopilotLearningRule struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Text   string `json:"text"`
	Origin string `json:"origin"`
}

type autopilotLearningPair struct {
	ID       string `json:"id"`
	Project  string `json:"project"`
	Source   string `json:"source"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type autopilotLearningResponse struct {
	Rules         []autopilotLearningRule `json:"rules"`
	Pairs         []autopilotLearningPair `json:"pairs"`
	TotalPairs    int                     `json:"totalPairs"`
	UnfoldedPairs int                     `json:"unfoldedPairs"`
	LastError     string                  `json:"lastError"`
	CanLearn      bool                    `json:"canLearn"`
	Learning      bool                    `json:"learning"`
}

type autopilotLearningImportResponse struct {
	Files   int `json:"files"`
	Pairs   int `json:"pairs"`
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

type autopilotLearningRelearnResponse struct {
	Started bool `json:"started"`
}

func newAutopilotAdviseCommand(ctx *commandContext) *cobra.Command {
	var question string
	var options []string
	cmd := &cobra.Command{
		Use:   "advise",
		Short: "Ask how the owner would decide a question",
		Long: `Before you choose, ask what the owner would pick. AO answers from their profile
(rules learned from the choices they made themselves) and the few most similar
past decisions. Choose what they would choose, then record it with
"ao autopilot decide --based-on <ids> --confidence high|medium|low". If nothing
similar exists or the past choices disagree, say low and take the safest option
that can be undone. This is advice about preference only: the hard limits stay.

  ao autopilot advise --question "Merge the small fix without waiting for CI?"`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := autopilotOwnSession()
			if err != nil {
				return err
			}
			if strings.TrimSpace(question) == "" {
				return usageError{errors.New("usage: --question is required")}
			}
			var out autopilotAdviceResponse
			if err := ctx.postJSON(cmd.Context(), autopilotPath(id, "/advice"), autopilotAdviceRequest{Question: question, Options: options}, &out); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(ctx.deps.Out, out.Advice)
			return nil
		},
	}
	cmd.Flags().StringVar(&question, "question", "", "What you would have asked the owner")
	cmd.Flags().StringArrayVar(&options, "option", nil, "An option you are weighing (repeatable)")
	return cmd
}

func newAutopilotLearnCommand(ctx *commandContext) *cobra.Command {
	var doImport, relearn, asJSON bool
	cmd := &cobra.Command{
		Use:   "learn",
		Short: "See and feed what Autopilot learned about how you decide",
		Long: `Autopilot learns how you decide from the choices you make in normal mode: each
question an orchestrator asks you and your answer is stored locally, and a short
profile of your rules is kept from them. With no flag this prints the profile.

  --import    one-time scan of your past orchestrator conversations for question
              and answer pairs (only the pairs are stored)
  --relearn   rewrite the profile now from the decisions it has not seen yet

Edit or delete rules and decisions in the app (Autopilot, What Autopilot learned).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if doImport {
				var out autopilotLearningImportResponse
				if err := ctx.postJSON(cmd.Context(), "autopilot/learning/import", struct{}{}, &out); err != nil {
					return err
				}
				if asJSON {
					return writeJSON(ctx.deps.Out, out)
				}
				_, _ = fmt.Fprintf(ctx.deps.Out, "Read %d orchestrator conversations: %d question and answer pairs found, %d new.\n", out.Files, out.Pairs, out.Added)
			}
			if relearn {
				var out autopilotLearningRelearnResponse
				if err := ctx.postJSON(cmd.Context(), "autopilot/learning/relearn", struct{}{}, &out); err != nil {
					return err
				}
				if !out.Started {
					_, _ = fmt.Fprintln(ctx.deps.Out, "No profile update started: it is already running, or no Sonnet model can be reached (is the claude command signed in?).")
				} else {
					_, _ = fmt.Fprintln(ctx.deps.Out, "Updating the profile in the background.")
				}
			}
			if doImport || relearn {
				return nil
			}
			var view autopilotLearningResponse
			if err := ctx.getJSON(cmd.Context(), "autopilot/learning", &view); err != nil {
				return err
			}
			if asJSON {
				return writeJSON(ctx.deps.Out, view)
			}
			_, _ = fmt.Fprintf(ctx.deps.Out, "%d decisions stored, %d not yet in the profile.\n", view.TotalPairs, view.UnfoldedPairs)
			if view.LastError != "" {
				_, _ = fmt.Fprintf(ctx.deps.Out, "Last profile update failed: %s\n", view.LastError)
			}
			if len(view.Rules) == 0 {
				_, _ = fmt.Fprintln(ctx.deps.Out, "No profile rules yet. Run `ao autopilot learn --import` to start from your past conversations.")
				return nil
			}
			for _, r := range view.Rules {
				scope := r.Scope
				if scope == "global" {
					scope = "all projects"
				}
				_, _ = fmt.Fprintf(ctx.deps.Out, "- [%s] %s (%s)\n", scope, r.Text, r.Origin)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&doImport, "import", false, "Scan past orchestrator conversations for question and answer pairs")
	cmd.Flags().BoolVar(&relearn, "relearn", false, "Rewrite the profile now from the decisions it has not seen yet")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}
