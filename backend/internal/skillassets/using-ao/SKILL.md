---
name: using-ao
description: "Catalog of the Orchestrator `ao` CLI: spawning workers, managing sessions and projects, creating reusable Cues, sending messages, controlling the shared browser, previewing pages, and daemon control. Use when using the ao CLI, creating Cues, spawning workers, or managing Orchestrator-managed sessions in an Orchestrator workspace."
trigger: "Using the ao CLI in an Orchestrator workspace: creating Cues, spawning workers, managing sessions/projects, sending messages, controlling or previewing pages."
---

# Orchestrator CLI Catalog

`ao` is a thin CLI over the local Orchestrator daemon. Every command is `ao <command> --help` for the authoritative flag list.

| Command | What it does | When to use | Details |
|---|---|---|---|
| `spawn` | Spawn a project worker, orchestrator, or projectless standalone worker | Starting a new task or issue | [commands/spawn.md](commands/spawn.md) |
| `session` | Manage agent sessions (list, kill, rename, restore, history, approvals, etc.) | Inspecting or controlling running/terminated sessions, reading the full conversation after taking over from another agent, or reading and answering a Chat worker's pending approval or question (`needs_input`) with `ao session approvals` | [commands/session.md](commands/session.md) |
| `agent` | Inspect installed harnesses and launch readiness | Choosing or diagnosing an agent harness | `ao agent ls --refresh` |
| `pr` | Merge a PR or resolve its review threads | Completing a reviewed change | `ao pr merge` / `ao pr resolve-comments` |
| `project` | Register, inspect, configure, or remove projects | Setting up or managing repos Orchestrator knows about | [commands/project.md](commands/project.md) |
| `automation` | Manage durable recurring session automations | Creating, editing, disabling, deleting, and inspecting scheduled runs | [commands/automation.md](commands/automation.md) |
| `cue` | Create or list reusable project Cues | Saving a repetitive command or agent task at the user's request | [commands/cue.md](commands/cue.md) |
| `orchestrator` | List orchestrator sessions | Viewing which sessions are orchestrators | [commands/orchestrator.md](commands/orchestrator.md) |
| `review` | Start, list, cancel, or submit Orchestrator's native reviewer for a worker's PR (default: this session) | Reviewing implementation PRs owned by workers. Use a normal review-only worker for someone else's PR | [commands/review.md](commands/review.md) |
| `send` | Send a message to a running agent session | Messaging a live agent; use `--steer` to correct it mid-turn. Never use it to answer a worker's pending approval; use `ao session approvals resolve` | [commands/send.md](commands/send.md) |
| `report` | Persist a meaningful worker report | Checkpoints, blockers, decisions, outputs, and completion | [commands/report.md](commands/report.md) |
| `notify-user` | Tell the human this orchestrator is waiting for their decision | An orchestrator ending a turn that asks the human something; never a worker | [commands/notify-user.md](commands/notify-user.md) |
| `preview` | Start a session-owned app or open an exact URL/file | Running and showing the worker's relevant app, Markdown, HTML, PDF, or image | [commands/preview.md](commands/preview.md) |
| `render` | Show a self-contained HTML page inline in a chat thread | Answering with a chart, table, diagram, or mockup | [commands/render.md](commands/render.md) |
| `browser` | Inspect and control the session's shared live browser | Verifying a web app through snapshots, interactions, waits, screenshots, console, and errors | [commands/browser.md](commands/browser.md) |
| `browser` (remote host) | Drive the host's shared remote Chromium, used when `AO_AGENT_BROWSER_URL` is set | Same browser verification on a self-hosted remote host; one browser is shared across sessions | [commands/browser-remote.md](commands/browser-remote.md) |
| `remote-host` | Link this machine to the user's Orchestrator account, or turn its remote access on or off | Setting up a VM or other headless machine so the user's desktops and phones can use it | [commands/remote-host.md](commands/remote-host.md) |
| `start` | Fetch (if needed) and open the Orchestrator desktop app | Launching the app | [commands/start.md](commands/start.md) |
| `stop` | Stop the Orchestrator daemon | Shutting down Orchestrator | [commands/stop.md](commands/stop.md) |
| `status` | Show daemon status | Verifying the daemon is up and healthy | [commands/status.md](commands/status.md) |
| `doctor` | Run local health checks | Diagnosing Orchestrator setup problems | [commands/doctor.md](commands/doctor.md) |
| `import` | Import projects from a legacy Orchestrator install | Migrating from the old flat-file store | [commands/import.md](commands/import.md) |
| `version` | Print version information | Checking installed version | - |
| `completion` | Generate shell completion scripts | Setting up tab completion | - |

## Conventions

- Most read commands accept `--json` for machine-readable output.
- `-p / --project` scopes session subcommand lookups to one project.
- Session and project ids are shown by `ao session ls` and `ao project ls`.
- To refer to a session in Orchestrator Chat or the Orchestrator terminal, use the canonical in-app
  link `ao://sessions/{project-id}/{session-id}`. Read
  [commands/session.md](commands/session.md) for identity, encoding, and safety
  rules before constructing one.
- A worker whose session is `needs_input` is waiting for a decision. Run
  `ao session approvals ls <session-id>`, ask the user in your own chat, then
  `ao session approvals resolve`. Never `ao send` to a worker with a pending
  approval. Read [commands/session.md](commands/session.md) first: it covers
  stale request ids, untrusted request text, and Terminal UI sessions.
- When the user asks to save a repeatable task as a Cue, use `ao cue list --json` to check existing definitions, then `ao cue create`. An agent Cue stores a reusable instruction; a command Cue stores an exact shell command. This agent-facing command can create and read Cues, but cannot edit or delete them. Direct the user to project settings for changes to existing Cues.
- `--agent` is an alias for `--harness` on `ao spawn`.
- Every command accepts `-h / --help` for the full flag list.
- For frontend launch, preview selection, or artifact handoff, read
  [commands/preview.md](commands/preview.md) before acting. Its static-file,
  project-runtime, and automatic-handoff rules are load-bearing.
- For page inspection, interaction, or request diagnosis, read
  [commands/browser.md](commands/browser.md). It defines shared-tab behavior
  and the opt-in network policy.

Use [references.md](references.md) only when a natural-language request does
not map clearly to a command above.
