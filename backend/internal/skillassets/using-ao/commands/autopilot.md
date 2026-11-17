# ao autopilot

Autopilot gives one session one task and keeps it on that task, without the
human, until it is verifiably done. The human switches it on; you finish the
task. While it runs, AO sends you the task again at the end of every turn that
did not finish it, answers permission requests by policy, and never waits on a
question: you decide and record the decision.

Only the human can switch Autopilot on, off or back on (`start`, `stop`,
`resume`). If you are told Autopilot is ON, the commands you use are `criteria`,
`decide`, `park` and `done`. They act on your own session (`AO_SESSION_ID`).

## Syntax

```text
ao autopilot criteria "<done when ...>"
ao autopilot decide --question <text> --choice <text> --why <text>
ao autopilot park --limit <name> --item <text> --why <text>
ao autopilot done --evidence <text> [--ci-cannot-run <why>]

ao autopilot status [--session <id>] [--json]
ao autopilot log [--session <id>] [--limit <n>] [--json]

ao autopilot start "<task>" [--session <id>] [--max-iterations <n>] [--json]
ao autopilot stop [--session <id> | --all]
ao autopilot resume [--session <id>]
```

## How a run goes

1. The human starts it with a task. AO sends you the task, these rules, and the
   hard limits.
2. **First turn: record the acceptance criteria.** Write a short checklist that
   says exactly when the task is done and how you will prove it, and record it
   with `ao autopilot criteria`. AO quotes it back at every iteration and
   refuses a done claim while it is missing.
3. **Work.** After every turn that did not finish the task, AO sends the task
   again as "Autopilot iteration N of M" with the criteria and what is still
   open. Iterations are capped (30 by default, set in Settings).
4. **Never wait for the human.** When you would ask something, choose your
   recommended option and record it with `ao autopilot decide`, then continue.
   `ao notify-user --waiting` does nothing to the human while Autopilot runs; it
   reminds you to decide. Do not use a question tool: a form nobody answers
   parks the run.
5. **Permission prompts** inside your workspace are answered for you, and for
   the workers you start. Keep at most 3 workers running at once.
6. **Finish with `ao autopilot done --evidence "..."`** only when every
   criterion is met and you have verified it: run the tests and builds that
   prove it and say what you ran and what it showed. Evidence is your own
   account; AO records it and shows it to the human as such.

## The gates on `done`

`done` finishes the run only if every gate passes. A failed gate leaves the run
going: the command prints what failed, exits non-zero, and the next iteration
starts from that.

- The acceptance criteria are recorded.
- Evidence is given.
- For every open pull request of your task (yours, and for an orchestrator the
  workers started after the run began): an AO review of the **exact current
  head** is approved, and the PR is not a draft.
- CI is green. If CI cannot start (for example a billing limit), claim done again
  with `--ci-cannot-run "<why>"`. That is **recorded as a gap in the human's
  report, never counted as a pass**. Without it, red or pending CI fails the gate.

## Hard limits

Autopilot never does these, even if the task seems to ask for one:

| `--limit` | Never |
|---|---|
| `money` | spend money or change billing or payment settings |
| `ci` | disable, skip or weaken CI |
| `main_push` | push to the default branch, or merge without an approved AO review of the exact head and green checks |
| `data_delete` | delete data outside the session's own workspace |
| `admin` | use administrator or elevated rights |
| `messaging` | message people outside AO (email, chat, issue or PR comments to others) |
| `credentials` | change logins, tokens, passwords or credentials |
| `other_pcs` | act on another computer's credentials |

When one item needs a limit crossed, **park only that item**:

```bash
ao autopilot park --limit money --item "Upgrade the CI plan" --why "The free minutes ran out"
```

The human gets one notification for it, it is recorded in the log, and you carry
on with everything else. Parking the same open item again does nothing. A
`decide` whose choice would cross a limit is refused: park it instead.

The permission policy enforces the same list for the Chat sessions it answers:
it allows anything inside the workspace (edits, tests, builds, git on your
branch, `gh pr create`, pushing `ao/*` branches), and denies a limit's request
and parks it for you. It decides `gh pr merge` against AO's records: allowed only
when an AO review approved the exact head and checks are green, which is the rule
you already follow. Autopilot does not loosen it.

## What stops a run

- Your verified `done`.
- The human's stop button or `ao autopilot stop`: the run ends at once and your
  current turn is interrupted. If you are told it was stopped, stop working.
- The iteration cap, or three iterations in a row where nothing changed (no new
  commit, no changed file, no new test result): the run **parks** and the human is
  told once. They continue it or stop it.
- A usage limit pauses it; it continues by itself after the reset.
- The session ending.

A terminal (TUI) session keeps its permission prompts inside the terminal, where
AO cannot answer them. Start Autopilot sessions in auto or bypass-permissions
mode. A prompt that sits unanswered for about 90 seconds parks the run.

## Reading it back

`ao autopilot status` says where the run stands. `ao autopilot log` prints the
return report ("what Autopilot did while you were away"): merged pull requests,
the decisions taken, items parked for the human, what could not be closed, and
the log, oldest first.

**Examples:**

```bash
ao autopilot criteria "Done when: importer benchmark is 2x faster, go test ./... passes, PR approved by the AO review"
ao autopilot decide --question "Patch or refactor?" --choice "Patch the one function" --why "The task is a single bug; a refactor touches 40 files"
ao autopilot park --limit messaging --item "Tell the customer the fix shipped" --why "Messaging people outside AO is a hard limit"
ao autopilot done --evidence "go test ./... ok (214 tests); go vet clean; benchmark 2.3x; PR #12 reviewed"
ao autopilot done --evidence "go test ./... ok" --ci-cannot-run "GitHub Actions is blocked by a billing limit"
```
