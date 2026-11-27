-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.bookings_due_for_settlement(
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
           CASE WHEN app.is_banned(p.host_id) THEN '' ELSE COALESCE(a.stripe_account_id, '') END,
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
      AND NOT app.listing_taken_down(b.listing_id)
      AND (c.booking_id IS NULL OR c.refund_status <> 'pending')
      AND (r.booking_id IS NULL OR (r.status = 'resolved' AND r.refund_status <> 'pending'))
      AND b.ends_on BETWEEN earliest_ends_on AND latest_ends_on
      AND (s.id IS NULL OR s.status IN ('processing', 'awaiting_host'))
    ORDER BY s.updated_at NULLS FIRST, b.ends_on
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

CREATE POLICY listings_migrator_settlement_select ON app.listings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_settlements_team_retry ON app.booking_settlements
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (status = 'blocked')
    WITH CHECK (status = 'processing');

-- +goose StatementBegin
CREATE FUNCTION app.blocked_settlements(batch_size integer)
RETURNS TABLE (
    booking_id uuid,
    listing_title text,
    host_name text,
    ends_on date,
    payout_cents integer,
    cashback_cents integer,
    currency text,
    reason text,
    blocked_at timestamptz,
    can_retry boolean
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT s.booking_id, l.title, COALESCE(l.host_name, ''), b.ends_on, s.payout_cents, s.cashback_cents,
           p.currency, COALESCE(s.reason, ''), s.updated_at,
           app.current_user_id() NOT IN (s.host_id, s.guest_id)
    FROM app.booking_settlements s
    JOIN app.bookings b ON b.id = s.booking_id
    JOIN app.listings l ON l.id = b.listing_id
    JOIN app.payments p ON p.booking_id = s.booking_id AND p.status = 'succeeded'
    WHERE s.status = 'blocked' AND app.is_admin()
    ORDER BY s.updated_at, s.booking_id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.blocked_settlements(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.blocked_settlements(integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.retry_blocked_settlement(target uuid)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH released AS (
        UPDATE app.booking_settlements
        SET status = 'processing', reason = NULL, updated_at = now()
        WHERE booking_id = target
          AND status = 'blocked'
          AND app.is_admin()
          AND app.current_user_id() NOT IN (host_id, guest_id)
        RETURNING id
    )
    SELECT EXISTS (SELECT 1 FROM released)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.retry_blocked_settlement(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.retry_blocked_settlement(uuid) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.retry_blocked_settlement(uuid);
DROP FUNCTION app.blocked_settlements(integer);
DROP POLICY booking_settlements_team_retry ON app.booking_settlements;
DROP POLICY listings_migrator_settlement_select ON app.listings;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.bookings_due_for_settlement(
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
           CASE WHEN app.is_banned(p.host_id) THEN '' ELSE COALESCE(a.stripe_account_id, '') END,
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
