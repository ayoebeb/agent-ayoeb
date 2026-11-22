package autopilot

import (
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// openQuestionPrefix marks a LastGap that is a question the agent asked the user
// while Autopilot was on. The next iteration quotes it back, and `decide` clears it.
const openQuestionPrefix = "You asked the user: "

// WaitDivertedGuidance is what the agent is told when it declares that it is
// waiting for the user while Autopilot is on. The user is not told anything.
const WaitDivertedGuidance = `Autopilot is on: do not wait for the user. Choose your recommended option, record it with ` +
	"`ao autopilot decide --question \"<what you asked>\" --choice \"<what you chose>\" --why \"<reason>\"`" +
	`, and continue. If every option would cross a hard limit, park only that item with ` +
	"`ao autopilot park`" + ` and keep working on the rest.`

func hardLimitLines() string {
	var b strings.Builder
	for _, l := range domain.HardLimits {
		fmt.Fprintf(&b, "- %s: never %s.\n", l, l.Description())
	}
	return b.String()
}

func hardLimitNames() string {
	names := make([]string, 0, len(domain.HardLimits))
	for _, l := range domain.HardLimits {
		names = append(names, string(l))
	}
	return strings.Join(names, "|")
}

const parkInstruction = "Meeting one: park only that item and carry on with everything else, " +
	"`ao autopilot park --limit <%s> --item \"<what you could not do>\" --why \"<why>\"`. AO tells the user once."

// startMessage is the first message of a run: the task, how the loop works, and
// the hard limits. It is the one place the agent learns the rules, so it states
// them in full.
func startMessage(run domain.AutopilotRun, kind domain.SessionKind) string {
	var b strings.Builder
	b.WriteString("Autopilot is ON for this session. The user has handed you this task and will not answer questions or approve anything while it runs. Finish it completely: done, verified and bug free.\n\n")
	b.WriteString("TASK\n")
	b.WriteString(run.Task)
	b.WriteString("\n\nHOW AUTOPILOT WORKS\n")
	b.WriteString("1. First, write the acceptance criteria: a short checklist that says exactly when this task is done and how you will prove it. Record it with `ao autopilot criteria \"<done when ...>\"`.\n")
	fmt.Fprintf(&b, "2. Work through the task. After every turn that did not finish it, AO sends you the task again (iteration 2, 3, ... of at most %d) until it is verifiably done.\n", run.MaxIterations)
	b.WriteString("3. Never wait for the user. If you would ask something, pick your recommended option and record it with `ao autopilot decide --question \"<what>\" --choice \"<what you chose>\" --why \"<reason>\"`, then continue.\n")
	b.WriteString("4. Permission prompts inside your workspace are answered for you, for you and for the workers you start. Keep at most 2 workers running at once, and start every worker and every review on a Sonnet model, never Opus.\n")
	b.WriteString("5. When every criterion is met and you have verified it (run the tests and builds that prove it), declare it with `ao autopilot done --evidence \"<the commands you ran and their results>\"`. ")
	b.WriteString("AO then checks the gates: for pull-request work an AO review of the exact head must be approved and CI must be green. If CI cannot start (for example a billing limit), say so with `--ci-cannot-run \"<why>\"`: that is recorded as a gap, not a pass. A gate that fails sends you back to work with the reason.\n")
	if kind == domain.KindOrchestrator {
		b.WriteString("6. You are the orchestrator: delegate to workers as usual. Merge only under the existing rule: an approved AO review of the exact head and green checks. Autopilot does not loosen it.\n")
	}
	b.WriteString("\nHARD LIMITS (Autopilot never does these, even if the task seems to ask for one)\n")
	b.WriteString(hardLimitLines())
	fmt.Fprintf(&b, parkInstruction+"\n", hardLimitNames())
	b.WriteString("\nThe user can stop Autopilot at any time. If you are told it was stopped, stop working at once.")
	return b.String()
}

// iterationMessage is the Ralph-loop message: the original task again, with where
// the run stands. next is the iteration about to start.
func iterationMessage(run domain.AutopilotRun, next int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Autopilot iteration %d of %d. The task is not verified done yet.\n\n", next, run.MaxIterations)
	b.WriteString("TASK\n")
	b.WriteString(run.Task)
	b.WriteString("\n\nACCEPTANCE CRITERIA\n")
	if strings.TrimSpace(run.Criteria) == "" {
		b.WriteString("None recorded yet. Record them first with `ao autopilot criteria \"<done when ...>\"`.")
	} else {
		b.WriteString(run.Criteria)
	}
	b.WriteString("\n\nWHAT IS STILL OPEN\n")
	if strings.TrimSpace(run.LastGap) == "" {
		b.WriteString("No done claim yet. Check each criterion against the real state, fix what fails, then verify.")
	} else {
		b.WriteString(run.LastGap)
	}
	b.WriteString("\n\nContinue the work. Do not ask the user anything: decide with your recommended option and record it with `ao autopilot decide`. ")
	b.WriteString("When every criterion is met and verified, run `ao autopilot done --evidence \"<the commands you ran and their results>\"`. Hard limits still apply: park the item, do not cross them.")
	if next >= run.MaxIterations {
		b.WriteString("\nThis is the last iteration: if the task is not done after it, Autopilot parks the run for the user.")
	}
	return b.String()
}

func resumeMessage(run domain.AutopilotRun, next int, reason string) string {
	return reason + "\n\n" + iterationMessage(run, next)
}

// undoMessage asks the agent to reverse one decision the user disagrees with.
func undoMessage(ev domain.AutopilotEvent, running bool) string {
	var b strings.Builder
	b.WriteString("The user asks you to undo a decision Autopilot recorded for you.\n")
	fmt.Fprintf(&b, "Decision: %s\n", clip(ev.Summary, 400))
	if ev.Detail != "" {
		fmt.Fprintf(&b, "Why you chose it: %s\n", clip(ev.Detail, 600))
	}
	b.WriteString("Reverse it: put back what that choice changed, or take the other option instead.")
	if running {
		b.WriteString(" Record the replacement with `ao autopilot decide --question \"...\" --choice \"...\" --why \"...\"` and carry on with the task.")
	}
	return b.String()
}

// stopMessage tells the agent the user ended the run.
func stopMessage() string {
	return "Autopilot was stopped by the user. Stop the autonomous work now: do not start new steps, workers or merges. " +
		"Say in one short message where things stand and what is left, then wait for the user."
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}
