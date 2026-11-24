-- +goose Up
-- A reviewer's Chat turn can stop on the provider's usage limit like a worker's.
-- Such a stop is recorded under the worker session the review belongs to, and
-- review_id names the reviewer it paused ('' for a stop of the session itself).
ALTER TABLE usage_limit_stops ADD COLUMN review_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE usage_limit_stops DROP COLUMN review_id;
