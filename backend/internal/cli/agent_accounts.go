package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// agentAccountDTO mirrors one row of GET /api/v1/agents/accounts.
type agentAccountDTO struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Email    string `json:"email,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Default  bool   `json:"default"`
	SignedIn bool   `json:"signedIn"`
	Device   bool   `json:"device"`
}

type agentAccountsResponse struct {
	Accounts []agentAccountDTO `json:"accounts"`
}

type sessionAccountWorkerMoveDTO struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type switchSessionAccountResponse struct {
	OK           bool                          `json:"ok"`
	SessionID    string                        `json:"sessionId"`
	Transition   *sessionTransitionBriefDTO    `json:"transition,omitempty"`
	Workers      []sessionAccountWorkerMoveDTO `json:"workers"`
	MovedWorkers int                           `json:"movedWorkers"`
}

type sessionTransitionBriefDTO struct {
	ID              string `json:"id"`
	Phase           string `json:"phase"`
	SourceAccountID string `json:"sourceAccountId,omitempty"`
	TargetAccountID string `json:"targetAccountId,omitempty"`
}

type switchSessionAccountRequest struct {
	AccountID      string `json:"accountId"`
	StopTurn       bool   `json:"stopTurn,omitempty"`
	IncludeWorkers *bool  `json:"includeWorkers,omitempty"`
}

type switchAccountOptions struct {
	account   string
	workers   bool
	noWorkers bool
	stopTurn  bool
	json      bool
}

func newAgentAccountsCommand(ctx *commandContext) *cobra.Command {
	var asJSON, refresh bool
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "List the Claude and Codex accounts Orchestrator can run agents with",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			accounts, err := ctx.fetchAgentAccounts(cmd.Context(), refresh)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), agentAccountsResponse{Accounts: accounts})
			}
			return writeAgentAccounts(cmd, accounts)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Read sign-in and limits again instead of using the cached reading")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}

func (c *commandContext) fetchAgentAccounts(ctx context.Context, refresh bool) ([]agentAccountDTO, error) {
	path := "agents/accounts"
	if refresh {
		path += "?refresh=true"
	}
	var res agentAccountsResponse
	if err := c.getJSON(ctx, path, &res); err != nil {
		return nil, err
	}
	return res.Accounts, nil
}

// shortEmail keeps an address readable in a table: long ones are cut in the middle.
func shortEmail(email string) string {
	const limit = 28
	if email == "" {
		return "-"
	}
	if len(email) <= limit {
		return email
	}
	return email[:limit-1] + "…"
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func writeAgentAccounts(cmd *cobra.Command, accounts []agentAccountDTO) error {
	if len(accounts) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no accounts")
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tPROVIDER\tEMAIL\tSIGNED IN\tDEFAULT"); err != nil {
		return err
	}
	for _, a := range accounts {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Provider, shortEmail(a.Email), yesNo(a.SignedIn), yesNo(a.Default)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// resolveAgentAccount finds the account a person means: an exact id, or an email
// prefix that names exactly one account. provider, when set, limits the choice
// to accounts of that provider (the session's agent).
func resolveAgentAccount(accounts []agentAccountDTO, query, provider string) (agentAccountDTO, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return agentAccountDTO{}, fmt.Errorf("--account is required")
	}
	var pool []agentAccountDTO
	for _, a := range accounts {
		if provider == "" || a.Provider == provider {
			pool = append(pool, a)
		}
	}
	for _, a := range pool {
		if a.ID == query {
			return a, nil
		}
	}
	lower := strings.ToLower(query)
	var matches []agentAccountDTO
	for _, a := range pool {
		if a.Email != "" && strings.HasPrefix(strings.ToLower(a.Email), lower) {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return agentAccountDTO{}, fmt.Errorf("no account matches %q; run `ao agent accounts` to see them", query)
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return agentAccountDTO{}, fmt.Errorf("%q matches several accounts (%s); use the account id", query, strings.Join(ids, ", "))
	}
}

// accountProviderForHarness is the account provider of a session's agent, or ""
// for an agent without accounts (the server then reports it).
func accountProviderForHarness(harness string) string {
	switch harness {
	case "claude-code":
		return "claude"
	case "codex":
		return "codex"
	default:
		return ""
	}
}

func newSessionSwitchAccountCommand(ctx *commandContext) *cobra.Command {
	var opts switchAccountOptions
	cmd := &cobra.Command{
		Use:   "switch-account <id>",
		Short: "Move a session's agent to another Claude or Codex account, keeping its conversation",
		Long: "Move a session's agent to another account of the same provider. The conversation continues on the new account.\n\n" +
			"For an orchestrator, its live workers of the same project and agent move too, and a session that stopped on a usage limit continues by itself. " +
			"Use --no-workers to move only the orchestrator. --workers is the default for an orchestrator and is accepted for clarity.",
		Args: oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			if opts.workers && opts.noWorkers {
				return fmt.Errorf("use either --workers or --no-workers, not both")
			}
			return ctx.switchSessionAccount(cmd.Context(), cmd, id, opts)
		},
	}
	cmd.Flags().StringVar(&opts.account, "account", "", "Account id or the start of its email address (see `ao agent accounts`)")
	cmd.Flags().BoolVar(&opts.workers, "workers", false, "Also move the project's live workers (the default for an orchestrator)")
	cmd.Flags().BoolVar(&opts.noWorkers, "no-workers", false, "Move only this session, not its workers")
	cmd.Flags().BoolVar(&opts.stopTurn, "stop-turn", false, "Cancel a turn in progress instead of waiting for it to finish")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func (c *commandContext) switchSessionAccount(ctx context.Context, cmd *cobra.Command, id string, opts switchAccountOptions) error {
	sess, err := c.fetchScopedSession(ctx, id, "")
	if err != nil {
		return err
	}
	accounts, err := c.fetchAgentAccounts(ctx, false)
	if err != nil {
		return err
	}
	account, err := resolveAgentAccount(accounts, opts.account, accountProviderForHarness(sess.Harness))
	if err != nil {
		return err
	}
	req := switchSessionAccountRequest{AccountID: account.ID, StopTurn: opts.stopTurn}
	if opts.workers || opts.noWorkers {
		include := opts.workers
		req.IncludeWorkers = &include
	}
	var res switchSessionAccountResponse
	if err := c.putJSON(ctx, "agents/accounts/sessions/"+url.PathEscape(id), req, &res); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	return writeSwitchAccountResult(cmd, id, account, res)
}

func writeSwitchAccountResult(cmd *cobra.Command, id string, account agentAccountDTO, res switchSessionAccountResponse) error {
	out := cmd.OutOrStdout()
	label := account.ID
	if account.Email != "" {
		label += " (" + account.Email + ")"
	}
	if res.Transition != nil {
		if _, err := fmt.Fprintf(out, "moving session %s to account %s\n", id, label); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(out, "session %s is already on account %s\n", id, label); err != nil {
		return err
	}
	if len(res.Workers) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(out, "also moved %d workers\n", res.MovedWorkers); err != nil {
		return err
	}
	for _, w := range res.Workers {
		if w.Status == "moving" {
			continue
		}
		line := fmt.Sprintf("  %s: %s", w.SessionID, w.Status)
		if w.Reason != "" {
			line += " (" + strings.ReplaceAll(w.Reason, "_", " ") + ")"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}
