package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// The daemon's hard limits, in the order the agent's instructions state them.
// Mirrors domain.HardLimits; the daemon validates the value.
var autopilotLimits = []string{"money", "ci", "main_push", "data_delete", "admin", "messaging", "credentials", "other_pcs"}

// Mirrors of the daemon's Autopilot DTOs (controllers.AutopilotViewResponse and
// friends). The CLI keeps its own copy so it need not import the controllers.
type autopilotRun struct {
	ID                  string     `json:"id"`
	SessionID           string     `json:"sessionId"`
	Task                string     `json:"task"`
	Criteria            string     `json:"criteria"`
	State               string     `json:"state"`
	EndReason           string     `json:"endReason"`
	Iteration           int        `json:"iteration"`
	MaxIterations       int        `json:"maxIterations"`
	StagnantIterations  int        `json:"stagnantIterations"`
	LastGap             string     `json:"lastGap"`
	Evidence            string     `json:"evidence"`
	CIGap               string     `json:"ciGap"`
	PermissionsAllowed  int        `json:"permissionsAllowed"`
	PermissionsDenied   int        `json:"permissionsDenied"`
	StartedAt           time.Time  `json:"startedAt"`
	EndedAt             *time.Time `json:"endedAt"`
	OpenParkedItemCount int        `json:"openParkedItemCount"`
}

type autopilotEvent struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Iteration  int        `json:"iteration"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
}

type autopilotMergedPR struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

type autopilotReport struct {
	Task      string              `json:"task"`
	State     string              `json:"state"`
	MergedPRs []autopilotMergedPR `json:"mergedPrs"`
	Decisions []autopilotEvent    `json:"decisions"`
	Parked    []autopilotEvent    `json:"parked"`
	Gaps      []string            `json:"gaps"`
}

type autopilotView struct {
	Run      *autopilotRun    `json:"run"`
	Events   []autopilotEvent `json:"events"`
	Parked   []autopilotEvent `json:"parked"`
	Warnings []string         `json:"warnings"`
	Report   *autopilotReport `json:"report"`
}

type autopilotStartRequest struct {
	Task          string `json:"task"`
	MaxIterations *int   `json:"maxIterations,omitempty"`
}

type autopilotCriteriaRequest struct {
	Criteria string `json:"criteria"`
}

type autopilotDoneRequest struct {
	Evidence    string `json:"evidence"`
	CICannotRun string `json:"ciCannotRun,omitempty"`
}

type autopilotDoneResponse struct {
	Accepted bool         `json:"accepted"`
	Failures []string     `json:"failures"`
	Gaps     []string     `json:"gaps"`
	Run      autopilotRun `json:"run"`
}

type autopilotDecideRequest struct {
	Question   string   `json:"question"`
	Choice     string   `json:"choice"`
	Why        string   `json:"why"`
	BasedOn    []string `json:"basedOn,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

type autopilotParkRequest struct {
	Limit string `json:"limit"`
	Item  string `json:"item"`
	Why   string `json:"why"`
}

type autopilotParkResponse struct {
	Event         autopilotEvent `json:"event"`
	AlreadyParked bool           `json:"alreadyParked"`
}

type autopilotOptions struct {
	session       string
	json          bool
	all           bool
	maxIterations int
	limit         int
	evidence      string
	ciCannotRun   string
	question      string
	choice        string
	why           string
	parkLimit     string
	item          string
	basedOn       []string
	confidence    string
}

func newAutopilotCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autopilot",
		Short: "Run one task to completion without waiting for the user",
		Long: `Autopilot gives a session one task and keeps it on that task until it is verifiably
done, without waiting for the user. The user switches it on (the Autopilot
control in the app, or "ao autopilot start"); while it runs, AO sends the task
again at the end of every turn that did not finish it, answers permission
requests by policy, and never waits on a question: you decide and record it.

For the user:
  ao autopilot start "<task>" --session <id>   put a task on a session
  ao autopilot status [--session <id>]         where the run stands
  ao autopilot log [--session <id>]            what it did while you were away
  ao autopilot stop [--session <id> | --all]   emergency stop
  ao autopilot resume --session <id>           continue a parked run

For the agent doing the task (the session is taken from AO_SESSION_ID):
  ao autopilot criteria "<done when ...>"      record the acceptance criteria
  ao autopilot advise --question ...           how the owner would decide it
  ao autopilot decide --question ... --choice ... --why ... [--based-on <ids> --confidence high|medium|low]
  ao autopilot park --limit <name> --item ... --why ...
  ao autopilot done --evidence "<tests you ran and their results>"`,
	}
	cmd.AddCommand(
		newAutopilotStartCommand(ctx),
		newAutopilotStatusCommand(ctx),
		newAutopilotLogCommand(ctx),
		newAutopilotStopCommand(ctx),
		newAutopilotResumeCommand(ctx),
		newAutopilotCriteriaCommand(ctx),
		newAutopilotDecideCommand(ctx),
		newAutopilotAdviseCommand(ctx),
		newAutopilotLearnCommand(ctx),
		newAutopilotParkCommand(ctx),
		newAutopilotDoneCommand(ctx),
	)
	return cmd
}

