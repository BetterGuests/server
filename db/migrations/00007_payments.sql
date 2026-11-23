-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN nightly_price_cents integer CHECK (nightly_price_cents IS NULL OR nightly_price_cents BETWEEN 100 AND 10000000);

CREATE TABLE app.payments (
    id                         uuid PRIMARY KEY,
    booking_id                 uuid NOT NULL UNIQUE REFERENCES app.bookings(id),
    guest_id                   uuid NOT NULL REFERENCES app.accounts(id),
    host_id                    uuid NOT NULL REFERENCES app.accounts(id),
    stripe_payment_intent_id   text NOT NULL UNIQUE CHECK (length(stripe_payment_intent_id) BETWEEN 1 AND 200),
    amount_cents               integer NOT NULL CHECK (amount_cents > 0),
    currency                   text NOT NULL CHECK (currency ~ '^[a-z]{3}$'),
    status                     text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed', 'canceled')),
    last_event_at              timestamptz,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payments_guest_id_idx ON app.payments (guest_id);
CREATE INDEX payments_host_id_idx ON app.payments (host_id);

ALTER TABLE app.payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.payments FORCE ROW LEVEL SECURITY;

CREATE POLICY payments_select ON app.payments
    FOR SELECT TO __APP_ROLE__
    USING (guest_id = app.current_user_id());

CREATE POLICY payments_insert ON app.payments
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (guest_id = app.current_user_id() AND status = 'pending');

CREATE POLICY payments_update ON app.payments
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id())
    WITH CHECK (guest_id = app.current_user_id());

GRANT SELECT, INSERT, UPDATE ON app.payments TO __APP_ROLE__;

CREATE POLICY bookings_confirm_paid ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (
        guest_id = app.current_user_id()
        AND status = 'pending'
        AND EXISTS (
            SELECT 1 FROM app.payments p
            WHERE p.booking_id = bookings.id AND p.status = 'succeeded'
        )
    )
    WITH CHECK (guest_id = app.current_user_id() AND status = 'confirmed');

-- +goose Down

DROP POLICY IF EXISTS bookings_confirm_paid ON app.bookings;
DROP TABLE IF EXISTS app.payments;
ALTER TABLE app.listings DROP COLUMN IF EXISTS nightly_price_cents;
