-- +goose Up

ALTER TABLE app.payments
    ADD COLUMN guest_fee_cents integer NOT NULL DEFAULT 0
    CHECK (guest_fee_cents >= 0 AND guest_fee_cents < amount_cents);

DROP FUNCTION app.bookings_due_for_settlement(date, date, integer);

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
    stripe_transfer_id text,
    adjustment_cents integer,
    guest_fee_cents integer
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
           s.stripe_refund_id, s.stripe_transfer_id,
           COALESCE(c.refund_cents, 0) + COALESCE(r.refund_cents, 0),
           p.guest_fee_cents
    FROM app.bookings b
    JOIN app.payments p ON p.booking_id = b.id AND p.status = 'succeeded'
    LEFT JOIN app.booking_settlements s ON s.booking_id = b.id
    LEFT JOIN app.host_payout_accounts a ON a.account_id = p.host_id
    LEFT JOIN app.booking_cancellations c ON c.booking_id = b.id
    LEFT JOIN app.stay_reports r ON r.booking_id = b.id
    WHERE (
          b.status IN ('confirmed', 'completed')
          OR (b.status = 'cancelled' AND c.refund_cents < c.amount_cents)
      )
      AND (c.booking_id IS NULL OR c.refund_status <> 'pending')
      AND (r.booking_id IS NULL OR (r.status = 'resolved' AND r.refund_status <> 'pending'))
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
    stripe_transfer_id text,
    adjustment_cents integer
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
           s.stripe_refund_id, s.stripe_transfer_id,
           COALESCE(c.refund_cents, 0) + COALESCE(r.refund_cents, 0)
    FROM app.bookings b
    JOIN app.payments p ON p.booking_id = b.id AND p.status = 'succeeded'
    LEFT JOIN app.booking_settlements s ON s.booking_id = b.id
    LEFT JOIN app.host_payout_accounts a ON a.account_id = p.host_id
    LEFT JOIN app.booking_cancellations c ON c.booking_id = b.id
    LEFT JOIN app.stay_reports r ON r.booking_id = b.id
    WHERE (
          b.status IN ('confirmed', 'completed')
          OR (b.status = 'cancelled' AND c.refund_cents < c.amount_cents)
      )
      AND (c.booking_id IS NULL OR c.refund_status <> 'pending')
      AND (r.booking_id IS NULL OR (r.status = 'resolved' AND r.refund_status <> 'pending'))
      AND b.ends_on BETWEEN earliest_ends_on AND latest_ends_on
      AND (s.id IS NULL OR s.status IN ('processing', 'awaiting_host'))
    ORDER BY s.updated_at NULLS FIRST, b.ends_on
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.bookings_due_for_settlement(date, date, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.bookings_due_for_settlement(date, date, integer) TO __APP_ROLE__;

ALTER TABLE app.payments DROP COLUMN guest_fee_cents;