// autopilotSession is the session a command acts on: --session, or the calling
// session from AO_SESSION_ID.
func autopilotSession(opts autopilotOptions) (string, error) {
	id := strings.TrimSpace(opts.session)
	if id == "" {
		id = strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	}
	if id == "" {
		return "", usageError{errors.New("usage: pass --session <id>, or run this from the session's shell (AO_SESSION_ID)")}
	}
	return id, nil
}

// autopilotOwnSession is the calling session, for the commands only the agent doing
// the task may run.
func autopilotOwnSession() (string, error) {
	id := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if id == "" {
		return "", usageError{errors.New("usage: AO_SESSION_ID is required; run this from the session that has the task")}
	}
	return id, nil
}

func autopilotPath(id, tail string) string {
	return "sessions/" + url.PathEscape(id) + "/autopilot" + tail
}

func newAutopilotStartCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   `start "<task>"`,
		Short: "Give a session one task to finish on its own",
		Long: `Put one task on a session and switch Autopilot on for it. AO sends the session the
task and its rules, then keeps it on the task until it is verifiably done, parks
it for you (out of iterations, or stuck), or you stop it. Only you can run this:
an agent cannot switch Autopilot on for itself.

  ao autopilot start "Make the importer twice as fast and keep the tests green" --session mer-orchestrator-1`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.autopilotStart(cmd.Context(), args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Session that gets the task (default: AO_SESSION_ID)")
	cmd.Flags().IntVar(&opts.maxIterations, "max-iterations", 0, "Most iterations before the run parks itself (default: the Settings value)")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print the run as JSON")
	return cmd
}

func (c *commandContext) autopilotStart(ctx context.Context, task string, opts autopilotOptions) error {
	task = strings.TrimSpace(task)
	if task == "" {
		return usageError{errors.New(`usage: ao autopilot start "<task>": say what the task is`)}
	}
	if opts.maxIterations < 0 {
		return usageError{errors.New("usage: --max-iterations must be at least 1")}
	}
	id, err := autopilotSession(opts)
	if err != nil {
		return err
	}
	req := autopilotStartRequest{Task: task}
	if opts.maxIterations > 0 {
		req.MaxIterations = &opts.maxIterations
	}
	var view autopilotView
	if err := c.postJSON(ctx, autopilotPath(id, ""), req, &view); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(c.deps.Out, view)
	}
	if view.Run != nil {
		_, _ = fmt.Fprintf(c.deps.Out, "Autopilot is on for %s: iteration 0 of %d. Stop it any time with `ao autopilot stop --session %s`.\n",
			id, view.Run.MaxIterations, id)
	}
	for _, w := range view.Warnings {
		_, _ = fmt.Fprintf(c.deps.Out, "Note: %s\n", w)
	}
	return nil
}

func newAutopilotStatusCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show where a session's Autopilot run stands",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.autopilotStatus(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Session to read (default: AO_SESSION_ID)")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print the run as JSON")
	return cmd
}

func (c *commandContext) autopilotStatus(ctx context.Context, opts autopilotOptions) error {
	id, err := autopilotSession(opts)
	if err != nil {
		return err
	}
	var view autopilotView
	if err := c.getJSON(ctx, autopilotPath(id, ""), &view); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(c.deps.Out, view)
	}
	writeAutopilotStatus(c.deps.Out, id, view)
	return nil
}

