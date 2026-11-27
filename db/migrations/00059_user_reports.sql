-- +goose Up

CREATE TABLE app.user_reports (
    id              uuid PRIMARY KEY,
    reporter_id     uuid NOT NULL REFERENCES app.accounts(id),
    subject_id      uuid NOT NULL REFERENCES app.accounts(id),
    target          text NOT NULL CHECK (target IN ('listing', 'host', 'guest')),
    listing_id      uuid REFERENCES app.listings(id) ON DELETE SET NULL,
    booking_id      uuid REFERENCES app.bookings(id) ON DELETE SET NULL,
    conversation_id uuid REFERENCES app.conversations(id) ON DELETE SET NULL,
    listing_title   text NOT NULL CHECK (length(listing_title) <= 200),
    reporter_name   text NOT NULL CHECK (length(reporter_name) BETWEEN 1 AND 200),
    subject_name    text NOT NULL CHECK (length(subject_name) BETWEEN 1 AND 200),
    reason          text NOT NULL,
    details         text NOT NULL CHECK (length(details) BETWEEN 1 AND 4000),
    status          text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'actioned', 'dismissed')),
    resolution_note text CHECK (resolution_note IS NULL OR length(resolution_note) BETWEEN 1 AND 1000),
    resolved_by     uuid REFERENCES app.accounts(id),
    resolved_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_reports_not_self CHECK (reporter_id <> subject_id),
    CONSTRAINT user_reports_reason_fits_target CHECK (
        (target = 'listing' AND reason IN ('inaccurate', 'scam', 'unsafe', 'offensive', 'other'))
        OR (target = 'host' AND reason IN ('scam', 'off_platform_payment', 'harassment', 'discrimination', 'unsafe', 'other'))
        OR (target = 'guest' AND reason IN ('property_damage', 'scam', 'off_platform_payment', 'harassment', 'discrimination', 'unsafe', 'other'))
    ),
    CONSTRAINT user_reports_resolution_shape CHECK ((status = 'open') = (resolved_by IS NULL AND resolved_at IS NULL))
);

CREATE INDEX user_reports_reporter_id_idx ON app.user_reports (reporter_id);
CREATE INDEX user_reports_subject_id_idx ON app.user_reports (subject_id);
CREATE INDEX user_reports_listing_id_idx ON app.user_reports (listing_id);
CREATE INDEX user_reports_booking_id_idx ON app.user_reports (booking_id);
CREATE INDEX user_reports_conversation_id_idx ON app.user_reports (conversation_id);
CREATE INDEX user_reports_resolved_by_idx ON app.user_reports (resolved_by);
CREATE INDEX user_reports_status_idx ON app.user_reports (status, created_at);

CREATE UNIQUE INDEX user_reports_one_open_per_listing_idx
    ON app.user_reports (reporter_id, listing_id) WHERE status = 'open' AND target = 'listing';
CREATE UNIQUE INDEX user_reports_one_open_per_person_idx
    ON app.user_reports (reporter_id, subject_id, target) WHERE status = 'open' AND target <> 'listing';

ALTER TABLE app.user_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.user_reports FORCE ROW LEVEL SECURITY;

CREATE POLICY user_reports_select ON app.user_reports
    FOR SELECT TO __APP_ROLE__
    USING (
        reporter_id = app.current_user_id()
        OR (app.is_admin() AND subject_id <> app.current_user_id())
    );

CREATE POLICY user_reports_insert ON app.user_reports
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        reporter_id = app.current_user_id()
        AND status = 'open'
        AND resolution_note IS NULL
        AND listing_id IS NOT NULL
        AND (
            (
                target IN ('listing', 'host')
                AND booking_id IS NULL
                AND conversation_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM app.listings l
                    WHERE l.id = user_reports.listing_id
                      AND l.host_id = user_reports.subject_id
                      AND l.status = 'published'
                )
            )
            OR (
                target = 'host'
                AND conversation_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM app.bookings b
                    JOIN app.listings l ON l.id = b.listing_id
                    WHERE b.id = user_reports.booking_id
                      AND b.guest_id = app.current_user_id()
                      AND b.listing_id = user_reports.listing_id
                      AND l.host_id = user_reports.subject_id
                )
            )
            OR (
                target = 'host'
                AND booking_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM app.conversations c
                    WHERE c.id = user_reports.conversation_id
                      AND c.guest_id = app.current_user_id()
                      AND c.host_id = user_reports.subject_id
                      AND c.listing_id = user_reports.listing_id
                )
            )
            OR (
                target = 'guest'
                AND conversation_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM app.bookings b
                    JOIN app.listings l ON l.id = b.listing_id
                    WHERE b.id = user_reports.booking_id
                      AND l.host_id = app.current_user_id()
                      AND b.listing_id = user_reports.listing_id
                      AND b.guest_id = user_reports.subject_id
                )
            )
            OR (
                target = 'guest'
                AND booking_id IS NULL
                AND EXISTS (
                    SELECT 1 FROM app.conversations c
                    WHERE c.id = user_reports.conversation_id
                      AND c.host_id = app.current_user_id()
                      AND c.guest_id = user_reports.subject_id
                      AND c.listing_id = user_reports.listing_id
                )
            )
        )
    );

CREATE POLICY user_reports_decide ON app.user_reports
    FOR UPDATE TO __APP_ROLE__
    USING (app.is_admin() AND subject_id <> app.current_user_id() AND status = 'open')
    WITH CHECK (app.is_admin() AND resolved_by = app.current_user_id());

CREATE POLICY user_reports_migrator_select ON app.user_reports
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.user_reports TO __APP_ROLE__;
GRANT UPDATE (status, resolution_note, resolved_by, resolved_at) ON app.user_reports TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.queue_user_report_alerts()
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
    WITH inserted AS (
        INSERT INTO app.notifications (id, account_id, listing_id, kind, event_key, for_team, title, body)
        SELECT gen_random_uuid(), a.account_id, r.listing_id, 'user_report_filed', 'user_report_filed:' || r.id, true,
               'A new safety report',
               left('Review the report about a ' || r.target || ' at ' || r.listing_title || '.', 500)
        FROM app.user_reports r
        CROSS JOIN app.admin_accounts a
        WHERE r.status = 'open'
          AND r.created_at > now() - interval '3 days'
          AND r.listing_id IS NOT NULL
          AND a.account_id <> r.subject_id
        ON CONFLICT DO NOTHING
        RETURNING account_id, booking_id, kind, title, body, for_team
    )
    SELECT i.account_id, i.booking_id, i.kind, i.title, i.body, i.for_team FROM inserted i;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.queue_user_report_alerts() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.queue_user_report_alerts() TO __APP_ROLE__;

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
                     'id_expiring', 'identity_in_review', 'identity_review')
      AND (NOT n.for_team OR a.account_id IS NOT NULL)
    ORDER BY n.email_attempts, n.created_at
    LIMIT least(greatest(batch_size, 1), 200)
$$;
-- +goose StatementEnd

DROP FUNCTION app.queue_user_report_alerts();
DROP TABLE app.user_reports;
