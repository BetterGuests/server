-- +goose Up

ALTER TABLE app.listing_photos ADD COLUMN fingerprint bigint;

-- +goose Down

ALTER TABLE app.listing_photos DROP COLUMN IF EXISTS fingerprint;