func writeAutopilotStatus(w io.Writer, id string, view autopilotView) {
	if view.Run == nil {
		_, _ = fmt.Fprintf(w, "Autopilot has not run on %s.\n", id)
		return
	}
	run := view.Run
	_, _ = fmt.Fprintf(w, "Autopilot on %s: %s\n", id, autopilotStateText(*run))
	_, _ = fmt.Fprintf(w, "Task: %s\n", clipText(run.Task, 200))
	if strings.TrimSpace(run.Criteria) == "" {
		_, _ = fmt.Fprintln(w, "Criteria: not recorded yet")
	} else {
		_, _ = fmt.Fprintf(w, "Criteria: %s\n", clipText(run.Criteria, 200))
	}
	if run.PermissionsAllowed+run.PermissionsDenied > 0 {
		_, _ = fmt.Fprintf(w, "Permission requests: %d allowed, %d denied\n", run.PermissionsAllowed, run.PermissionsDenied)
	}
	if strings.TrimSpace(run.LastGap) != "" {
		_, _ = fmt.Fprintf(w, "Still open: %s\n", clipText(run.LastGap, 300))
	}
	if run.CIGap != "" {
		_, _ = fmt.Fprintf(w, "CI could not run: %s\n", run.CIGap)
	}
	if n := len(view.Parked); n > 0 {
		_, _ = fmt.Fprintf(w, "%d item(s) parked for the user; `ao autopilot log` lists them.\n", n)
	}
	for _, warn := range view.Warnings {
		_, _ = fmt.Fprintf(w, "Note: %s\n", warn)
	}
}

func autopilotStateText(run autopilotRun) string {
	switch run.State {
	case "running":
		return fmt.Sprintf("running, iteration %d of %d", run.Iteration+1, run.MaxIterations)
	case "paused_usage_limit":
		return "paused on the usage limit; it continues after the reset"
	case "parked":
		return "parked, waiting for the user (" + autopilotReasonText(run.EndReason) + ")"
	case "done":
		return "finished: the task was declared done and every check passed"
	case "stopped":
		return "stopped (" + autopilotReasonText(run.EndReason) + ")"
	}
	return run.State
}

func autopilotReasonText(reason string) string {
	switch reason {
	case "max_iterations":
		return "it used all its iterations"
	case "stuck":
		return "nothing changed for three iterations"
	case "needs_input":
		return "a session sat on a prompt Autopilot cannot answer"
	case "emergency_stop":
		return "stopped by the user"
	case "user":
		return "ended by the user"
	case "session_ended":
		return "the session ended"
	case "goal_done":
		return "done"
	}
	return reason
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		return strings.TrimSpace(string([]rune(s)[:n-1])) + "…"
	}
	return s
}

func newAutopilotLogCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show what Autopilot did: decisions, parked items, merged PRs, the log",
		Long: `Show the return report ("what I did while you were away") and the run log for a
session: the pull requests that merged, the decisions the agent took instead of
asking, the items parked for you, and what the run could not close.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.autopilotLog(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Session to read (default: AO_SESSION_ID)")
	cmd.Flags().IntVar(&opts.limit, "limit", 30, "Most log lines to show")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print the run, log and report as JSON")
	return cmd
}

func (c *commandContext) autopilotLog(ctx context.Context, opts autopilotOptions) error {
	id, err := autopilotSession(opts)
	if err != nil {
		return err
	}
	if opts.limit < 1 {
		return usageError{errors.New("usage: --limit must be at least 1")}
	}
	var view autopilotView
	if err := c.getJSON(ctx, autopilotPath(id, ""), &view); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(c.deps.Out, view)
	}
	writeAutopilotStatus(c.deps.Out, id, view)
	if view.Run == nil {
		return nil
	}
	if rep := view.Report; rep != nil {
		_, _ = fmt.Fprintln(c.deps.Out, "\nWhat Autopilot did:")
		if len(rep.MergedPRs) == 0 {
			_, _ = fmt.Fprintln(c.deps.Out, "  Merged pull requests: none")
		}
		for _, pr := range rep.MergedPRs {
			_, _ = fmt.Fprintf(c.deps.Out, "  Merged: %s (%s)\n", clipText(pr.Title, 100), pr.URL)
		}
		for _, d := range rep.Decisions {
			_, _ = fmt.Fprintf(c.deps.Out, "  Decided: %s\n    because %s\n", clipText(d.Summary, 200), clipText(d.Detail, 200))
		}
		for _, p := range rep.Parked {
			state := "needs you"
			if p.ResolvedAt != nil {
				state = "handled"
			}
			_, _ = fmt.Fprintf(c.deps.Out, "  Parked (%s): %s\n    %s\n", state, clipText(p.Summary, 200), clipText(p.Detail, 200))
		}
		for _, g := range rep.Gaps {
			_, _ = fmt.Fprintf(c.deps.Out, "  Gap: %s\n", g)
		}
	}
	events := view.Events
	if len(events) > opts.limit {
		events = events[:opts.limit]
	}
	_, _ = fmt.Fprintf(c.deps.Out, "\nLog (oldest first, %d line(s)):\n", len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		_, _ = fmt.Fprintf(c.deps.Out, "  %s  %-17s %s\n", ev.CreatedAt.Local().Format("2006-01-02 15:04"), ev.Kind, clipText(ev.Summary, 160))
	}
	return nil
}

func newAutopilotStopCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Emergency stop: end the run and cut the session's current turn",
		Long: `Stop Autopilot at once: the run ends, the session's current turn is interrupted, and
