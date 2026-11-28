-- +goose Up

CREATE TABLE app.payment_disputes (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_dispute_id   text NOT NULL UNIQUE CHECK (length(stripe_dispute_id) BETWEEN 1 AND 200),
    booking_id          uuid NOT NULL REFERENCES app.bookings(id),
    status              text NOT NULL CHECK (length(status) BETWEEN 1 AND 40),
    reason              text NOT NULL DEFAULT '' CHECK (length(reason) <= 100),
    amount_cents        integer NOT NULL CHECK (amount_cents >= 0),
    last_event_at       timestamptz NOT NULL,
    stripe_reversal_id  text UNIQUE CHECK (stripe_reversal_id IS NULL OR length(stripe_reversal_id) BETWEEN 1 AND 200),
    reversed_cents      integer NOT NULL DEFAULT 0 CHECK (reversed_cents >= 0),
    reversed_by         uuid REFERENCES app.accounts(id),
    reversed_at         timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK ((stripe_reversal_id IS NULL) = (reversed_by IS NULL AND reversed_at IS NULL AND reversed_cents = 0))
);

CREATE INDEX payment_disputes_booking_id_idx ON app.payment_disputes (booking_id);
CREATE INDEX payment_disputes_reversed_by_idx ON app.payment_disputes (reversed_by);

ALTER TABLE app.payment_disputes ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.payment_disputes FORCE ROW LEVEL SECURITY;

CREATE POLICY payment_disputes_team_select ON app.payment_disputes
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

GRANT SELECT ON app.payment_disputes TO __APP_ROLE__;

