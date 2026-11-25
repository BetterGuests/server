-- +goose Up

CREATE TABLE app.booking_cancellations (
    booking_id        uuid PRIMARY KEY REFERENCES app.bookings(id),
    guest_id          uuid NOT NULL REFERENCES app.accounts(id),
    host_id           uuid NOT NULL REFERENCES app.accounts(id),
    listing_id        uuid NOT NULL REFERENCES app.listings(id),
    cancelled_by      text NOT NULL CHECK (cancelled_by IN ('guest', 'host', 'expired')),
    amount_cents      integer NOT NULL CHECK (amount_cents >= 0),
    refund_cents      integer NOT NULL CHECK (refund_cents >= 0 AND refund_cents <= amount_cents),
    payment_intent_id text CHECK (payment_intent_id IS NULL OR length(payment_intent_id) BETWEEN 1 AND 200),
    refund_status     text NOT NULL CHECK (refund_status IN ('none', 'pending', 'succeeded')),
    stripe_refund_id  text UNIQUE CHECK (stripe_refund_id IS NULL OR length(stripe_refund_id) BETWEEN 1 AND 200),
    created_at        timestamptz NOT NULL DEFAULT now(),
    CHECK ((refund_status = 'none') = (refund_cents = 0)),
    CHECK (refund_cents = 0 OR payment_intent_id IS NOT NULL)
);

CREATE INDEX booking_cancellations_guest_id_idx ON app.booking_cancellations (guest_id);
CREATE INDEX booking_cancellations_host_id_idx ON app.booking_cancellations (host_id);
CREATE INDEX booking_cancellations_listing_id_idx ON app.booking_cancellations (listing_id, created_at);

CREATE TABLE app.listing_suspensions (
    listing_id  uuid PRIMARY KEY REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id     uuid NOT NULL REFERENCES app.accounts(id),
    reason      text NOT NULL CHECK (length(reason) BETWEEN 1 AND 300),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX listing_suspensions_host_id_idx ON app.listing_suspensions (host_id);

CREATE TABLE app.stay_reports (
    booking_id        uuid PRIMARY KEY REFERENCES app.bookings(id),
    guest_id          uuid NOT NULL REFERENCES app.accounts(id),
    host_id           uuid NOT NULL REFERENCES app.accounts(id),
    listing_title     text NOT NULL CHECK (length(listing_title) BETWEEN 1 AND 200),
    details           text NOT NULL CHECK (length(details) BETWEEN 1 AND 4000),
    amount_cents      integer NOT NULL CHECK (amount_cents > 0),
    payment_intent_id text NOT NULL CHECK (length(payment_intent_id) BETWEEN 1 AND 200),
    status            text NOT NULL CHECK (status IN ('open', 'resolved')),
    refund_cents      integer NOT NULL DEFAULT 0 CHECK (refund_cents >= 0 AND refund_cents <= amount_cents),
    refund_status     text NOT NULL DEFAULT 'none' CHECK (refund_status IN ('none', 'pending', 'succeeded')),
    stripe_refund_id  text UNIQUE CHECK (stripe_refund_id IS NULL OR length(stripe_refund_id) BETWEEN 1 AND 200),
    resolution_note   text CHECK (resolution_note IS NULL OR length(resolution_note) <= 1000),
    resolved_by       uuid REFERENCES app.accounts(id),
    resolved_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'resolved') = (resolved_by IS NOT NULL AND resolved_at IS NOT NULL))
);

CREATE INDEX stay_reports_guest_id_idx ON app.stay_reports (guest_id);
CREATE INDEX stay_reports_host_id_idx ON app.stay_reports (host_id);
CREATE INDEX stay_reports_status_idx ON app.stay_reports (status, created_at);

ALTER TABLE app.booking_cancellations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_cancellations FORCE ROW LEVEL SECURITY;
ALTER TABLE app.listing_suspensions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_suspensions FORCE ROW LEVEL SECURITY;
ALTER TABLE app.stay_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.stay_reports FORCE ROW LEVEL SECURITY;

CREATE POLICY booking_cancellations_select ON app.booking_cancellations
    FOR SELECT TO __APP_ROLE__
    USING (guest_id = app.current_user_id() OR host_id = app.current_user_id());