the session is told to stop. Workers it started keep running; stop them
separately. Only you can run this.

  ao autopilot stop --session mer-orchestrator-1
  ao autopilot stop --all`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.autopilotStop(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Session whose run to stop (default: AO_SESSION_ID)")
	cmd.Flags().BoolVar(&opts.all, "all", false, "Stop every open Autopilot run")
	return cmd
}

func (c *commandContext) autopilotStop(ctx context.Context, opts autopilotOptions) error {
	if opts.all {
		if strings.TrimSpace(opts.session) != "" {
			return usageError{errors.New("usage: --all and --session cannot be combined")}
		}
		var out struct {
			Stopped int `json:"stopped"`
		}
		if err := c.postJSON(ctx, "autopilot/stop", struct{}{}, &out); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(c.deps.Out, "Stopped %d Autopilot run(s).\n", out.Stopped)
		return nil
	}
	id, err := autopilotSession(opts)
	if err != nil {
		return err
	}
	var out struct {
		Stopped bool `json:"stopped"`
	}
	if err := c.postJSON(ctx, autopilotPath(id, "/stop"), struct{}{}, &out); err != nil {
		return err
	}
	if out.Stopped {
		_, _ = fmt.Fprintf(c.deps.Out, "Autopilot stopped on %s.\n", id)
	} else {
		_, _ = fmt.Fprintf(c.deps.Out, "Autopilot was not running on %s.\n", id)
	}
	return nil
}

func newAutopilotResumeCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Continue a parked run",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := autopilotSession(opts)
			if err != nil {
				return err
			}
			var view autopilotView
			if err := ctx.postJSON(cmd.Context(), autopilotPath(id, "/resume"), struct{}{}, &view); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(ctx.deps.Out, "Autopilot continues on %s.\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "Session whose parked run to continue (default: AO_SESSION_ID)")
	return cmd
}

func newAutopilotCriteriaCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   `criteria "<done when ...>"`,
		Short: "Record the acceptance criteria for your Autopilot task",
		Long: `Record, in your first turn, the checklist that says exactly when the task is done
and how you will prove it. AO quotes it back at every iteration and refuses a
done claim while it is missing. Run it again to replace it.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := autopilotOwnSession()
			if err != nil {
				return err
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New(`usage: ao autopilot criteria "<done when ...>"`)}
			}
			var run autopilotRun
			if err := ctx.postJSON(cmd.Context(), autopilotPath(id, "/criteria"), autopilotCriteriaRequest{Criteria: args[0]}, &run); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(ctx.deps.Out, "Acceptance criteria recorded.")
			return nil
		},
	}
}

func newAutopilotDecideCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "decide",
		Short: "Record a choice you made instead of asking the user",
		Long: `While Autopilot runs you never wait for the user. When you would ask something,
pick your recommended option, record it here with the reason, and continue.
The user reads these in the log afterwards. A choice that would cross a hard
limit is refused: park that item instead.

  ao autopilot decide --question "Patch or refactor?" --choice "Patch the one function" --why "The task is one bug"`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := autopilotOwnSession()
			if err != nil {
				return err
			}
			if strings.TrimSpace(opts.question) == "" || strings.TrimSpace(opts.choice) == "" || strings.TrimSpace(opts.why) == "" {
				return usageError{errors.New("usage: --question, --choice and --why are all required")}
			}
			var out struct {
				Event autopilotEvent `json:"event"`
			}
			switch opts.confidence {
			case "", "high", "medium", "low":
			default:
				return usageError{errors.New("usage: --confidence must be high, medium or low")}
			}
			req := autopilotDecideRequest{Question: opts.question, Choice: opts.choice, Why: opts.why, BasedOn: opts.basedOn, Confidence: opts.confidence}
			if err := ctx.postJSON(cmd.Context(), autopilotPath(id, "/decisions"), req, &out); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(ctx.deps.Out, "Decision recorded. Continue with it.")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.question, "question", "", "What you would have asked the user")
	cmd.Flags().StringVar(&opts.choice, "choice", "", "The option you chose")
	cmd.Flags().StringVar(&opts.why, "why", "", "Why that option")
	cmd.Flags().StringSliceVar(&opts.basedOn, "based-on", nil, "Ids of the owner's past decisions this followed (from `ao autopilot advise`)")
	cmd.Flags().StringVar(&opts.confidence, "confidence", "", "How sure you are the owner would choose this: high, medium or low (low: safest option that can be undone)")
	return cmd
}

func newAutopilotParkCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "park",
		Short: "Park one item that needs a hard limit crossed",
		Long: `When one item would need a hard limit crossed, set only that item aside: AO tells
the user once, and you carry on with everything else. The hard limits are
money, ci, main_push, data_delete, admin, messaging, credentials and other_pcs
(spend money or change billing; disable, skip or weaken CI; push to main without
review; delete data outside the workspace; use administrator rights; message
people outside AO; change logins or credentials; act on another computer's
credentials).

  ao autopilot park --limit money --item "Upgrade the CI plan" --why "The free minutes ran out"`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := autopilotOwnSession()
			if err != nil {
				return err
			}
			if strings.TrimSpace(opts.item) == "" || strings.TrimSpace(opts.why) == "" {
				return usageError{errors.New("usage: --limit, --item and --why are all required")}
			}
			valid := false
			for _, l := range autopilotLimits {
				valid = valid || l == opts.parkLimit
			}
			if !valid {
				return usageError{fmt.Errorf("usage: --limit must be one of: %s", strings.Join(autopilotLimits, ", "))}
			}
			var out autopilotParkResponse
			req := autopilotParkRequest{Limit: opts.parkLimit, Item: opts.item, Why: opts.why}
			if err := ctx.postJSON(cmd.Context(), autopilotPath(id, "/parked"), req, &out); err != nil {
				return err
			}
			if out.AlreadyParked {
				_, _ = fmt.Fprintln(ctx.deps.Out, "That item is already parked; the user was not told again. Carry on with the rest.")
			} else {
				_, _ = fmt.Fprintln(ctx.deps.Out, "Parked, and the user has been told once. Carry on with everything else.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.parkLimit, "limit", "", "The hard limit the item would cross: "+strings.Join(autopilotLimits, ", "))
	cmd.Flags().StringVar(&opts.item, "item", "", "What you could not do")
	cmd.Flags().StringVar(&opts.why, "why", "", "Why it needs the limit crossed")
	return cmd
}

func newAutopilotDoneCommand(ctx *commandContext) *cobra.Command {
	var opts autopilotOptions
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Declare the task done; AO checks the gates",
		Long: `Declare your Autopilot task done. Run it only when every acceptance criterion is
met and you have verified it by running the tests and builds that prove it.
--evidence is what you ran and what it showed.

AO then checks the gates: the criteria are recorded, and for pull-request work
an AO review of the exact head is approved and CI is green. A gate that fails
sends you back to work with the reason, and the command exits non-zero.

If CI cannot start (for example a billing limit), say so with --ci-cannot-run
"<why>": that is recorded as a gap in the user's report, never counted as a pass.

  ao autopilot done --evidence "go test ./... ok; go vet clean; PR #7 approved"`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.autopilotDone(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.evidence, "evidence", "", "The test and build commands you ran and their results")
	cmd.Flags().StringVar(&opts.ciCannotRun, "ci-cannot-run", "", "Why CI cannot start, recorded as a gap")
	return cmd
}

func (c *commandContext) autopilotDone(ctx context.Context, opts autopilotOptions) error {
	id, err := autopilotOwnSession()
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.evidence) == "" {
		return usageError{errors.New(`usage: --evidence "<the commands you ran and their results>" is required`)}
	}
	var out autopilotDoneResponse
	if err := c.postJSON(ctx, autopilotPath(id, "/done"), autopilotDoneRequest{Evidence: opts.evidence, CICannotRun: opts.ciCannotRun}, &out); err != nil {
		return err
	}
	for _, g := range out.Gaps {
		_, _ = fmt.Fprintf(c.deps.Out, "Recorded gap: %s\n", g)
	}
	if out.Accepted {
		_, _ = fmt.Fprintln(c.deps.Out, "Done. Autopilot has finished this task.")
		return nil
	}
	_, _ = fmt.Fprintln(c.deps.Out, "Not done yet. These checks failed:")
	for _, f := range out.Failures {
		_, _ = fmt.Fprintf(c.deps.Out, "- %s\n", f)
	}
	return errors.New("the done claim was refused; fix what is listed above and claim done again")
}
