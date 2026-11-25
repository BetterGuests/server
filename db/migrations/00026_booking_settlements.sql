-- +goose Up

ALTER TABLE app.booking_task_submissions DROP CONSTRAINT booking_task_submissions_status_check;
ALTER TABLE app.booking_task_submissions ADD CONSTRAINT booking_task_submissions_status_check
    CHECK (status IN ('checking', 'approved', 'retake', 'rejected'));

CREATE TABLE app.host_payout_accounts (
    account_id         uuid PRIMARY KEY REFERENCES app.accounts(id),
    stripe_account_id  text NOT NULL UNIQUE CHECK (length(stripe_account_id) BETWEEN 1 AND 200),
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app.booking_settlements (
    id                  uuid PRIMARY KEY,
    booking_id          uuid NOT NULL UNIQUE REFERENCES app.bookings(id),
    guest_id            uuid NOT NULL REFERENCES app.accounts(id),
    host_id             uuid NOT NULL REFERENCES app.accounts(id),
    fee_cents           integer NOT NULL CHECK (fee_cents >= 0),
    cashback_cents      integer NOT NULL CHECK (cashback_cents >= 0),
    refund_due_cents    integer NOT NULL CHECK (refund_due_cents >= 0 AND refund_due_cents <= cashback_cents),
    payout_cents        integer NOT NULL CHECK (payout_cents >= 0),
    stripe_refund_id    text UNIQUE CHECK (stripe_refund_id IS NULL OR length(stripe_refund_id) BETWEEN 1 AND 200),
    stripe_transfer_id  text UNIQUE CHECK (stripe_transfer_id IS NULL OR length(stripe_transfer_id) BETWEEN 1 AND 200),
    status              text NOT NULL CHECK (status IN ('processing', 'awaiting_host', 'blocked', 'settled')),
    reason              text CHECK (reason IS NULL OR length(reason) <= 500),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX booking_settlements_guest_id_idx ON app.booking_settlements (guest_id);
CREATE INDEX booking_settlements_host_id_idx ON app.booking_settlements (host_id);
CREATE INDEX bookings_ends_on_idx ON app.bookings (ends_on);

ALTER TABLE app.host_payout_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.host_payout_accounts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.booking_settlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_settlements FORCE ROW LEVEL SECURITY;

CREATE POLICY host_payout_accounts_select ON app.host_payout_accounts
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY host_payout_accounts_insert ON app.host_payout_accounts
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY booking_settlements_select ON app.booking_settlements
    FOR SELECT TO __APP_ROLE__
    USING (guest_id = app.current_user_id() OR host_id = app.current_user_id());

CREATE POLICY booking_settlements_insert ON app.booking_settlements
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.payments p
            WHERE p.booking_id = booking_settlements.booking_id
              AND p.host_id = app.current_user_id()
              AND p.guest_id = booking_settlements.guest_id
              AND p.status = 'succeeded'
        )
    );

CREATE POLICY booking_settlements_update ON app.booking_settlements
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id() AND status <> 'settled')
    WITH CHECK (host_id = app.current_user_id());

GRANT SELECT, INSERT ON app.host_payout_accounts TO __APP_ROLE__;
GRANT SELECT, INSERT, UPDATE ON app.booking_settlements TO __APP_ROLE__;

CREATE POLICY bookings_settlement_select ON app.bookings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY payments_settlement_select ON app.payments
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_task_submissions_settlement_select ON app.booking_task_submissions
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_bundle_refunds_settlement_select ON app.booking_bundle_refunds
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_settlements_settlement_select ON app.booking_settlements
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY host_payout_accounts_settlement_select ON app.host_payout_accounts
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.bookings_due_for_settlement(
    earliest_ends_on date,
    latest_ends_on date,
    batch_size integer
)
RETURNS TABLE (
    booking_id uuid,
    guest_id uuid,
    host_id uuid,
    payment_intent_id text,
    amount_cents integer,
    currency text,
    accepted_task_keys text[],
    earlier_refund_cents integer,
    host_stripe_account_id text,
    settlement_id uuid,
    fee_cents integer,
    cashback_cents integer,
    refund_due_cents integer,
    payout_cents integer,
    stripe_refund_id text,
    stripe_transfer_id text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, p.guest_id, p.host_id, p.stripe_payment_intent_id, p.amount_cents, p.currency,
           ARRAY(
               SELECT t.task_key FROM app.booking_task_submissions t
               WHERE t.booking_id = b.id AND t.status IN ('checking', 'approved')
           ),
           COALESCE((
               SELECT sum(r.amount_cents) FROM app.booking_bundle_refunds r
               WHERE r.booking_id = b.id AND r.status = 'succeeded'
           ), 0)::integer,
           COALESCE(a.stripe_account_id, ''),
           s.id, s.fee_cents, s.cashback_cents, s.refund_due_cents, s.payout_cents,
           s.stripe_refund_id, s.stripe_transfer_id
    FROM app.bookings b
    JOIN app.payments p ON p.booking_id = b.id AND p.status = 'succeeded'
    LEFT JOIN app.booking_settlements s ON s.booking_id = b.id
    LEFT JOIN app.host_payout_accounts a ON a.account_id = p.host_id
    WHERE b.status IN ('confirmed', 'completed')
      AND b.ends_on BETWEEN earliest_ends_on AND latest_ends_on
      AND (s.id IS NULL OR s.status IN ('processing', 'awaiting_host'))
    ORDER BY s.updated_at NULLS FIRST, b.ends_on
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.bookings_due_for_settlement(date, date, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.bookings_due_for_settlement(date, date, integer) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.bookings_due_for_settlement(date, date, integer);
DROP POLICY host_payout_accounts_settlement_select ON app.host_payout_accounts;
DROP POLICY booking_settlements_settlement_select ON app.booking_settlements;
DROP POLICY booking_bundle_refunds_settlement_select ON app.booking_bundle_refunds;
DROP POLICY booking_task_submissions_settlement_select ON app.booking_task_submissions;
DROP POLICY payments_settlement_select ON app.payments;
DROP POLICY bookings_settlement_select ON app.bookings;
DROP INDEX app.bookings_ends_on_idx;
DROP TABLE app.booking_settlements;
DROP TABLE app.host_payout_accounts;
UPDATE app.booking_task_submissions SET status = 'retake' WHERE status = 'rejected';
ALTER TABLE app.booking_task_submissions DROP CONSTRAINT booking_task_submissions_status_check;
ALTER TABLE app.booking_task_submissions ADD CONSTRAINT booking_task_submissions_status_check
    CHECK (status IN ('checking', 'approved', 'retake'));