CREATE POLICY booking_cancellations_insert ON app.booking_cancellations
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        (
            (cancelled_by IN ('guest', 'expired') AND guest_id = app.current_user_id())
            OR (cancelled_by = 'host' AND host_id = app.current_user_id() AND refund_cents = amount_cents)
        )
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = booking_cancellations.booking_id
              AND b.guest_id = booking_cancellations.guest_id
              AND b.listing_id = booking_cancellations.listing_id
              AND l.host_id = booking_cancellations.host_id
        )
    );

CREATE POLICY booking_cancellations_update ON app.booking_cancellations
    FOR UPDATE TO __APP_ROLE__
    USING (refund_status = 'pending' AND (guest_id = app.current_user_id() OR host_id = app.current_user_id()))
    WITH CHECK (guest_id = app.current_user_id() OR host_id = app.current_user_id());

CREATE POLICY booking_cancellations_migrator_select ON app.booking_cancellations
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.booking_cancellations TO __APP_ROLE__;
GRANT UPDATE (refund_status, stripe_refund_id) ON app.booking_cancellations TO __APP_ROLE__;

CREATE POLICY bookings_cancel_confirmed ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (
        status = 'confirmed'
        AND EXISTS (
            SELECT 1 FROM app.booking_cancellations c
            WHERE c.booking_id = bookings.id
              AND (
                  (c.cancelled_by = 'guest' AND c.guest_id = app.current_user_id())
                  OR (c.cancelled_by = 'host' AND c.host_id = app.current_user_id())
              )
        )
    )
    WITH CHECK (
        status = 'cancelled'
        AND EXISTS (SELECT 1 FROM app.booking_cancellations c WHERE c.booking_id = bookings.id)
    );

CREATE POLICY listing_suspensions_select ON app.listing_suspensions
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id() OR app.is_admin());

CREATE POLICY listing_suspensions_insert ON app.listing_suspensions
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_suspensions.listing_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY listing_suspensions_migrator_select ON app.listing_suspensions
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.listing_suspensions TO __APP_ROLE__;

CREATE POLICY stay_reports_select ON app.stay_reports
    FOR SELECT TO __APP_ROLE__
    USING (guest_id = app.current_user_id() OR host_id = app.current_user_id() OR app.is_admin());

CREATE POLICY stay_reports_insert ON app.stay_reports
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND status = 'open'
        AND refund_cents = 0
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.payments p ON p.booking_id = b.id
            WHERE b.id = stay_reports.booking_id
              AND b.guest_id = app.current_user_id()
              AND b.status IN ('confirmed', 'completed')
              AND p.status = 'succeeded'
              AND p.host_id = stay_reports.host_id
              AND p.amount_cents = stay_reports.amount_cents
              AND p.stripe_payment_intent_id = stay_reports.payment_intent_id
        )
    );

CREATE POLICY stay_reports_decide ON app.stay_reports
    FOR UPDATE TO __APP_ROLE__
    USING (
        app.is_admin()
        AND guest_id <> app.current_user_id()
        AND host_id <> app.current_user_id()
        AND (status = 'open' OR refund_status = 'pending')
    )
    WITH CHECK (app.is_admin());

CREATE POLICY stay_reports_refund ON app.stay_reports
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id() AND status = 'resolved' AND refund_status = 'pending')
    WITH CHECK (guest_id = app.current_user_id() AND status = 'resolved');

CREATE POLICY stay_reports_migrator_select ON app.stay_reports
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.stay_reports TO __APP_ROLE__;
GRANT UPDATE (status, refund_cents, refund_status, stripe_refund_id, resolution_note, resolved_by, resolved_at)
    ON app.stay_reports TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.only_admins_decide_reports()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NOT app.is_admin() AND (NEW.status <> OLD.status OR NEW.refund_cents <> OLD.refund_cents) THEN
        RAISE EXCEPTION 'only the team can decide a report' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER stay_reports_only_admins_decide
    BEFORE UPDATE ON app.stay_reports
    FOR EACH ROW
    EXECUTE FUNCTION app.only_admins_decide_reports();

-- +goose StatementBegin
CREATE FUNCTION app.refuse_unavailable_booking()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.listing_suspensions s WHERE s.listing_id = NEW.listing_id) THEN
        RAISE EXCEPTION 'listing is paused' USING ERRCODE = 'restrict_violation';
    END IF;
    IF EXISTS (
        SELECT 1 FROM app.booking_cancellations c
        JOIN app.bookings b ON b.id = c.booking_id
        WHERE c.listing_id = NEW.listing_id
          AND c.cancelled_by = 'host'
          AND daterange(b.starts_on, b.ends_on, '[)') && daterange(NEW.starts_on, NEW.ends_on, '[)')
    ) THEN
        RAISE EXCEPTION 'dates are blocked' USING ERRCODE = 'exclusion_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.refuse_unavailable_booking() FROM PUBLIC;

