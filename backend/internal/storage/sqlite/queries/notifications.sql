-- name: CreateNotification :one
INSERT INTO notifications (
    id, session_id, project_id, pr_url, type, title, body, status, created_at, source_key
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetNotificationBySourceKey :one
SELECT * FROM notifications WHERE source_key = ? LIMIT 1;

-- name: ListUnreadNotificationsPage :many
SELECT *
FROM notifications
WHERE status = 'unread'
  AND dismissed_at IS NULL
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- Unresolved is the still-actionable set: the underlying issue has not gone
-- away yet. Terminal facts (pr_merged, pr_closed_unmerged) describe something
-- that already happened, so they are unseen-only and never listed here.
-- name: ListUnresolvedNotificationsPage :many
SELECT *
FROM notifications
WHERE resolved_at IS NULL
  AND dismissed_at IS NULL
  AND type IN ('needs_input', 'ready_to_merge', 'orchestrator_waiting')
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListNotificationsPage :many
SELECT *
FROM notifications
WHERE dismissed_at IS NULL
  AND (
    CAST(sqlc.arg(before_id) AS TEXT) = ''
    OR created_at < sqlc.arg(before_created_at)
    OR (created_at = sqlc.arg(before_created_at) AND id < CAST(sqlc.arg(before_id) AS TEXT))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: CountUnreadNotifications :one
SELECT COUNT(*)
FROM notifications
WHERE status = 'unread'
  AND dismissed_at IS NULL;

-- name: CountUnresolvedNotifications :one
SELECT COUNT(*)
FROM notifications
WHERE resolved_at IS NULL
  AND dismissed_at IS NULL
  AND type IN ('needs_input', 'ready_to_merge', 'orchestrator_waiting');

-- name: MarkNotificationRead :one
UPDATE notifications
SET status = 'read'
WHERE id = ?
  AND status = 'unread'
  AND dismissed_at IS NULL
RETURNING *;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications
SET status = 'read'
WHERE status = 'unread'
  AND dismissed_at IS NULL;

-- name: ResolveSessionNotificationsByType :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE session_id = sqlc.arg(session_id)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
RETURNING *;

-- Dismissed rows are dedupe tombstones, not visible history. Once the issue
-- resolves they have no remaining purpose and must not emit resolved events.
-- name: DeleteDismissedSessionNotificationsByType :execrows
DELETE FROM notifications
WHERE session_id = sqlc.arg(session_id)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL;

-- name: ResolvePRNotificationsByType :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE pr_url = sqlc.arg(pr_url)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
RETURNING *;

-- name: DeleteDismissedPRNotificationsByType :execrows
DELETE FROM notifications
WHERE pr_url = sqlc.arg(pr_url)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL;

-- Readiness is more than open/closed: draft, CI, review decision, unresolved
-- human comments, and mergeability all block a merge. Rather than restate that
-- rule in SQL and let it drift from the live path, this returns the open rows
-- and lets domain.MergeReadiness judge them against the stored PR facts.
-- name: ListOpenReadyToMergeNotifications :many
SELECT *
FROM notifications
WHERE type = 'ready_to_merge'
  AND resolved_at IS NULL;

-- Restart reconciliation: a resolution transition observed while the daemon was
-- down never reaches lifecycle, so open rows are re-checked against the durable
-- session/PR facts on startup.
-- name: ResolveStaleNeedsInputNotifications :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE type = 'needs_input'
  AND resolved_at IS NULL
  AND dismissed_at IS NULL
  AND session_id IN (
    SELECT id FROM sessions
    WHERE is_terminated = TRUE
       OR activity_state NOT IN ('waiting_input', 'blocked')
  )
RETURNING *;

-- name: DeleteStaleDismissedNeedsInputNotifications :execrows
DELETE FROM notifications
WHERE type = 'needs_input'
  AND resolved_at IS NULL
  AND dismissed_at IS NOT NULL
  AND session_id IN (
    SELECT id FROM sessions
    WHERE is_terminated = TRUE
       OR activity_state NOT IN ('waiting_input', 'blocked')
  );

-- Every orchestrator still waiting on the user. Dismissed rows are included on
-- purpose: clearing the notification panel acknowledges the alert, not the
-- question, so the sidebar marker must outlive it.
-- name: ListOpenOrchestratorWaiting :many
SELECT *
FROM notifications
WHERE type = 'orchestrator_waiting'
  AND resolved_at IS NULL
ORDER BY created_at DESC, id DESC;

-- Open rows the single reminder is due for: not yet reminded, and waiting since
-- before the cutoff.
-- name: ListDueOrchestratorWaitingReminders :many
SELECT *
FROM notifications
WHERE type = 'orchestrator_waiting'
  AND resolved_at IS NULL
  AND reminded_at IS NULL
  AND created_at <= sqlc.arg(cutoff)
ORDER BY created_at ASC, id ASC;

-- Claims the reminder: only the call that flips reminded_at from NULL wins, so a
-- row is announced at most once even if two ticks race.
-- name: MarkNotificationReminded :one
UPDATE notifications
SET reminded_at = sqlc.arg(reminded_at)
WHERE id = sqlc.arg(id)
  AND type = 'orchestrator_waiting'
  AND resolved_at IS NULL
  AND reminded_at IS NULL
RETURNING *;

-- An orchestrator's wait outlives the user dismissing its notification, so
-- resolving it must reach dismissed rows too and report them: the sidebar marker
-- comes from the open waits, not from the visible history, and learns the wait
-- ended from the resolved event.
-- name: ResolveOpenSessionNotificationsByTypeIncludingDismissed :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE session_id = sqlc.arg(session_id)
  AND type = sqlc.arg(type)
  AND resolved_at IS NULL
RETURNING *;

-- Restart reconciliation for orchestrator waits: an orchestrator that ended
-- while the daemon was down never reached lifecycle.
-- name: ResolveStaleOrchestratorWaitingNotifications :many
UPDATE notifications
SET resolved_at = sqlc.arg(resolved_at)
WHERE type = 'orchestrator_waiting'
  AND resolved_at IS NULL
  AND session_id IN (SELECT id FROM sessions WHERE is_terminated = TRUE)
RETURNING *;

-- name: GetOpenNotificationByDedupe :one
SELECT *
FROM notifications
WHERE session_id = ?
  AND type = ?
  AND pr_url = ?
  AND (status = 'unread' OR resolved_at IS NULL)
LIMIT 1;

-- name: ClearAllNotifications :execrows
UPDATE notifications
SET dismissed_at = CURRENT_TIMESTAMP,
    status = 'read'
WHERE dismissed_at IS NULL;

-- Resolved notifications no longer carry a live dedupe fact. Remove them in
-- the same transaction that dismisses visible history so only open tombstones
-- remain.
-- name: DeleteResolvedDismissedNotifications :execrows
DELETE FROM notifications
WHERE dismissed_at IS NOT NULL
  AND resolved_at IS NOT NULL;

-- Read before dismissal so the delete response and live event keep the row's
-- original unread state. Clients use that state to decrement their badge.
-- name: GetNotificationForDismissal :one
SELECT *
FROM notifications
WHERE id = ?
  AND dismissed_at IS NULL;

-- name: DismissNotification :execrows
UPDATE notifications
SET dismissed_at = CURRENT_TIMESTAMP,
    status = 'read'
WHERE id = ?
  AND dismissed_at IS NULL;
