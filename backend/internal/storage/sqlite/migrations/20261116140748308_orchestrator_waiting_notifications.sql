-- +goose Up
-- +goose StatementBegin
-- SQLite cannot widen a CHECK constraint in place. Rebuild the table to add the
-- orchestrator_waiting kind (a project orchestrator waiting on the user) and
-- reminded_at, which records the single re-notification of a still-open row so
-- it survives a daemon restart.
CREATE TABLE notifications_next (
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

INSERT INTO notifications_next (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at, source_key
)
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at, source_key
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

-- Whether AO tells the user when a project's orchestrator is waiting for them
-- (a distinct, hard-to-miss notification). On by default: the orchestrator is the
-- user's main contact, and a missed question stalls all the work behind it.
ALTER TABLE app_settings ADD COLUMN orchestrator_waiting_enabled INTEGER NOT NULL DEFAULT 1
    CHECK (orchestrator_waiting_enabled IN (0, 1));

-- Minutes before a still-unanswered orchestrator question is announced a second
-- time. 0 turns the reminder off. The default is 10.
ALTER TABLE app_settings ADD COLUMN orchestrator_waiting_renotify_minutes INTEGER NOT NULL DEFAULT 10
    CHECK (orchestrator_waiting_renotify_minutes >= 0);

-- +goose Down
ALTER TABLE app_settings DROP COLUMN orchestrator_waiting_renotify_minutes;
ALTER TABLE app_settings DROP COLUMN orchestrator_waiting_enabled;

-- +goose StatementBegin
DELETE FROM notifications WHERE type = 'orchestrator_waiting';

CREATE TABLE notifications_previous (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    pr_url TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL CHECK (type IN (
        'needs_input', 'ready_to_merge', 'pr_merged', 'pr_closed_unmerged',
        'review_completed', 'review_changes_requested'
    )),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'unread' CHECK (status IN ('read', 'unread')),
    created_at TIMESTAMP NOT NULL,
    resolved_at TIMESTAMP,
    dismissed_at TIMESTAMP,
    source_key TEXT NOT NULL DEFAULT ''
);

INSERT INTO notifications_previous (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at, source_key
)
SELECT id, session_id, project_id, pr_url, type, title, body, status,
       created_at, resolved_at, dismissed_at, source_key
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
