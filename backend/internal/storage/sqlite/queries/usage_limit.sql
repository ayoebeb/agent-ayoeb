-- Usage-limit stops: the turns that ended on the provider quota, and what AO
-- has done about each. See migration usage_limit_stops. The status a client
-- shows is derived from the failed turn; these rows only make the follow-ups
-- (tell the orchestrator, notify the user, continue the session) happen once.

-- The latest turn of each wanted session current conversation, for the sessions
-- where it failed. A cancelled turn is a withdrawn prompt and a rolled-back or
-- promoted one is hidden, so none of them count as the latest, the same rule
-- ListInterruptedSessions uses. A queued turn does not count either: it is
-- waiting behind the failed turn (a failed turn holds the queue), so the session
-- is still stopped on the limit, and the queued work is what a resume releases.
-- The caller parses the error text for the limit.
-- name: ListLatestFailedTurns :many
WITH wanted_session AS (
    SELECT CAST(j.value AS TEXT) AS session_id
    FROM json_each(?) AS j
),
session_conversation AS (
    SELECT wanted_session.session_id, (
        SELECT conversations.id FROM conversations
        WHERE conversations.current_session_id = wanted_session.session_id
        ORDER BY conversations.scope = 'project' DESC, conversations.created_at DESC, conversations.id DESC
        LIMIT 1
    ) AS conversation_id
    FROM wanted_session
),
latest_turn AS (
    SELECT session_conversation.session_id, (
        SELECT conversation_turns.id FROM conversation_turns
        WHERE conversation_turns.conversation_id = session_conversation.conversation_id
          AND conversation_turns.promoted_to_turn_id IS NULL
          AND conversation_turns.rolled_back_at IS NULL
          AND conversation_turns.state NOT IN ('cancelled', 'queued')
        ORDER BY conversation_turns.requested_at DESC, conversation_turns.rowid DESC
        LIMIT 1
    ) AS turn_id
    FROM session_conversation
)
SELECT CAST(latest_turn.session_id AS TEXT) AS session_id,
       turns.id AS turn_id,
       turns.error_message AS error_message,
       turns.completed_at AS completed_at,
       turns.requested_at AS requested_at,
       turns.usage_limit_resets_at AS usage_limit_resets_at,
       turns.usage_limit_auto_resume AS usage_limit_auto_resume,
       COALESCE(stops.resume_state, '') AS resume_state,
       COALESCE(stops.skip_reason, '') AS skip_reason
FROM latest_turn
JOIN conversation_turns AS turns ON turns.id = latest_turn.turn_id
LEFT JOIN usage_limit_stops AS stops ON stops.turn_id = turns.id
WHERE turns.state = 'failed';

-- Every failed turn that is still the latest of its session conversation and
-- has not been followed up (no stop row) and was completed since the cutoff, or
-- is still waiting to be resumed (whatever its age: a weekly limit resets in
-- days, and the watcher closes a stop only when its turn stopped being the
-- latest). A queued turn behind the failed one does not make it older (see
-- ListLatestFailedTurns). This is the watcher sweep.
-- name: ListLimitStopCandidates :many
SELECT CAST(conversations.current_session_id AS TEXT) AS session_id,
       turns.id AS turn_id,
       turns.error_message AS error_message,
       turns.completed_at AS completed_at,
       turns.requested_at AS requested_at,
       turns.usage_limit_resets_at AS usage_limit_resets_at,
       turns.usage_limit_auto_resume AS usage_limit_auto_resume,
       COALESCE(stops.resume_state, '') AS resume_state,
       COALESCE(stops.skip_reason, '') AS skip_reason,
       stops.resets_at AS stop_resets_at,
       stops.retry_at AS retry_at,
       COALESCE(stops.attempts, 0) AS attempts
FROM conversation_turns AS turns
JOIN conversations ON conversations.id = turns.conversation_id AND conversations.current_session_id IS NOT NULL
LEFT JOIN usage_limit_stops AS stops ON stops.turn_id = turns.id
WHERE turns.state = 'failed'
  AND turns.rolled_back_at IS NULL
  AND turns.promoted_to_turn_id IS NULL
  AND (stops.turn_id IS NULL OR stops.resume_state = 'pending')
  AND (stops.resume_state = 'pending' OR turns.completed_at >= sqlc.arg(completed_since))
  AND NOT EXISTS (
      SELECT 1 FROM conversation_turns AS newer
      WHERE newer.conversation_id = turns.conversation_id
        AND newer.promoted_to_turn_id IS NULL
        AND newer.rolled_back_at IS NULL
        AND newer.state NOT IN ('cancelled', 'queued')
        AND (newer.requested_at > turns.requested_at
             OR (newer.requested_at = turns.requested_at AND newer.rowid > turns.rowid))
  );

