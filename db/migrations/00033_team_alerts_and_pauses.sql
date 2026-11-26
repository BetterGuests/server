-- +goose Up

ALTER TABLE app.listing_suspensions
    ADD COLUMN paused_by  text NOT NULL DEFAULT 'host_cancellations'
        CHECK (paused_by IN ('host_cancellations', 'team')),
    ADD COLUMN lifted_at  timestamptz,
    ADD COLUMN lifted_by  uuid REFERENCES app.accounts(id),
    ADD CONSTRAINT listing_suspensions_lift_shape CHECK ((lifted_at IS NULL) = (lifted_by IS NULL));

CREATE POLICY listing_suspensions_team_insert ON app.listing_suspensions
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        app.is_admin()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_suspensions.listing_id AND l.host_id = listing_suspensions.host_id
        )
    );

CREATE POLICY listing_suspensions_team_update ON app.listing_suspensions
    FOR UPDATE TO __APP_ROLE__
    USING (app.is_admin())
    WITH CHECK (app.is_admin());

CREATE POLICY listing_suspensions_pause_again ON app.listing_suspensions
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id() AND lifted_at IS NOT NULL)
    WITH CHECK (host_id = app.current_user_id() AND lifted_at IS NULL);

GRANT UPDATE (reason, paused_by, created_at, lifted_at, lifted_by) ON app.listing_suspensions TO __APP_ROLE__;

CREATE POLICY booking_cancellations_team_select ON app.booking_cancellations
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

-- +goose StatementBegin
CREATE FUNCTION app.only_admins_lift_pauses()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.lifted_at IS NOT NULL AND NEW.lifted_at IS DISTINCT FROM OLD.lifted_at AND NOT app.is_admin() THEN
        RAISE EXCEPTION 'only the team can lift a pause' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER listing_suspensions_only_admins_lift
    BEFORE UPDATE ON app.listing_suspensions
    FOR EACH ROW
    EXECUTE FUNCTION app.only_admins_lift_pauses();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.refuse_unavailable_booking()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM app.listing_suspensions s
        WHERE s.listing_id = NEW.listing_id AND s.lifted_at IS NULL
    ) THEN
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

ALTER TABLE app.notifications ALTER COLUMN booking_id DROP NOT NULL;
ALTER TABLE app.notifications
    ADD COLUMN listing_id      uuid REFERENCES app.listings(id) ON DELETE CASCADE,
    ADD COLUMN event_key       text CHECK (event_key IS NULL OR length(event_key) BETWEEN 1 AND 200),
    ADD COLUMN for_team        boolean NOT NULL DEFAULT false,
    ADD COLUMN emailed_at      timestamptz,
    ADD COLUMN email_attempts  integer NOT NULL DEFAULT 0 CHECK (email_attempts >= 0),
    ADD CONSTRAINT notifications_subject CHECK (booking_id IS NOT NULL OR listing_id IS NOT NULL);

CREATE UNIQUE INDEX notifications_one_per_listing_event_idx
    ON app.notifications (account_id, event_key) WHERE event_key IS NOT NULL;
CREATE INDEX notifications_awaiting_email_idx
    ON app.notifications (created_at) WHERE emailed_at IS NULL;

CREATE POLICY notifications_migrator_update ON app.notifications
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE POLICY accounts_team_alert_select ON app.accounts
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (EXISTS (SELECT 1 FROM app.admin_accounts a WHERE a.account_id = accounts.id));

CREATE POLICY accounts_team_alert_insert ON app.accounts
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (EXISTS (SELECT 1 FROM app.admin_accounts a WHERE a.account_id = accounts.id));