CREATE POLICY payment_disputes_migrator_select ON app.payment_disputes
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY payment_disputes_migrator_insert ON app.payment_disputes
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY payment_disputes_migrator_update ON app.payment_disputes
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.record_payment_dispute(
    dispute_id text,
    intent_id text,
    dispute_status text,
    dispute_cents integer,
    dispute_reason text,
    happened_at timestamptz
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    disputed record;
BEGIN
    SELECT p.booking_id, p.host_id, l.title INTO disputed
    FROM app.payments p
    JOIN app.bookings b ON b.id = p.booking_id
    JOIN app.listings l ON l.id = b.listing_id
    WHERE p.stripe_payment_intent_id = intent_id;
    IF NOT FOUND THEN
        RETURN false;
    END IF;

    INSERT INTO app.payment_disputes AS d
        (stripe_dispute_id, booking_id, status, reason, amount_cents, last_event_at)
    VALUES (dispute_id, disputed.booking_id, dispute_status, left(dispute_reason, 100), dispute_cents, happened_at)
    ON CONFLICT (stripe_dispute_id) DO UPDATE
    SET status = EXCLUDED.status, reason = EXCLUDED.reason, amount_cents = EXCLUDED.amount_cents,
        last_event_at = EXCLUDED.last_event_at, updated_at = now()
    WHERE d.last_event_at <= EXCLUDED.last_event_at
      AND (d.status NOT IN ('won', 'lost', 'warning_closed') OR EXCLUDED.status IN ('won', 'lost', 'warning_closed'));

    INSERT INTO app.notifications (id, account_id, booking_id, kind, for_team, title, body)
    SELECT gen_random_uuid(), a.account_id, disputed.booking_id, 'payment_disputed', true,
           'A payment was disputed',
           left('A guest''s bank disputed the payment for ' || disputed.title || '. Check it under Payouts.', 500)
    FROM app.admin_accounts a
    JOIN app.accounts known ON known.id = a.account_id
    WHERE a.account_id <> disputed.host_id
    ON CONFLICT DO NOTHING;

    RETURN true;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.record_payment_dispute(text, text, text, integer, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.record_payment_dispute(text, text, text, integer, text, timestamptz) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.team_payment_disputes(only_dispute text, batch_size integer)
RETURNS TABLE (
    dispute_id text,
    listing_title text,
    host_name text,
    status text,
    reason text,
    amount_cents integer,
    currency text,
    transfer_id text,
    paid_out_cents integer,
    reversible_cents integer,
    reversed_cents integer,
    can_reverse boolean
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH found AS (
        SELECT d.stripe_dispute_id, l.title, COALESCE(l.host_name, '') AS host_name, d.status, d.reason,
               d.amount_cents, p.currency, COALESCE(s.stripe_transfer_id, '') AS transfer_id,
               CASE WHEN s.stripe_transfer_id IS NULL THEN 0 ELSE s.payout_cents END AS paid_out_cents,
               greatest(least(
                   d.amount_cents,
                   CASE WHEN s.stripe_transfer_id IS NULL THEN 0 ELSE s.payout_cents END
                       - COALESCE((SELECT sum(o.reversed_cents) FROM app.payment_disputes o WHERE o.booking_id = d.booking_id), 0)::integer
               ), 0) AS reversible_cents,
               d.reversed_cents,
               d.stripe_reversal_id IS NULL AND d.status NOT IN ('won', 'warning_closed')
                   AND app.current_user_id() NOT IN (p.host_id, p.guest_id) AS open_to_viewer,
               d.created_at
        FROM app.payment_disputes d
        JOIN app.bookings b ON b.id = d.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        JOIN app.payments p ON p.booking_id = d.booking_id
        LEFT JOIN app.booking_settlements s ON s.booking_id = d.booking_id
        WHERE app.is_admin() AND (only_dispute = '' OR d.stripe_dispute_id = only_dispute)
    )
    SELECT f.stripe_dispute_id, f.title, f.host_name, f.status, f.reason, f.amount_cents, f.currency,
           f.transfer_id, f.paid_out_cents, f.reversible_cents, f.reversed_cents,
           f.open_to_viewer AND f.reversible_cents > 0
    FROM found f
    ORDER BY f.created_at DESC, f.stripe_dispute_id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.team_payment_disputes(text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.team_payment_disputes(text, integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.record_dispute_reversal(only_dispute text, reversal_id text, cents integer)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH recorded AS (
        UPDATE app.payment_disputes d
        SET stripe_reversal_id = reversal_id, reversed_cents = cents,
            reversed_by = app.current_user_id(), reversed_at = now(), updated_at = now()
        FROM app.payments p
        WHERE d.stripe_dispute_id = only_dispute
          AND p.booking_id = d.booking_id
          AND d.stripe_reversal_id IS NULL
          AND cents > 0
          AND app.is_admin()
          AND app.current_user_id() NOT IN (p.host_id, p.guest_id)
        RETURNING d.id
    )
    SELECT EXISTS (SELECT 1 FROM recorded)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.record_dispute_reversal(text, text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.record_dispute_reversal(text, text, integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.notifications_awaiting_email(batch_size integer)
RETURNS TABLE (
    notification_id uuid,
    account_id uuid,
    booking_id uuid,
    kind text,
    title text,
    body text,
    for_team boolean,
    team_email text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT n.id, n.account_id, n.booking_id, n.kind, n.title, n.body, n.for_team,
           CASE WHEN n.for_team THEN a.email END
    FROM app.notifications n
    LEFT JOIN app.admin_accounts a ON a.account_id = n.account_id
    WHERE n.emailed_at IS NULL
      AND n.email_attempts < 5
      AND n.created_at > now() - interval '3 days'
      AND n.kind IN ('report_filed', 'report_received', 'problem_reported', 'problem_resolved',
                     'listing_paused', 'listing_unpaused',
                     'id_expiring', 'identity_in_review', 'identity_review',
                     'user_report_filed', 'payment_disputed')
      AND (NOT n.for_team OR a.account_id IS NOT NULL)
    ORDER BY n.email_attempts, n.created_at
    LIMIT least(greatest(batch_size, 1), 200)
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.notifications_awaiting_email(batch_size integer)
RETURNS TABLE (
    notification_id uuid,
    account_id uuid,
    booking_id uuid,
    kind text,
    title text,
    body text,
    for_team boolean,
    team_email text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT n.id, n.account_id, n.booking_id, n.kind, n.title, n.body, n.for_team,
           CASE WHEN n.for_team THEN a.email END
    FROM app.notifications n
    LEFT JOIN app.admin_accounts a ON a.account_id = n.account_id
    WHERE n.emailed_at IS NULL
      AND n.email_attempts < 5
      AND n.created_at > now() - interval '3 days'
      AND n.kind IN ('report_filed', 'report_received', 'problem_reported', 'problem_resolved',
                     'listing_paused', 'listing_unpaused',
                     'id_expiring', 'identity_in_review', 'identity_review',
                     'user_report_filed')
      AND (NOT n.for_team OR a.account_id IS NOT NULL)
    ORDER BY n.email_attempts, n.created_at
    LIMIT least(greatest(batch_size, 1), 200)
$$;
-- +goose StatementEnd

DROP FUNCTION app.record_dispute_reversal(text, text, integer);
DROP FUNCTION app.team_payment_disputes(text, integer);
DROP FUNCTION app.record_payment_dispute(text, text, text, integer, text, timestamptz);
DROP TABLE app.payment_disputes;