CREATE TRIGGER bookings_refuse_unavailable
    BEFORE INSERT ON app.bookings
    FOR EACH ROW
    EXECUTE FUNCTION app.refuse_unavailable_booking();

-- +goose StatementBegin
CREATE FUNCTION app.expired_booking_requests(batch_size integer)
RETURNS TABLE (booking_id uuid, guest_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, b.guest_id
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    LEFT JOIN app.payments p ON p.booking_id = b.id
    CROSS JOIN LATERAL (
        SELECT CASE WHEN p.status = 'authorized' THEN coalesce(p.last_event_at, b.created_at) ELSE b.created_at END AS asked_at
    ) asked
    WHERE b.status = 'requested'
      AND now() >= least(
          asked.asked_at + interval '12 hours',
          greatest(
              (b.starts_on + l.check_in_time) AT TIME ZONE l.time_zone,
              asked.asked_at + interval '1 hour'
          )
      )
    ORDER BY b.created_at
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.pending_booking_refunds(batch_size integer)
RETURNS TABLE (booking_id uuid, guest_id uuid, source text, payment_intent_id text, amount_cents integer)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT c.booking_id, c.guest_id, 'cancellation', c.payment_intent_id, c.refund_cents
    FROM app.booking_cancellations c
    WHERE c.refund_status = 'pending'
    UNION ALL
    SELECT r.booking_id, r.guest_id, 'report', r.payment_intent_id, r.refund_cents
    FROM app.stay_reports r
    WHERE r.status = 'resolved' AND r.refund_status = 'pending'
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.expired_booking_requests(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.expired_booking_requests(integer) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.pending_booking_refunds(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.pending_booking_refunds(integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.listing_booked_dates(target_listing_id uuid, earliest_checkout date, range_limit integer)
RETURNS TABLE (starts_on date, ends_on date)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.starts_on, b.ends_on
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    WHERE b.listing_id = target_listing_id
      AND l.status = 'published'
      AND (
          b.status IN ('requested', 'pending', 'confirmed')
          OR EXISTS (
              SELECT 1 FROM app.booking_cancellations c
              WHERE c.booking_id = b.id AND c.cancelled_by = 'host'
          )
      )
      AND b.ends_on >= earliest_checkout
    ORDER BY b.starts_on
    LIMIT least(greatest(range_limit, 1), 500)
$$;
-- +goose StatementEnd

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

DROP FUNCTION app.stay_journey(uuid, integer);

-- +goose StatementBegin
CREATE FUNCTION app.stay_journey(after_booking_id uuid, batch_size integer)
RETURNS TABLE (
    booking_id uuid,
    listing_title text,
    status text,
    payment_status text,
    guest_id uuid,
    host_id uuid,
    guest_name text,
    starts_on date,
    ends_on date,
    check_in_time text,
    check_out_time text,
    time_zone text,
    completed_at timestamptz,
    checked_in_at timestamptz,
    checked_out_at timestamptz,
    has_arrival_guide boolean,
    guest_reviewed boolean,
    host_reviewed boolean,
    guest_review_published boolean,
    host_review_published boolean,
    sent_notifications text[],
    posted_events text[],
    cancelled_by text,
    cancellation_refund_cents integer,
    report_status text,
    report_refund_cents integer
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, l.title, b.status, coalesce(p.status, ''), b.guest_id, l.host_id,
           coalesce(n.display_name, ''), b.starts_on, b.ends_on,
           to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
           b.completed_at, s.checked_in_at, s.checked_out_at,
           EXISTS (SELECT 1 FROM app.listing_arrival_guides a WHERE a.listing_id = l.id),
           r.id IS NOT NULL, g.id IS NOT NULL,
           r.published_at IS NOT NULL, g.published_at IS NOT NULL,
           ARRAY(SELECT x.kind || ':' || x.account_id::text FROM app.notifications x WHERE x.booking_id = b.id),
           ARRAY(SELECT m.event_kind FROM app.messages m WHERE m.booking_id = b.id AND m.event_kind IS NOT NULL),
           coalesce(c.cancelled_by, ''), coalesce(c.refund_cents, 0),
           coalesce(q.status, ''), coalesce(q.refund_cents, 0)
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    LEFT JOIN app.payments p ON p.booking_id = b.id
    LEFT JOIN app.account_names n ON n.account_id = b.guest_id
    LEFT JOIN app.booking_stays s ON s.booking_id = b.id
    LEFT JOIN app.listing_reviews r ON r.booking_id = b.id
    LEFT JOIN app.guest_reviews g ON g.booking_id = b.id
    LEFT JOIN app.booking_cancellations c ON c.booking_id = b.id
    LEFT JOIN app.stay_reports q ON q.booking_id = b.id
    WHERE b.id > after_booking_id
      AND (
          (b.status = 'completed' AND b.completed_at > now() - interval '21 days')
          OR (b.status <> 'completed' AND b.ends_on >= current_date - 1)
      )
    ORDER BY b.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.stay_journey(uuid, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.stay_journey(uuid, integer) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.stay_journey(uuid, integer);

-- +goose StatementBegin
CREATE FUNCTION app.stay_journey(after_booking_id uuid, batch_size integer)
RETURNS TABLE (
    booking_id uuid,
    listing_title text,
    status text,
    payment_status text,
    guest_id uuid,
    host_id uuid,
    guest_name text,
    starts_on date,
    ends_on date,
    check_in_time text,
    check_out_time text,
    time_zone text,
    completed_at timestamptz,
    checked_in_at timestamptz,
    checked_out_at timestamptz,
    has_arrival_guide boolean,
    guest_reviewed boolean,
    host_reviewed boolean,
    guest_review_published boolean,
    host_review_published boolean,
    sent_notifications text[],
    posted_events text[]
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, l.title, b.status, coalesce(p.status, ''), b.guest_id, l.host_id,
           coalesce(n.display_name, ''), b.starts_on, b.ends_on,
           to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
           b.completed_at, s.checked_in_at, s.checked_out_at,
           EXISTS (SELECT 1 FROM app.listing_arrival_guides a WHERE a.listing_id = l.id),
           r.id IS NOT NULL, g.id IS NOT NULL,
           r.published_at IS NOT NULL, g.published_at IS NOT NULL,
           ARRAY(SELECT x.kind || ':' || x.account_id::text FROM app.notifications x WHERE x.booking_id = b.id),
           ARRAY(SELECT m.event_kind FROM app.messages m WHERE m.booking_id = b.id AND m.event_kind IS NOT NULL)
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    LEFT JOIN app.payments p ON p.booking_id = b.id
    LEFT JOIN app.account_names n ON n.account_id = b.guest_id
    LEFT JOIN app.booking_stays s ON s.booking_id = b.id
    LEFT JOIN app.listing_reviews r ON r.booking_id = b.id
    LEFT JOIN app.guest_reviews g ON g.booking_id = b.id
    WHERE b.id > after_booking_id
      AND (
          (b.status = 'completed' AND b.completed_at > now() - interval '21 days')
          OR (b.status <> 'completed' AND b.ends_on >= current_date - 1)
      )
    ORDER BY b.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.stay_journey(uuid, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.stay_journey(uuid, integer) TO __APP_ROLE__;

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.listing_booked_dates(target_listing_id uuid, earliest_checkout date, range_limit integer)
RETURNS TABLE (starts_on date, ends_on date)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.starts_on, b.ends_on
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    WHERE b.listing_id = target_listing_id
      AND l.status = 'published'
      AND b.status IN ('requested', 'pending', 'confirmed')
      AND b.ends_on >= earliest_checkout
    ORDER BY b.starts_on
    LIMIT least(greatest(range_limit, 1), 500)
$$;
-- +goose StatementEnd

DROP FUNCTION app.pending_booking_refunds(integer);
DROP FUNCTION app.expired_booking_requests(integer);
DROP TRIGGER bookings_refuse_unavailable ON app.bookings;
DROP FUNCTION app.refuse_unavailable_booking();
DROP POLICY bookings_cancel_confirmed ON app.bookings;
DROP TABLE app.stay_reports;
DROP FUNCTION app.only_admins_decide_reports();
DROP TABLE app.listing_suspensions;
DROP TABLE app.booking_cancellations;
