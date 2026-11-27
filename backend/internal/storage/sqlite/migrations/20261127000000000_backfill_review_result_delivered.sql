-- +goose Up
-- AO now sends every review result to its worker and stamps delivered_at.
-- Results recorded before that must not be sent again after the upgrade.
UPDATE review_run SET delivered_at = created_at WHERE status = 'complete' AND delivered_at IS NULL;

-- +goose Down
SELECT 1;
