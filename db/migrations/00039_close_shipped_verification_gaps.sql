-- +goose Up

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_identity_data_deletion()
RETURNS TABLE (id uuid, persona_inquiry_id text, held boolean)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.identity_verifications v
    SET revoked_at = now(), updated_at = now()
    WHERE v.account_id = app.current_user_id() AND v.revoked_at IS NULL;

    DELETE FROM app.identity_names n
    WHERE n.account_id = app.current_user_id()
      AND NOT EXISTS (
        SELECT 1 FROM app.identity_verification_holds h
        WHERE h.verification_id = n.verification_id AND h.released_at IS NULL
      );

    DELETE FROM app.identity_fingerprints f
    WHERE f.account_id = app.current_user_id()
      AND NOT EXISTS (
        SELECT 1 FROM app.identity_verification_holds h
        WHERE h.verification_id = f.verification_id AND h.released_at IS NULL
      );

    WITH flagged AS (
        UPDATE app.identity_verifications v
        SET deletion_requested_at = COALESCE(v.deletion_requested_at, now()), updated_at = now()
        WHERE v.account_id = app.current_user_id() AND v.redacted_at IS NULL
        RETURNING v.id, v.persona_inquiry_id
    ),
    flagged_documents AS (
        UPDATE app.property_proof_inquiries i
        SET deletion_requested_at = COALESCE(i.deletion_requested_at, now())
        WHERE i.host_id = app.current_user_id()
          AND i.redacted_at IS NULL
          AND NOT EXISTS (
            SELECT 1 FROM app.property_proofs p
            WHERE p.persona_inquiry_id = i.persona_inquiry_id AND p.status = 'approved'
          )
        RETURNING i.id, i.persona_inquiry_id
    )
    SELECT f.id, f.persona_inquiry_id,
        EXISTS (
            SELECT 1 FROM app.identity_verification_holds h
            WHERE h.verification_id = f.id AND h.released_at IS NULL
        )
    FROM flagged f
    UNION ALL
    SELECT d.id, d.persona_inquiry_id, false
    FROM flagged_documents d
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.apply_verification_pauses(valid_days integer, grace_days integer)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH problems AS MATERIALIZED (
        SELECT p.listing_id, p.host_id, p.reason, p.since
        FROM app.listing_verification_problems(valid_days, grace_days) p
    ),
    reopened AS (
        UPDATE app.listing_suspensions s
        SET reason = p.reason, paused_by = 'verification', created_at = now(), lifted_at = NULL, lifted_by = NULL
        FROM problems p
        WHERE s.listing_id = p.listing_id
          AND s.lifted_at IS NOT NULL
          AND (s.lifted_by IS NULL OR s.lifted_at < p.since)
        RETURNING s.listing_id
    ),
    resolved AS (
        UPDATE app.listing_suspensions s
        SET lifted_at = now()
        WHERE s.paused_by = 'verification' AND s.lifted_at IS NULL
          AND NOT EXISTS (SELECT 1 FROM problems p WHERE p.listing_id = s.listing_id)
        RETURNING s.listing_id
    )
    INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by)
    SELECT p.listing_id, p.host_id, p.reason, 'verification'
    FROM problems p
    WHERE NOT EXISTS (SELECT 1 FROM app.listing_suspensions s WHERE s.listing_id = p.listing_id)
    ON CONFLICT (listing_id) DO NOTHING
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.queue_team_alerts()
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
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
        UNION ALL
        SELECT r.guest_id, r.booking_id, NULL, 'report_received', 'report_received:' || r.booking_id, false,
               'We got your report',
               left('Our team is looking at the problem you reported at ' || r.listing_title || '.', 500)
        FROM app.stay_reports r
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
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
        UNION ALL
        SELECT e.account_id, NULL, NULL, 'id_expiring',
               'id_expiring:' || e.verification_id || ':' || e.days_ahead, false,
               'Your ID check expires soon',
               'Verify your ID again before ' || to_char(e.expires_at, 'FMMon FMDD, YYYY') || ' to keep booking and hosting.'
        FROM (
            SELECT latest.account_id, latest.verification_id, latest.expires_at,
                   CASE WHEN latest.expires_at - now() <= interval '7 days' THEN 7 ELSE 30 END AS days_ahead
            FROM (
                SELECT DISTINCT ON (v.account_id) v.account_id, v.id AS verification_id,
                       COALESCE(v.last_event_at, v.updated_at) + interval '365 days' AS expires_at
                FROM app.identity_verifications v
                WHERE v.status = 'completed' AND v.revoked_at IS NULL
                  AND (v.review_state IS NULL OR v.review_state = 'cleared')
                ORDER BY v.account_id, COALESCE(v.last_event_at, v.updated_at) DESC
            ) latest
            WHERE latest.expires_at > now() AND latest.expires_at - now() <= interval '30 days'
        ) e
        UNION ALL
        SELECT v.account_id, NULL, NULL, 'identity_in_review', 'identity_in_review:' || v.id, false,
               'We are checking your ID',
               'Our team needs a closer look at your latest ID check.'
        FROM app.identity_verifications v
        WHERE v.review_state = 'pending' AND v.status = 'completed'
        UNION ALL
        SELECT a.account_id, NULL, NULL, 'identity_review', 'identity_review:' || v.id, true,
               'An ID check needs review',
               'An ID check is waiting for the team.'
        FROM app.identity_verifications v
        CROSS JOIN app.admin_accounts a
        WHERE v.review_state = 'pending' AND v.status = 'completed' AND a.account_id <> v.account_id
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

