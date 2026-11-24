-- +goose Up
-- AO stopped a session's agent on purpose (exit-agent, an update, an account
-- switch) and kept the session. The worker keeper reads it so it never resumes
-- such a stop as if it were a crash or a restart. One row per session: the
-- newest deliberate stop. stopped_at is the time the exit was recorded, so a
-- later exit (a crash after a manual resume) is told apart by being newer.
CREATE TABLE agent_stop_marks (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    stopped_at TIMESTAMP NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE agent_stop_marks;
