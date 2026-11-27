-- +goose Up

CREATE TABLE app.account_bans (
    id           uuid PRIMARY KEY,
    account_id   uuid NOT NULL REFERENCES app.accounts(id),
    account_name text NOT NULL CHECK (length(account_name) BETWEEN 1 AND 200),
    reason       text NOT NULL CHECK (length(reason) BETWEEN 1 AND 500),
    banned_by    uuid NOT NULL REFERENCES app.accounts(id),
    banned_at    timestamptz NOT NULL DEFAULT now(),
    lifted_by    uuid REFERENCES app.accounts(id),
    lifted_at    timestamptz,
    CONSTRAINT account_bans_not_self CHECK (account_id <> banned_by),
    CONSTRAINT account_bans_lift_shape CHECK ((lifted_at IS NULL) = (lifted_by IS NULL))
);

CREATE INDEX account_bans_account_id_idx ON app.account_bans (account_id);
CREATE INDEX account_bans_banned_by_idx ON app.account_bans (banned_by);
CREATE INDEX account_bans_lifted_by_idx ON app.account_bans (lifted_by);
CREATE UNIQUE INDEX account_bans_one_active_idx ON app.account_bans (account_id) WHERE lifted_at IS NULL;

ALTER TABLE app.account_bans ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_bans FORCE ROW LEVEL SECURITY;

CREATE POLICY account_bans_migrator_select ON app.account_bans
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.is_banned(target_account uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (
        SELECT 1 FROM app.account_bans b WHERE b.account_id = target_account AND b.lifted_at IS NULL
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.is_admin_account(target_account uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (SELECT 1 FROM app.admin_accounts a WHERE a.account_id = target_account)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.is_banned(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.is_admin_account(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.is_banned(uuid) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.is_admin_account(uuid) TO __APP_ROLE__;

CREATE POLICY account_bans_select ON app.account_bans
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id() OR app.is_admin());

CREATE POLICY account_bans_insert ON app.account_bans
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        app.is_admin()
        AND banned_by = app.current_user_id()
        AND lifted_at IS NULL
        AND NOT app.is_admin_account(account_id)
    );

CREATE POLICY account_bans_lift ON app.account_bans
    FOR UPDATE TO __APP_ROLE__
    USING (app.is_admin() AND lifted_at IS NULL)
    WITH CHECK (app.is_admin() AND lifted_by = app.current_user_id());

GRANT SELECT, INSERT ON app.account_bans TO __APP_ROLE__;
GRANT UPDATE (lifted_by, lifted_at) ON app.account_bans TO __APP_ROLE__;

CREATE POLICY account_names_team_select ON app.account_names
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

ALTER TABLE app.listing_suspensions
    DROP CONSTRAINT listing_suspensions_paused_by_check,
    ADD CONSTRAINT listing_suspensions_paused_by_check
        CHECK (paused_by IN ('host_cancellations', 'team', 'verification', 'ban'));

-- +goose StatementBegin
CREATE FUNCTION app.keep_banned_listings_paused()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF OLD.paused_by = 'ban' AND OLD.lifted_at IS NULL AND NEW.lifted_at IS NOT NULL AND app.is_banned(OLD.host_id) THEN
        RAISE EXCEPTION 'the host is banned' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER listing_suspensions_keep_banned_paused
    BEFORE UPDATE ON app.listing_suspensions
    FOR EACH ROW
    EXECUTE FUNCTION app.keep_banned_listings_paused();

-- +goose StatementBegin
CREATE FUNCTION app.refuse_banned_booking()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NOT (OLD.status = 'requested' AND NEW.status = 'pending') THEN
            RETURN NEW;
        END IF;
    END IF;
    IF app.is_banned(NEW.guest_id)
       OR EXISTS (SELECT 1 FROM app.listings l WHERE l.id = NEW.listing_id AND app.is_banned(l.host_id)) THEN
        RAISE EXCEPTION 'account is banned' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.refuse_banned_booking() FROM PUBLIC;

CREATE TRIGGER bookings_refuse_banned
    BEFORE INSERT OR UPDATE OF status ON app.bookings
    FOR EACH ROW
    EXECUTE FUNCTION app.refuse_banned_booking();

ALTER TABLE app.booking_cancellations
    DROP CONSTRAINT booking_cancellations_cancelled_by_check,
    ADD CONSTRAINT booking_cancellations_cancelled_by_check
        CHECK (cancelled_by IN ('guest', 'host', 'expired', 'team'));

CREATE POLICY booking_cancellations_team_insert ON app.booking_cancellations
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        app.is_admin()
        AND cancelled_by = 'team'
        AND refund_cents = amount_cents
        AND (app.is_banned(guest_id) OR app.is_banned(host_id))
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_cancellations.booking_id
              AND b.guest_id = booking_cancellations.guest_id
              AND b.listing_id = booking_cancellations.listing_id
              AND l.host_id = booking_cancellations.host_id
        )
    );

CREATE POLICY bookings_cancel_by_team ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (
        app.is_admin()
        AND status IN ('requested', 'pending', 'confirmed')
        AND EXISTS (
            SELECT 1 FROM app.booking_cancellations c
            WHERE c.booking_id = bookings.id AND c.cancelled_by = 'team'
        )
    )
    WITH CHECK (
        app.is_admin()
        AND status = 'cancelled'
        AND EXISTS (
            SELECT 1 FROM app.booking_cancellations c
            WHERE c.booking_id = bookings.id AND c.cancelled_by = 'team'
        )
    );

-- +goose StatementBegin
CREATE FUNCTION app.skip_payout_setup_alerts_while_banned()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF app.is_banned(NEW.account_id) THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.skip_payout_setup_alerts_while_banned() FROM PUBLIC;

CREATE TRIGGER notifications_skip_payout_setup_while_banned
    BEFORE INSERT ON app.notifications
    FOR EACH ROW
    WHEN (NEW.kind = 'payout_setup_needed')
    EXECUTE FUNCTION app.skip_payout_setup_alerts_while_banned();

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

-- +goose Down

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

DROP TRIGGER notifications_skip_payout_setup_while_banned ON app.notifications;
DROP FUNCTION app.skip_payout_setup_alerts_while_banned();
DROP POLICY bookings_cancel_by_team ON app.bookings;
DROP POLICY booking_cancellations_team_insert ON app.booking_cancellations;
ALTER TABLE app.booking_cancellations
    DROP CONSTRAINT booking_cancellations_cancelled_by_check,
    ADD CONSTRAINT booking_cancellations_cancelled_by_check
        CHECK (cancelled_by IN ('guest', 'host', 'expired')) NOT VALID;
DROP TRIGGER bookings_refuse_banned ON app.bookings;
DROP FUNCTION app.refuse_banned_booking();
DROP TRIGGER listing_suspensions_keep_banned_paused ON app.listing_suspensions;
DROP FUNCTION app.keep_banned_listings_paused();
ALTER TABLE app.listing_suspensions
    DROP CONSTRAINT listing_suspensions_paused_by_check,
    ADD CONSTRAINT listing_suspensions_paused_by_check
        CHECK (paused_by IN ('host_cancellations', 'team', 'verification')) NOT VALID;
DROP POLICY account_names_team_select ON app.account_names;
DROP TABLE app.account_bans;
DROP FUNCTION app.is_banned(uuid);
DROP FUNCTION app.is_admin_account(uuid);
