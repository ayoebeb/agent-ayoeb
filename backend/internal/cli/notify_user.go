package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

// notifyUserMaxQuestionRunes mirrors the daemon's cap on the one-line question
// (orchestratorwait.MaxQuestionRunes). The CLI keeps its own copy so it need not
// import the service; the daemon enforces the cap and cuts a longer one.
const notifyUserMaxQuestionRunes = 240

// autopilotWaitGuidance is what the agent is told when it declares a wait while
// Autopilot runs for it. It mirrors autopilot.WaitDivertedGuidance, which the
// daemon also puts in the next iteration message.
const autopilotWaitGuidance = "Autopilot is on, so do not wait for the user. Choose your recommended option, " +
	"record it with `ao autopilot decide --question \"...\" --choice \"...\" --why \"...\"`, and continue. " +
	"If every option would cross a hard limit, park only that item with `ao autopilot park`."

type notifyUserOptions struct {
	waiting string
	clear   bool
}

// notifyUserDeclareRequest and notifyUserDeclareResponse mirror the daemon's
// DeclareOrchestratorWaitingRequest and DeclareOrchestratorWaitingResponse.
type notifyUserDeclareRequest struct {
	Question string `json:"question"`
}

type notifyUserDeclareResponse struct {
	Outcome string `json:"outcome"`
}

type notifyUserClearResponse struct {
	Cleared bool `json:"cleared"`
}

func newNotifyUserCommand(ctx *commandContext) *cobra.Command {
	var opts notifyUserOptions
	cmd := &cobra.Command{
		Use:   "notify-user",
		Short: "Tell the user this orchestrator is waiting for them",
		Long: `Tell the user this project orchestrator is waiting for their decision.

Run it as the last step of any turn that ends by asking the user something. The
user gets a clear notification (desktop toast with sound, phone push, a badge and
a marker in the sidebar) that names the project and shows your one-line question.
It stays open until the user replies, and AO reminds them once if it is still
unanswered after a delay they chose. Declaring the same question again while it
is open does nothing, so it is safe to run at the end of every turn that needs
the user.

Only a project orchestrator can use it: a worker asks its orchestrator with
"ao report --needs-input". The session is taken from AO_SESSION_ID.

  ao notify-user --waiting "Merge PR #42 now, or wait for the second review?"
  ao notify-user --clear`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.notifyUser(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.waiting, "waiting", "", "One-line question the user must answer, at most 240 characters")
	cmd.Flags().BoolVar(&opts.clear, "clear", false, "Withdraw the open question, for example because you found the answer yourself")
	return cmd
}

func (c *commandContext) notifyUser(ctx context.Context, opts notifyUserOptions) error {
	question := strings.TrimSpace(opts.waiting)
	if opts.clear && opts.waiting != "" {
		return usageError{errors.New("usage: --waiting and --clear cannot be combined")}
	}
	if !opts.clear && question == "" {
		return usageError{errors.New(`usage: --waiting "<one-line question>" or --clear is required`)}
	}
	if utf8.RuneCountInString(question) > notifyUserMaxQuestionRunes {
		return usageError{fmt.Errorf("usage: the question must be at most %d characters; say it in one line", notifyUserMaxQuestionRunes)}
	}
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return usageError{errors.New("usage: AO_SESSION_ID is required; run this from the orchestrator session")}
	}
	path := "sessions/" + url.PathEscape(sessionID) + "/waiting"

	if opts.clear {
		var out notifyUserClearResponse
		if err := c.deleteJSON(ctx, path, &out); err != nil {
			return err
		}
		if out.Cleared {
			_, _ = fmt.Fprintln(c.deps.Out, "Withdrew the question; the user is no longer shown as being waited on.")
		} else {
			_, _ = fmt.Fprintln(c.deps.Out, "Nothing was open to withdraw.")
		}
		return nil
	}

	var out notifyUserDeclareResponse
	if err := c.postJSON(ctx, path, notifyUserDeclareRequest{Question: question}, &out); err != nil {
		return err
	}
	switch out.Outcome {
	case "notified":
		_, _ = fmt.Fprintln(c.deps.Out, "The user has been notified. It stays open until they reply.")
	case "unchanged":
		_, _ = fmt.Fprintln(c.deps.Out, "That question is already open; the user was not notified again.")
	case "autopilot":
		_, _ = fmt.Fprintln(c.deps.Out, "Nothing was sent to the user: "+autopilotWaitGuidance)
	case "disabled":
		_, _ = fmt.Fprintln(c.deps.Out, "The user turned this notification off in Settings; nothing was sent. Ask in your reply as usual.")
	default:
		_, _ = fmt.Fprintf(c.deps.Out, "Done (%s).\n", out.Outcome)
	}
	return nil
}
