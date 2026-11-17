-- +goose Up
-- One row per turn that stopped on the provider's usage limit, written the first
-- time AO sees the stop. It makes the follow-ups happen once: telling the
-- orchestrator, and continuing the session after the window resets. The row
-- never decides a session's status; that is derived from the failed turn.
CREATE TABLE usage_limit_stops (
    turn_id TEXT PRIMARY KEY REFERENCES conversation_turns(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL DEFAULT '',
    -- The login the session ran on, and the limit window it hit: the account and
    -- the reset time to the minute. One window is one user notification.
    account_key TEXT NOT NULL DEFAULT '',
    window_key TEXT NOT NULL DEFAULT '',
    window_label TEXT NOT NULL DEFAULT '',
    resets_at TIMESTAMP,
    detected_at TIMESTAMP NOT NULL,
    reported_at TIMESTAMP,
    resume_state TEXT NOT NULL DEFAULT 'pending'
        CHECK (resume_state IN ('pending', 'resumed', 'skipped')),
    skip_reason TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    retry_at TIMESTAMP,
    resumed_at TIMESTAMP,
    -- The turn the "continue" message started, so the cap on concurrent resumes
    -- can count the ones still running.
    resume_turn_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_usage_limit_stops_due ON usage_limit_stops(resume_state, resets_at);
CREATE INDEX idx_usage_limit_stops_session ON usage_limit_stops(session_id, resumed_at);

-- Whether AO continues a limit-stopped session by itself once the window resets
-- (on by default), and how many of those continued turns may run at once.
ALTER TABLE app_settings ADD COLUMN usage_limit_auto_resume INTEGER NOT NULL DEFAULT 1
    CHECK (usage_limit_auto_resume IN (0, 1));
ALTER TABLE app_settings ADD COLUMN usage_limit_max_resumes INTEGER NOT NULL DEFAULT 3
    CHECK (usage_limit_max_resumes BETWEEN 1 AND 10);

-- +goose Down
ALTER TABLE app_settings DROP COLUMN usage_limit_max_resumes;
ALTER TABLE app_settings DROP COLUMN usage_limit_auto_resume;
DROP INDEX idx_usage_limit_stops_session;
DROP INDEX idx_usage_limit_stops_due;
DROP TABLE usage_limit_stops;
