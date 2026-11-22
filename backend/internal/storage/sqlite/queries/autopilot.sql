-- Autopilot task runs and their log. See migration 20261122090000000.

-- name: CreateAutopilotRun :one
INSERT INTO autopilot_runs (
    id, session_id, project_id, task, criteria, state, max_iterations,
    started_at, updated_at
) VALUES (?, ?, ?, ?, ?, 'running', ?, ?, ?)
RETURNING *;

-- name: GetAutopilotRun :one
SELECT * FROM autopilot_runs WHERE id = ?;

-- name: GetOpenAutopilotRunForSession :one
SELECT *
FROM autopilot_runs
WHERE session_id = ?
  AND state IN ('running', 'paused_usage_limit', 'parked')
LIMIT 1;

-- name: GetLatestAutopilotRunForSession :one
SELECT *
FROM autopilot_runs
WHERE session_id = ?
ORDER BY started_at DESC, id DESC
LIMIT 1;

-- name: ListOpenAutopilotRuns :many
SELECT *
FROM autopilot_runs
WHERE state IN ('running', 'paused_usage_limit', 'parked')
ORDER BY started_at, id;

-- The runs that still drive their project's work: a parked run waits for the
-- user and answers nothing on its own.
-- name: ListActiveAutopilotRunsForProject :many
SELECT *
FROM autopilot_runs
WHERE project_id = ?
  AND state IN ('running', 'paused_usage_limit')
ORDER BY started_at, id;

-- name: UpdateAutopilotRun :one
UPDATE autopilot_runs
SET criteria = sqlc.arg(criteria),
    state = sqlc.arg(state),
    end_reason = sqlc.arg(end_reason),
    iteration = sqlc.arg(iteration),
    max_iterations = sqlc.arg(max_iterations),
    stagnant_iterations = sqlc.arg(stagnant_iterations),
    fingerprint = sqlc.arg(fingerprint),
    last_gap = sqlc.arg(last_gap),
    evidence = sqlc.arg(evidence),
    ci_gap = sqlc.arg(ci_gap),
    permissions_allowed = sqlc.arg(permissions_allowed),
    permissions_denied = sqlc.arg(permissions_denied),
    updated_at = sqlc.arg(updated_at),
    last_nudge_at = sqlc.arg(last_nudge_at),
    ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: CreateAutopilotEvent :one
INSERT INTO autopilot_events (
    id, run_id, session_id, kind, iteration, summary, detail, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAutopilotEvent :one
SELECT * FROM autopilot_events WHERE id = ?;

-- Newest first, capped: a long run logs an event per iteration.
-- name: ListAutopilotEventsForRun :many
SELECT *
FROM autopilot_events
WHERE run_id = sqlc.arg(run_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListOpenParkedAutopilotEvents :many
SELECT *
FROM autopilot_events
WHERE run_id = ?
  AND kind = 'parked'
  AND resolved_at IS NULL
ORDER BY created_at, id;

-- name: CountOpenParkedAutopilotEventsForSession :one
SELECT COUNT(*)
FROM autopilot_events
WHERE session_id = ?
  AND kind = 'parked'
  AND resolved_at IS NULL;

-- A parked item is resolved when the user marks it handled; a decision, when the
-- user asks the agent to undo it.
-- name: ResolveAutopilotEvent :one
UPDATE autopilot_events
SET resolved_at = ?
WHERE id = ?
  AND kind IN ('parked', 'decision')
  AND resolved_at IS NULL
RETURNING *;

-- The PRs that merged in a project while a run was going, for the return report.
-- name: ListMergedPRNotificationsForProjectSince :many
SELECT *
FROM notifications
WHERE project_id = ?
  AND type = 'pr_merged'
  AND created_at >= ?
ORDER BY created_at, id;

-- name: GetAutopilotMaxIterations :one
SELECT autopilot_max_iterations FROM app_settings WHERE id = 1;

-- name: SetAutopilotMaxIterations :exec
UPDATE app_settings SET autopilot_max_iterations = ?, updated_at = ? WHERE id = 1;
