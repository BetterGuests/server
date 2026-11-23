-- +goose Up

ALTER TABLE app.bookings ADD COLUMN verification_id uuid REFERENCES app.identity_verifications(id);

CREATE INDEX bookings_verification_id_idx ON app.bookings (verification_id);

-- +goose Down

DROP INDEX IF EXISTS app.bookings_verification_id_idx;
ALTER TABLE app.bookings DROP COLUMN IF EXISTS verification_id;
