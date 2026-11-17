-- +goose Up
-- Tool permissions a user chose "Always allow" for, remembered for every future
-- session of the project. A new Claude Code session workspace gets these written
-- into its .claude/settings.local.json; revoking one removes it again. The rule
-- is stored as the tool plus its argument so the settings-file spelling stays a
-- rendering concern.
CREATE TABLE project_permission_rules (
    id                TEXT PRIMARY KEY,
    project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool              TEXT NOT NULL,
    content           TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL CHECK (kind IN ('exact', 'command', 'tool', 'server')),
    label             TEXT NOT NULL,
    source_session_id TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL,
    UNIQUE (project_id, tool, content)
);

-- +goose Down
DROP TABLE project_permission_rules;
