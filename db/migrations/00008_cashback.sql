-- +goose Up

CREATE TABLE app.booking_task_submissions (
    id           uuid PRIMARY KEY,
    booking_id   uuid NOT NULL REFERENCES app.bookings(id),
    guest_id     uuid NOT NULL REFERENCES app.accounts(id),
    task_key     text NOT NULL CHECK (task_key ~ '^[a-z_]{3,40}$'),
    photo_url    text NOT NULL CHECK (length(photo_url) BETWEEN 10 AND 2000),
    status       text NOT NULL CHECK (status IN ('checking', 'approved', 'retake')),
    note         text CHECK (note IS NULL OR length(note) <= 500),
    reviewed_at  timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    UNIQUE (booking_id, task_key)
);

CREATE INDEX booking_task_submissions_guest_id_idx ON app.booking_task_submissions (guest_id);

CREATE TABLE app.booking_bundle_refunds (
    id                uuid PRIMARY KEY,
    booking_id        uuid NOT NULL REFERENCES app.bookings(id),
    guest_id          uuid NOT NULL REFERENCES app.accounts(id),
    bundle_key        text NOT NULL CHECK (bundle_key ~ '^[a-z_]{3,40}$'),
    amount_cents      integer NOT NULL CHECK (amount_cents > 0),
    status            text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    stripe_refund_id  text UNIQUE CHECK (stripe_refund_id IS NULL OR length(stripe_refund_id) BETWEEN 1 AND 200),
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    UNIQUE (booking_id, bundle_key)
);

CREATE INDEX booking_bundle_refunds_guest_id_idx ON app.booking_bundle_refunds (guest_id);

ALTER TABLE app.booking_task_submissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_task_submissions FORCE ROW LEVEL SECURITY;
ALTER TABLE app.booking_bundle_refunds ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_bundle_refunds FORCE ROW LEVEL SECURITY;

CREATE POLICY booking_task_submissions_select ON app.booking_task_submissions
    FOR SELECT TO __APP_ROLE__
    USING (
        guest_id = app.current_user_id()
        OR EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY booking_task_submissions_insert ON app.booking_task_submissions
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (guest_id = app.current_user_id() AND status = 'checking');

CREATE POLICY booking_task_submissions_update_guest ON app.booking_task_submissions
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id() AND status = 'retake')
    WITH CHECK (guest_id = app.current_user_id() AND status = 'checking');

CREATE POLICY booking_task_submissions_update_host ON app.booking_task_submissions
    FOR UPDATE TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY booking_bundle_refunds_select ON app.booking_bundle_refunds
    FOR SELECT TO __APP_ROLE__
    USING (
        guest_id = app.current_user_id()
        OR EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY booking_bundle_refunds_insert ON app.booking_bundle_refunds
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        status = 'pending'
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY booking_bundle_refunds_update ON app.booking_bundle_refunds
    FOR UPDATE TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY payments_select_host ON app.payments
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());

GRANT SELECT, INSERT, UPDATE ON app.booking_task_submissions TO __APP_ROLE__;
GRANT SELECT, INSERT, UPDATE ON app.booking_bundle_refunds TO __APP_ROLE__;

-- +goose Down

DROP POLICY IF EXISTS payments_select_host ON app.payments;
DROP TABLE IF EXISTS app.booking_bundle_refunds;
DROP TABLE IF EXISTS app.booking_task_submissions;
