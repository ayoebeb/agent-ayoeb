-- +goose Up
-- One row per limit event (an account's limit window) that AO asks the owner
-- about: switch the stopped sessions to another account, or wait for the reset.
-- The sessions that stopped are the window's usage_limit_stops rows; this row
-- only holds the question and the answer, so one event is one prompt however
-- many sessions stopped.
CREATE TABLE usage_limit_prompts (
    id TEXT PRIMARY KEY,
    window_key TEXT NOT NULL UNIQUE,
    -- The account that hit the limit, its provider and the limit's own name.
    account_key TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    window_label TEXT NOT NULL DEFAULT '',
    resets_at TIMESTAMP,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'switched', 'waiting')),
    target_account_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    decided_at TIMESTAMP
);

CREATE INDEX idx_usage_limit_stops_window ON usage_limit_stops(window_key);

-- +goose Down
DROP INDEX idx_usage_limit_stops_window;
DROP TABLE usage_limit_prompts;
