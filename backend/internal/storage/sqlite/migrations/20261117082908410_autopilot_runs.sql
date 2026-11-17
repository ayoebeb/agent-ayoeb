-- +goose Up

-- One Autopilot task run per session. A run is the user's task plus the loop AO
-- drives to finish it: AO re-sends the task at the end of every turn until the
-- agent declares it done and the verification gates pass.
--
-- At most one run per session is open (running, paused on the usage limit, or
-- parked waiting for the user); finished runs stay as history.
CREATE TABLE autopilot_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    task TEXT NOT NULL,
    -- The "done when ..." list the agent drafted from the task in its first turn.
    criteria TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('running', 'paused_usage_limit', 'parked', 'done', 'stopped')),
    -- Why the run left running: user, emergency_stop, session_ended, goal_done,
    -- max_iterations, stuck, needs_input. Empty while running.
    end_reason TEXT NOT NULL DEFAULT '',
    -- Completed turns the loop has judged so far.
    iteration INTEGER NOT NULL DEFAULT 0,
    max_iterations INTEGER NOT NULL CHECK (max_iterations >= 1),
    -- Consecutive iterations in which neither the workspace nor the recorded test
    -- result changed. Three park the run.
    stagnant_iterations INTEGER NOT NULL DEFAULT 0,
    fingerprint TEXT NOT NULL DEFAULT '',
    -- What was still failing at the last judgement, quoted back in the next prompt.
    last_gap TEXT NOT NULL DEFAULT '',
    -- The agent's evidence for its last done claim (tests it ran and their result).
    evidence TEXT NOT NULL DEFAULT '',
    -- Set when the agent declared that CI cannot start; a recorded gap, not a pass.
    ci_gap TEXT NOT NULL DEFAULT '',
    permissions_allowed INTEGER NOT NULL DEFAULT 0,
    permissions_denied INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    last_nudge_at TIMESTAMP,
    ended_at TIMESTAMP
);

CREATE UNIQUE INDEX idx_autopilot_runs_open_session
    ON autopilot_runs(session_id)
    WHERE state IN ('running', 'paused_usage_limit', 'parked');
CREATE INDEX idx_autopilot_runs_session
    ON autopilot_runs(session_id, started_at DESC, id DESC);
CREATE INDEX idx_autopilot_runs_open
    ON autopilot_runs(state)
    WHERE state IN ('running', 'paused_usage_limit', 'parked');

-- The log the user reads: iterations, decisions the agent took instead of
-- asking, items parked for the user, failed done claims, and how the run ended.
CREATE TABLE autopilot_events (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES autopilot_runs(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN (
        'started', 'iteration', 'decision', 'parked', 'gate_failed', 'done',
        'stopped', 'run_parked', 'paused', 'resumed', 'permission_denied',
        'wait_diverted'
    )),
    iteration INTEGER NOT NULL DEFAULT 0,
    summary TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    -- A parked item is open until the user marks it handled.
    resolved_at TIMESTAMP
);

CREATE INDEX idx_autopilot_events_run
    ON autopilot_events(run_id, created_at, id);
CREATE INDEX idx_autopilot_events_open_parked
    ON autopilot_events(run_id)
    WHERE kind = 'parked' AND resolved_at IS NULL;

-- Most iterations a run may take before it parks itself for the user. A run can
-- override it when it starts.
ALTER TABLE app_settings ADD COLUMN autopilot_max_iterations INTEGER NOT NULL DEFAULT 30
    CHECK (autopilot_max_iterations BETWEEN 1 AND 500);

-- SQLite cannot widen a CHECK constraint in place. Rebuild the notifications
-- table to add autopilot_parked: Autopilot parked one item (a hard limit it must
-- not cross, or a run it could not finish) and needs the user.
-- +goose StatementBegin
CREATE TABLE notifications_next (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN (
        'needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged',
        'review_completed', 'review_changes_requested', 'orchestrator_waiting',
        'autopilot_parked'
    )),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP,
    source_key TEXT NOT NULL DEFAULT '',
    reminded_at TIMESTAMP
);

INSERT INTO notifications_next (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at, source_key, reminded_at
)
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at, source_key, reminded_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_next RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);
CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);
CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);
CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC) WHERE dismissed_at IS NULL;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE source_key = '' AND (status = 'unread' OR resolved_at IS NULL);
CREATE UNIQUE INDEX idx_notifications_source_dedupe
    ON notifications(source_key) WHERE source_key <> '';
-- +goose StatementEnd

-- +goose Down
ALTER TABLE app_settings DROP COLUMN autopilot_max_iterations;

-- +goose StatementBegin
DELETE FROM notifications WHERE type = 'autopilot_parked';

CREATE TABLE notifications_previous (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN (
        'needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged',
        'review_completed', 'review_changes_requested', 'orchestrator_waiting'
    )),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP,
    source_key TEXT NOT NULL DEFAULT '',
    reminded_at TIMESTAMP
);

INSERT INTO notifications_previous (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at, source_key, reminded_at
)
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at, source_key, reminded_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_previous RENAME TO notifications;

CREATE INDEX idx_notifications_status_history
    ON notifications(status, created_at DESC, id DESC);
CREATE INDEX idx_notifications_history
    ON notifications(created_at DESC, id DESC);
CREATE INDEX idx_notifications_unresolved
    ON notifications(resolved_at, created_at DESC, id DESC);
CREATE INDEX idx_notifications_live_history
    ON notifications(created_at DESC, id DESC) WHERE dismissed_at IS NULL;
CREATE UNIQUE INDEX idx_notifications_open_dedupe
    ON notifications(session_id, type, pr_url)
    WHERE source_key = '' AND (status = 'unread' OR resolved_at IS NULL);
CREATE UNIQUE INDEX idx_notifications_source_dedupe
    ON notifications(source_key) WHERE source_key <> '';
-- +goose StatementEnd

DROP TABLE autopilot_events;
DROP TABLE autopilot_runs;