REVOKE ALL ON FUNCTION app.apply_verification_pauses(integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.apply_verification_pauses(integer, integer) TO __APP_ROLE__;

-- +goose Down

REVOKE EXECUTE ON FUNCTION app.apply_verification_pauses(integer, integer) FROM __APP_ROLE__;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_identity_data_deletion()
RETURNS TABLE (id uuid, persona_inquiry_id text, held boolean)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    DELETE FROM app.identity_names n
    WHERE n.account_id = app.current_user_id()
      AND NOT EXISTS (
        SELECT 1 FROM app.identity_verification_holds h
        WHERE h.verification_id = n.verification_id AND h.released_at IS NULL
      );

    DELETE FROM app.identity_fingerprints f
    WHERE f.account_id = app.current_user_id()
      AND NOT EXISTS (
        SELECT 1 FROM app.identity_verification_holds h
        WHERE h.verification_id = f.verification_id AND h.released_at IS NULL
      );

    WITH flagged AS (
        UPDATE app.identity_verifications v
        SET deletion_requested_at = COALESCE(v.deletion_requested_at, now()), updated_at = now()
        WHERE v.account_id = app.current_user_id() AND v.redacted_at IS NULL
        RETURNING v.id, v.persona_inquiry_id
    ),
    flagged_documents AS (
        UPDATE app.property_proof_inquiries i
        SET deletion_requested_at = COALESCE(i.deletion_requested_at, now())
        WHERE i.host_id = app.current_user_id()
          AND i.redacted_at IS NULL
          AND NOT EXISTS (
            SELECT 1 FROM app.property_proofs p
            WHERE p.persona_inquiry_id = i.persona_inquiry_id AND p.status = 'approved'
          )
        RETURNING i.id, i.persona_inquiry_id
    )
    SELECT f.id, f.persona_inquiry_id,
        EXISTS (
            SELECT 1 FROM app.identity_verification_holds h
            WHERE h.verification_id = f.id AND h.released_at IS NULL
        )
    FROM flagged f
    UNION ALL
    SELECT d.id, d.persona_inquiry_id, false
    FROM flagged_documents d
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.apply_verification_pauses(valid_days integer, grace_days integer)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    INSERT INTO app.listing_suspensions (listing_id, host_id, reason, paused_by)
    SELECT p.listing_id, p.host_id, p.reason, 'verification'
    FROM app.listing_verification_problems(valid_days, grace_days) p
    ON CONFLICT (listing_id) DO UPDATE
    SET reason = EXCLUDED.reason, paused_by = 'verification', created_at = now(), lifted_at = NULL, lifted_by = NULL
    WHERE app.listing_suspensions.lifted_at IS NOT NULL
      AND (
        app.listing_suspensions.lifted_by IS NULL
        OR app.listing_suspensions.lifted_at < (
            SELECT q.since FROM app.listing_verification_problems(valid_days, grace_days) q
            WHERE q.listing_id = app.listing_suspensions.listing_id
        )
      );

    UPDATE app.listing_suspensions s
    SET lifted_at = now()
    WHERE s.paused_by = 'verification' AND s.lifted_at IS NULL
      AND NOT EXISTS (
        SELECT 1 FROM app.listing_verification_problems(valid_days, grace_days) p
        WHERE p.listing_id = s.listing_id
      )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.queue_team_alerts()
RETURNS TABLE (account_id uuid, booking_id uuid, kind text, title text, body text, for_team boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN
    PERFORM app.apply_verification_pauses(365, 30);

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
        UNION ALL
        SELECT e.account_id, NULL, NULL, 'id_expiring',
               'id_expiring:' || e.verification_id || ':' || e.days_ahead, false,
               'Your ID check expires soon',
               'Verify your ID again before ' || to_char(e.expires_at, 'FMMon FMDD, YYYY') || ' to keep booking and hosting.'
        FROM (
            SELECT latest.account_id, latest.verification_id, latest.expires_at,
                   CASE WHEN latest.expires_at - now() <= interval '7 days' THEN 7 ELSE 30 END AS days_ahead
            FROM (
                SELECT DISTINCT ON (v.account_id) v.account_id, v.id AS verification_id,
                       COALESCE(v.last_event_at, v.updated_at) + interval '365 days' AS expires_at
                FROM app.identity_verifications v
                WHERE v.status = 'completed' AND v.revoked_at IS NULL
                  AND (v.review_state IS NULL OR v.review_state = 'cleared')
                ORDER BY v.account_id, COALESCE(v.last_event_at, v.updated_at) DESC
            ) latest
            WHERE latest.expires_at > now() AND latest.expires_at - now() <= interval '30 days'
        ) e
        UNION ALL
        SELECT v.account_id, NULL, NULL, 'identity_in_review', 'identity_in_review:' || v.id, false,
               'We are checking your ID',
               'Our team needs a closer look at your latest ID check.'
        FROM app.identity_verifications v
        WHERE v.review_state = 'pending' AND v.status = 'completed'
        UNION ALL
        SELECT a.account_id, NULL, NULL, 'identity_review', 'identity_review:' || v.id, true,
               'An ID check needs review',
               'An ID check is waiting for the team.'
        FROM app.identity_verifications v
        CROSS JOIN app.admin_accounts a
        WHERE v.review_state = 'pending' AND v.status = 'completed' AND a.account_id <> v.account_id
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
