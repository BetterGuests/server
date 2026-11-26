-- +goose Up

ALTER TABLE app.listing_photos
    ADD COLUMN screening text NOT NULL DEFAULT 'passed' CHECK (screening IN ('pending', 'passed', 'refused')),
    ADD COLUMN refusal text CHECK (refusal IS NULL OR length(refusal) <= 200);

ALTER TABLE app.listing_photos ALTER COLUMN screening SET DEFAULT 'pending';

-- +goose Down

ALTER TABLE app.listing_photos DROP COLUMN IF EXISTS refusal;
ALTER TABLE app.listing_photos DROP COLUMN IF EXISTS screening;
