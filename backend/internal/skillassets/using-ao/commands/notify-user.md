# ao notify-user

Tell the human that this project orchestrator is waiting for them. Only a
project orchestrator can use it; a worker asks its orchestrator with
`ao report --needs-input`.

The orchestrator is the human's main contact. A question that ends a turn but
raises no signal sits unseen, and every worker behind it stalls. This command is
that signal: the human gets a distinct notification titled "<project>:
orchestrator is waiting for you" (in their own language on the desktop) with your
one-line question (desktop toast with sound, phone push, the app badge, and a
marker on the project and the orchestrator in the sidebar). Clicking it opens
your chat.

## Syntax

```text
ao notify-user --waiting "<one-line question>"
ao notify-user --clear
```

`AO_SESSION_ID` selects your session. Run it from the orchestrator session.

## When to run it

Run `ao notify-user --waiting` as the last step of any turn that ends by asking
the human to decide, answer, approve, or choose something, in addition to
writing the question in your reply. Do not run it when the turn ends with
nothing needed from the human, and do not run it for a worker's question you can
answer yourself.

The question is one line, at most 240 characters, and must stand alone: it is
shown in a toast and on a phone with no other context. Say what they must decide,
not that you have an update.

## What happens next

- It stays open until the human replies in your chat, answers your approval, or
  you run `ao notify-user --clear`. A message from a worker does not close it.
- If it is still open after the delay the human chose (10 minutes by default),
  AO announces it once more, and never again.
- Declaring the same question again while it is open does nothing, so it is safe
  to run at the end of every turn that still needs the human. A different
  question replaces the old one and notifies again.
- If the human turned this notification off in Settings, the command says so and
  sends nothing; ask in your reply as usual.
- Pending approvals and input requests (the question dock) already raise this
  notification on their own. Do not declare them again.
- While Autopilot runs for you, the human is not told: the command answers
  "Nothing was sent to the user" and reminds you to decide instead. Choose your
  recommended option and record it with `ao autopilot decide` (see
  [autopilot.md](autopilot.md)).

**Examples:**

```bash
ao notify-user --waiting "Merge PR #42 now, or wait for the second review?"
ao notify-user --clear
```
