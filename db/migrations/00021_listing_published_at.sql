-- +goose Up

ALTER TABLE app.listings ADD COLUMN published_at timestamptz;

ALTER TABLE app.listings NO FORCE ROW LEVEL SECURITY;

UPDATE app.listings
SET published_at = created_at
WHERE status IN ('published', 'paused');

ALTER TABLE app.listings FORCE ROW LEVEL SECURITY;

-- +goose Down

ALTER TABLE app.listings DROP COLUMN published_at;
