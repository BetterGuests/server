-- +goose Up

ALTER TABLE app.bookings DROP CONSTRAINT bookings_status_check;
ALTER TABLE app.bookings ADD CONSTRAINT bookings_status_check
    CHECK (status IN ('requested', 'declined', 'pending', 'confirmed', 'cancelled', 'completed'));

ALTER TABLE app.bookings ADD COLUMN decided_at timestamptz;

ALTER TABLE app.bookings DROP CONSTRAINT bookings_no_overlap;
ALTER TABLE app.bookings
    ADD CONSTRAINT bookings_no_overlap
    EXCLUDE USING gist (
        listing_id WITH =,
        daterange(starts_on, ends_on, '[)') WITH &&
    )
    WHERE (status IN ('requested', 'pending', 'confirmed'));

DROP POLICY bookings_insert ON app.bookings;
CREATE POLICY bookings_insert ON app.bookings
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND status = 'requested'
        AND completed_at IS NULL
        AND decided_at IS NULL
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id
              AND l.status = 'published'
              AND l.host_id <> app.current_user_id()
        )
    );

CREATE POLICY bookings_cancel_guest ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id() AND status IN ('requested', 'pending'))
    WITH CHECK (guest_id = app.current_user_id() AND status = 'cancelled');

CREATE INDEX bookings_status_idx ON app.bookings (status);

-- +goose Down

DROP INDEX IF EXISTS app.bookings_status_idx;
DROP POLICY IF EXISTS bookings_cancel_guest ON app.bookings;

DROP POLICY bookings_insert ON app.bookings;
CREATE POLICY bookings_insert ON app.bookings
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND status = 'pending'
        AND completed_at IS NULL
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.status = 'published'
        )
    );

UPDATE app.bookings SET status = 'pending' WHERE status = 'requested';
UPDATE app.bookings SET status = 'cancelled' WHERE status = 'declined';

ALTER TABLE app.bookings DROP CONSTRAINT bookings_no_overlap;
ALTER TABLE app.bookings
    ADD CONSTRAINT bookings_no_overlap
    EXCLUDE USING gist (
        listing_id WITH =,
        daterange(starts_on, ends_on, '[)') WITH &&
    )
    WHERE (status IN ('pending', 'confirmed'));

ALTER TABLE app.bookings DROP COLUMN decided_at;

ALTER TABLE app.bookings DROP CONSTRAINT bookings_status_check;
ALTER TABLE app.bookings ADD CONSTRAINT bookings_status_check
    CHECK (status IN ('pending', 'confirmed', 'cancelled', 'completed'));