-- name: InsertUsageLimitStop :execrows
INSERT INTO usage_limit_stops (
    turn_id, session_id, project_id, account_key, window_key, window_label, resets_at, detected_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (turn_id) DO NOTHING;

-- name: ListPendingUsageLimitStops :many
SELECT * FROM usage_limit_stops
WHERE resume_state = 'pending'
ORDER BY resets_at IS NULL, resets_at, detected_at, turn_id;

-- Stops whose report to the orchestrator never went out (the send failed, or the
-- daemon stopped between recording the stop and sending it). A stop that was
-- recorded already skipped as too old or deliberately stopped is not news, and a
-- superseded one is stale, so none of those are listed.
-- name: ListUnreportedUsageLimitStops :many
SELECT * FROM usage_limit_stops
WHERE reported_at IS NULL
  AND skip_reason NOT IN ('stopped', 'expired', 'superseded')
ORDER BY detected_at, turn_id;

-- name: MarkUsageLimitStopReported :exec
UPDATE usage_limit_stops SET reported_at = ? WHERE turn_id = ? AND reported_at IS NULL;

-- name: SkipUsageLimitStop :execrows
UPDATE usage_limit_stops SET resume_state = 'skipped', skip_reason = ?
WHERE turn_id = ? AND resume_state = 'pending';

-- Switching the setting off parks the pending stops, and switching it on reopens
-- exactly those, so another reason for skipping one is never undone.
-- name: SkipPendingUsageLimitStopsDisabled :execrows
UPDATE usage_limit_stops SET resume_state = 'skipped', skip_reason = 'disabled'
WHERE resume_state = 'pending';

-- name: ReopenDisabledUsageLimitStops :execrows
UPDATE usage_limit_stops SET resume_state = 'pending', skip_reason = ''
WHERE resume_state = 'skipped' AND skip_reason = 'disabled';

-- name: DeferUsageLimitStop :exec
UPDATE usage_limit_stops SET attempts = attempts + 1, retry_at = ?
WHERE turn_id = ? AND resume_state = 'pending';

-- since is the moment before the continue message was sent, so the turn it
-- started is the newest one requested at or after it.
-- name: MarkUsageLimitStopResumed :exec
UPDATE usage_limit_stops
SET resume_state = 'resumed', resumed_at = sqlc.arg(resumed_at), attempts = attempts + 1,
    resume_turn_id = COALESCE((
        SELECT turns.id FROM conversation_turns AS turns
        JOIN conversations ON conversations.id = turns.conversation_id
        WHERE conversations.current_session_id = usage_limit_stops.session_id
          AND turns.requested_at >= sqlc.arg(since)
        ORDER BY turns.requested_at DESC, turns.rowid DESC
        LIMIT 1
    ), '')
WHERE turn_id = sqlc.arg(turn_id) AND resume_state = 'pending';

-- name: CountRunningUsageLimitResumes :one
SELECT COUNT(*) FROM usage_limit_stops AS stops
JOIN conversation_turns AS turns ON turns.id = stops.resume_turn_id
WHERE stops.resume_state = 'resumed' AND turns.state IN ('queued', 'running');

-- name: LastUsageLimitResumeAt :one
SELECT resumed_at FROM usage_limit_stops
WHERE resumed_at IS NOT NULL
ORDER BY resumed_at DESC
LIMIT 1;

-- A session that stopped on the limit again right after AO continued it.
-- name: CountRecentUsageLimitResumes :one
SELECT COUNT(*) FROM usage_limit_stops
WHERE session_id = ? AND resume_state = 'resumed' AND resumed_at >= ?;

-- name: SetUsageLimitAutoResume :exec
UPDATE app_settings SET usage_limit_auto_resume = ?, usage_limit_max_resumes = ?, updated_at = ? WHERE id = 1;