-- +goose StatementBegin
CREATE FUNCTION app.queue_team_alerts()
RETURNS TABLE (account_id uuid, booking_id uuid, kind text, title text, body text, for_team boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN
    INSERT INTO app.accounts (id)
    SELECT a.account_id FROM app.admin_accounts a
    ON CONFLICT (id) DO NOTHING;

    RETURN QUERY
    WITH wanted AS (
        SELECT a.account_id AS recipient, r.booking_id AS about_booking, NULL::uuid AS about_listing,
               'report_filed' AS event, 'report_filed:' || r.booking_id AS dedupe, true AS team,
               'A guest reported a problem' AS heading,
               left('Review the report about ' || r.listing_title || '.', 500) AS message
        FROM app.stay_reports r
        CROSS JOIN app.admin_accounts a
        WHERE r.status = 'open'
        UNION ALL
        SELECT r.guest_id, r.booking_id, NULL, 'report_received', 'report_received:' || r.booking_id, false,
               'We got your report',
               left('Our team is looking at the problem you reported at ' || r.listing_title || '.', 500)
        FROM app.stay_reports r
        WHERE r.status = 'open'
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), false,
               'Your listing is paused',
               left(l.title || ' is not taking new bookings. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days'
        UNION ALL
        SELECT a.account_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), true,
               'A listing was paused',
               left(l.title || ' is paused. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        CROSS JOIN app.admin_accounts a
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days' AND a.account_id <> s.host_id
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_unpaused',
               'listing_unpaused:' || s.listing_id || ':' || extract(epoch FROM s.lifted_at), false,
               'Your listing is live again',
               left(l.title || ' can take bookings again.', 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at > now() - interval '3 days'
    ),
    inserted AS (
        INSERT INTO app.notifications (id, account_id, booking_id, listing_id, kind, event_key, for_team, title, body)
        SELECT gen_random_uuid(), w.recipient, w.about_booking, w.about_listing, w.event, w.dedupe, w.team,
               w.heading, w.message
        FROM wanted w
        ON CONFLICT DO NOTHING
        RETURNING account_id, booking_id, kind, title, body, for_team
    )
    SELECT i.account_id, i.booking_id, i.kind, i.title, i.body, i.for_team FROM inserted i;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.notifications_awaiting_email(batch_size integer)
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
                     'listing_paused', 'listing_unpaused')
      AND (NOT n.for_team OR a.account_id IS NOT NULL)
    ORDER BY n.email_attempts, n.created_at
    LIMIT least(greatest(batch_size, 1), 200)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_email_attempt(notification_id uuid, delivered boolean)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.notifications n
    SET email_attempts = n.email_attempts + 1,
        emailed_at = CASE WHEN delivered THEN now() ELSE n.emailed_at END
    WHERE n.id = notification_id AND n.emailed_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.queue_team_alerts() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.notifications_awaiting_email(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.record_email_attempt(uuid, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.queue_team_alerts() TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.notifications_awaiting_email(integer) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.record_email_attempt(uuid, boolean) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.record_email_attempt(uuid, boolean);
DROP FUNCTION app.notifications_awaiting_email(integer);
DROP FUNCTION app.queue_team_alerts();
DROP POLICY accounts_team_alert_insert ON app.accounts;
DROP POLICY accounts_team_alert_select ON app.accounts;
DROP POLICY notifications_migrator_update ON app.notifications;
DROP INDEX app.notifications_awaiting_email_idx;
DROP INDEX app.notifications_one_per_listing_event_idx;
CREATE POLICY notifications_migrator_delete ON app.notifications
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (booking_id IS NULL);
DELETE FROM app.notifications WHERE booking_id IS NULL;
DROP POLICY notifications_migrator_delete ON app.notifications;
ALTER TABLE app.notifications
    DROP CONSTRAINT notifications_subject,
    DROP COLUMN email_attempts,
    DROP COLUMN emailed_at,
    DROP COLUMN for_team,
    DROP COLUMN event_key,
    DROP COLUMN listing_id;
ALTER TABLE app.notifications ALTER COLUMN booking_id SET NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.refuse_unavailable_booking()
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

DROP TRIGGER listing_suspensions_only_admins_lift ON app.listing_suspensions;
DROP FUNCTION app.only_admins_lift_pauses();
DROP POLICY booking_cancellations_team_select ON app.booking_cancellations;
REVOKE UPDATE (reason, paused_by, created_at, lifted_at, lifted_by) ON app.listing_suspensions FROM __APP_ROLE__;
DROP POLICY listing_suspensions_pause_again ON app.listing_suspensions;
DROP POLICY listing_suspensions_team_update ON app.listing_suspensions;
DROP POLICY listing_suspensions_team_insert ON app.listing_suspensions;
ALTER TABLE app.listing_suspensions
    DROP CONSTRAINT listing_suspensions_lift_shape,
    DROP COLUMN lifted_by,
    DROP COLUMN lifted_at,
    DROP COLUMN paused_by;
