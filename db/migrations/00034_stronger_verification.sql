-- +goose Up

ALTER TABLE app.identity_verifications
    ADD COLUMN review_state   text CHECK (review_state IN ('pending', 'cleared', 'rejected')),
    ADD COLUMN review_reasons text[] NOT NULL DEFAULT '{}'
        CHECK (review_reasons <@ ARRAY['name_changed', 'name_unconfirmed', 'under_age', 'duplicate_identity', 'previously_declined']),
    ADD COLUMN reviewed_by    uuid REFERENCES app.accounts(id),
    ADD COLUMN reviewed_at    timestamptz,
    ADD COLUMN revoked_at     timestamptz;

CREATE INDEX identity_verifications_pending_review_idx
    ON app.identity_verifications (created_at) WHERE review_state = 'pending';

CREATE TABLE app.identity_fingerprints (
    verification_id  uuid PRIMARY KEY REFERENCES app.identity_verifications(id),
    account_id       uuid NOT NULL REFERENCES app.accounts(id),
    fingerprint      bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX identity_fingerprints_fingerprint_idx ON app.identity_fingerprints (fingerprint);
CREATE INDEX identity_fingerprints_account_id_idx ON app.identity_fingerprints (account_id);

ALTER TABLE app.identity_fingerprints ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.identity_fingerprints FORCE ROW LEVEL SECURITY;

CREATE POLICY identity_fingerprints_select ON app.identity_fingerprints
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY identity_fingerprints_insert ON app.identity_fingerprints
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        account_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.identity_verifications v
            WHERE v.id = identity_fingerprints.verification_id AND v.account_id = app.current_user_id()
        )
    );

CREATE POLICY identity_fingerprints_migrator_select ON app.identity_fingerprints
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY identity_fingerprints_migrator_delete ON app.identity_fingerprints
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.identity_fingerprints TO __APP_ROLE__;

ALTER TABLE app.property_proofs ADD COLUMN revoked_at timestamptz;
ALTER TABLE app.property_proof_inquiries ADD COLUMN declined_at timestamptz;

ALTER TABLE app.listing_suspensions
    DROP CONSTRAINT listing_suspensions_paused_by_check,
    ADD CONSTRAINT listing_suspensions_paused_by_check
        CHECK (paused_by IN ('host_cancellations', 'team', 'verification')),
    DROP CONSTRAINT listing_suspensions_lift_shape,
    ADD CONSTRAINT listing_suspensions_lift_shape CHECK (
        (lifted_by IS NULL OR lifted_at IS NOT NULL)
        AND (lifted_at IS NULL OR lifted_by IS NOT NULL OR paused_by = 'verification')
    );

CREATE POLICY listing_suspensions_host_replaces_verification ON app.listing_suspensions
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id() AND paused_by = 'verification')
    WITH CHECK (host_id = app.current_user_id() AND paused_by = 'host_cancellations' AND lifted_at IS NULL);

CREATE POLICY listing_suspensions_migrator_insert ON app.listing_suspensions
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY listing_suspensions_migrator_update ON app.listing_suspensions
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

ALTER TABLE app.notifications
    DROP CONSTRAINT notifications_subject,
    ADD CONSTRAINT notifications_subject CHECK (
        booking_id IS NOT NULL OR listing_id IS NOT NULL
        OR kind IN ('id_expiring', 'identity_in_review', 'identity_review')
    );

-- +goose StatementBegin
CREATE FUNCTION app.identity_fingerprint_taken(candidate bytea)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM app.identity_fingerprints f
        JOIN app.identity_verifications v ON v.id = f.verification_id
        WHERE f.fingerprint = candidate
          AND app.current_user_id() IS NOT NULL
          AND f.account_id <> app.current_user_id()
          AND (v.review_state IS NULL OR v.review_state = 'cleared')
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.decline_persona_property_proof(target_inquiry text, declined timestamptz)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.property_proof_inquiries i
    SET declined_at = COALESCE(i.declined_at, declined)
    WHERE i.persona_inquiry_id = target_inquiry AND i.host_id = app.current_user_id();

    WITH revoked AS (
        UPDATE app.property_proofs p
        SET status = 'rejected', rejection_reason = 'Our ID provider declined this document.',
            revoked_at = declined, reviewed_at = declined, updated_at = declined
        WHERE p.persona_inquiry_id = target_inquiry
          AND p.host_id = app.current_user_id()
          AND p.status = 'approved'
        RETURNING p.id
    )
    SELECT EXISTS (SELECT 1 FROM revoked)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.revoke_property_proof(target_proof uuid, revoke_reason text, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    proof_host uuid;
    proof_status text;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT p.host_id, p.status INTO proof_host, proof_status
    FROM app.property_proofs p WHERE p.id = target_proof FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF proof_host = app.current_user_id() THEN
        RETURN 'own';
    END IF;
    IF proof_status <> 'approved' THEN
        RETURN 'wrong_state';
    END IF;
    UPDATE app.property_proofs p
    SET status = 'rejected', rejection_reason = revoke_reason, revoked_at = decided,
        reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
    WHERE p.id = target_proof;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.pending_identity_reviews(batch_size integer)
RETURNS TABLE (
    verification_id uuid,
    account_id uuid,
    persona_inquiry_id text,
    reasons text[],
    completed_at timestamptz,
    new_name text,
    previous_name text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT v.id, v.account_id, v.persona_inquiry_id, v.review_reasons,
           COALESCE(v.last_event_at, v.updated_at),
           COALESCE((
               SELECT concat_ws(' ', NULLIF(n.first_name, ''), NULLIF(n.middle_name, ''), NULLIF(n.last_name, ''), NULLIF(n.suffix, ''))
               FROM app.identity_names n WHERE n.verification_id = v.id
           ), ''),
           COALESCE((
               SELECT concat_ws(' ', NULLIF(n.first_name, ''), NULLIF(n.middle_name, ''), NULLIF(n.last_name, ''), NULLIF(n.suffix, ''))
               FROM app.identity_names n
               JOIN app.identity_verifications old ON old.id = n.verification_id
               WHERE old.account_id = v.account_id AND old.id <> v.id
                 AND old.status = 'completed' AND old.revoked_at IS NULL
                 AND (old.review_state IS NULL OR old.review_state = 'cleared')
               ORDER BY n.verified_at DESC, n.verification_id
               LIMIT 1
           ), '')
    FROM app.identity_verifications v
    WHERE app.is_admin() AND v.review_state = 'pending' AND v.status = 'completed'
    ORDER BY COALESCE(v.last_event_at, v.updated_at), v.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.decide_identity_review(target_verification uuid, approve boolean, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    reviewed app.identity_verifications%ROWTYPE;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT * INTO reviewed FROM app.identity_verifications v WHERE v.id = target_verification FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF reviewed.account_id = app.current_user_id() THEN
        RETURN 'own';
    END IF;
    IF reviewed.review_state IS DISTINCT FROM 'pending' OR reviewed.status <> 'completed' THEN
        RETURN 'wrong_state';
    END IF;
    UPDATE app.identity_verifications v
    SET review_state = CASE WHEN approve THEN 'cleared' ELSE 'rejected' END,
        reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
    WHERE v.id = target_verification;
    IF approve AND reviewed.review_reasons && ARRAY['name_changed', 'name_unconfirmed'] THEN
        UPDATE app.property_proofs p
        SET status = 'rejected', rejection_reason = 'The name on your ID changed. Send this document again.',
            revoked_at = decided, reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
        WHERE p.host_id = reviewed.account_id AND p.status = 'approved';
    END IF;
    IF NOT approve AND reviewed.review_reasons && ARRAY['under_age', 'duplicate_identity', 'previously_declined'] THEN
        UPDATE app.identity_verifications v
        SET revoked_at = decided, updated_at = decided
        WHERE v.account_id = reviewed.account_id AND v.id <> target_verification
          AND v.status = 'completed' AND v.revoked_at IS NULL;
    END IF;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.listing_verification_problems(valid_days integer, grace_days integer)
RETURNS TABLE (listing_id uuid, host_id uuid, reason text, since timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH standing AS (
        SELECT v.account_id,
               max(COALESCE(v.last_event_at, v.updated_at)) FILTER (
                   WHERE v.status = 'completed' AND v.revoked_at IS NULL
                     AND (v.review_state IS NULL OR v.review_state = 'cleared')
               ) AS verified_at,
               max(v.revoked_at) AS revoked_at
        FROM app.identity_verifications v
        GROUP BY v.account_id
    ),
    problems AS (
        SELECT l.id AS listing_id, l.host_id,
               'Your ID check was withdrawn. Verify your ID to take bookings.' AS reason,
               s.revoked_at AS since, 1 AS priority
        FROM app.listings l
        JOIN standing s ON s.account_id = l.host_id
        WHERE l.status = 'published' AND s.verified_at IS NULL AND s.revoked_at IS NOT NULL
        UNION ALL
        SELECT l.id, l.host_id,
               'Your ID check expired. Verify your ID again to take bookings.',
               s.verified_at + make_interval(days => valid_days + grace_days), 2
        FROM app.listings l
        JOIN standing s ON s.account_id = l.host_id
        WHERE l.status = 'published'
          AND s.verified_at + make_interval(days => valid_days + grace_days) < now()
        UNION ALL
        SELECT l.id, l.host_id,
               'The proof for this property was withdrawn. Send a new document to take bookings.',
               max(p.revoked_at), 3
        FROM app.listings l
        JOIN app.property_proofs p ON p.listing_id = l.id AND p.revoked_at IS NOT NULL
        WHERE l.status = 'published'
          AND NOT EXISTS (
            SELECT 1 FROM app.property_proofs a
            WHERE a.listing_id = l.id AND a.status = 'approved'
              AND a.address_key IS NOT DISTINCT FROM l.address_key
              AND a.unit_key IS NOT DISTINCT FROM l.unit_key
          )
        GROUP BY l.id, l.host_id
    )
    SELECT DISTINCT ON (p.listing_id) p.listing_id, p.host_id, p.reason, p.since
    FROM problems p
    ORDER BY p.listing_id, p.priority
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.apply_verification_pauses(valid_days integer, grace_days integer)
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
CREATE OR REPLACE FUNCTION app.approve_persona_property_proof(target_inquiry text, decided_at timestamptz)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH approved AS (
        UPDATE app.property_proofs p
        SET status = 'approved', reviewed_at = decided_at, updated_at = decided_at
        FROM app.property_proof_inquiries i, app.listings l
        WHERE p.persona_inquiry_id = target_inquiry
          AND i.persona_inquiry_id = p.persona_inquiry_id
          AND l.id = p.listing_id
          AND p.host_id = app.current_user_id()
          AND i.host_id = p.host_id
          AND i.listing_id = p.listing_id
          AND l.host_id = p.host_id
          AND p.status = 'submitted'
          AND i.declined_at IS NULL
          AND i.address_key IS NOT DISTINCT FROM l.address_key
          AND i.unit_key IS NOT DISTINCT FROM l.unit_key
        RETURNING p.id
    )
    SELECT EXISTS (SELECT 1 FROM approved)
$$;
-- +goose StatementEnd

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
CREATE OR REPLACE FUNCTION app.mark_identity_verification_redacted(target_id uuid)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    DELETE FROM app.identity_names n
    USING app.identity_verifications v
    WHERE n.verification_id = target_id AND v.id = target_id AND v.deletion_requested_at IS NOT NULL;

    DELETE FROM app.identity_fingerprints f
    USING app.identity_verifications v
    WHERE f.verification_id = target_id AND v.id = target_id AND v.deletion_requested_at IS NOT NULL;

    UPDATE app.identity_verifications
    SET redacted_at = now(), updated_at = now()
    WHERE id = target_id AND redacted_at IS NULL;

    UPDATE app.property_proof_inquiries
    SET redacted_at = now()
    WHERE id = target_id AND redacted_at IS NULL
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.only_admins_lift_pauses()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.lifted_at IS NOT NULL AND NEW.lifted_at IS DISTINCT FROM OLD.lifted_at AND NOT app.is_admin()
        AND NOT (OLD.paused_by = 'verification' AND current_user = '__MIGRATOR_ROLE__') THEN
        RAISE EXCEPTION 'only the team can lift a pause' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
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

REVOKE ALL ON FUNCTION app.identity_fingerprint_taken(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.identity_fingerprint_taken(bytea) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.decline_persona_property_proof(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.decline_persona_property_proof(text, timestamptz) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.revoke_property_proof(uuid, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.revoke_property_proof(uuid, text, timestamptz) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.pending_identity_reviews(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.pending_identity_reviews(integer) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.decide_identity_review(uuid, boolean, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.decide_identity_review(uuid, boolean, timestamptz) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.listing_verification_problems(integer, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.apply_verification_pauses(integer, integer) FROM PUBLIC;

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.approve_persona_property_proof(target_inquiry text, decided_at timestamptz)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH approved AS (
        UPDATE app.property_proofs p
        SET status = 'approved', reviewed_at = decided_at, updated_at = decided_at
        FROM app.property_proof_inquiries i, app.listings l
        WHERE p.persona_inquiry_id = target_inquiry
          AND i.persona_inquiry_id = p.persona_inquiry_id
          AND l.id = p.listing_id
          AND p.host_id = app.current_user_id()
          AND i.host_id = p.host_id
          AND i.listing_id = p.listing_id
          AND l.host_id = p.host_id
          AND p.status = 'submitted'
          AND i.address_key IS NOT DISTINCT FROM l.address_key
          AND i.unit_key IS NOT DISTINCT FROM l.unit_key
        RETURNING p.id
    )
    SELECT EXISTS (SELECT 1 FROM approved)
$$;
-- +goose StatementEnd

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
CREATE OR REPLACE FUNCTION app.mark_identity_verification_redacted(target_id uuid)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    DELETE FROM app.identity_names n
    USING app.identity_verifications v
    WHERE n.verification_id = target_id AND v.id = target_id AND v.deletion_requested_at IS NOT NULL;

    UPDATE app.identity_verifications
    SET redacted_at = now(), updated_at = now()
    WHERE id = target_id AND redacted_at IS NULL;

    UPDATE app.property_proof_inquiries
    SET redacted_at = now()
    WHERE id = target_id AND redacted_at IS NULL
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.only_admins_lift_pauses()
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
                     'listing_paused', 'listing_unpaused')
      AND (NOT n.for_team OR a.account_id IS NOT NULL)
    ORDER BY n.email_attempts, n.created_at
    LIMIT least(greatest(batch_size, 1), 200)
$$;
-- +goose StatementEnd

DROP FUNCTION app.apply_verification_pauses(integer, integer);
DROP FUNCTION app.listing_verification_problems(integer, integer);
DROP FUNCTION app.decide_identity_review(uuid, boolean, timestamptz);
DROP FUNCTION app.pending_identity_reviews(integer);
DROP FUNCTION app.revoke_property_proof(uuid, text, timestamptz);
DROP FUNCTION app.decline_persona_property_proof(text, timestamptz);
DROP FUNCTION app.identity_fingerprint_taken(bytea);

CREATE POLICY notifications_migrator_delete ON app.notifications
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (booking_id IS NULL AND listing_id IS NULL);
DELETE FROM app.notifications WHERE booking_id IS NULL AND listing_id IS NULL;
DROP POLICY notifications_migrator_delete ON app.notifications;
ALTER TABLE app.notifications
    DROP CONSTRAINT notifications_subject,
    ADD CONSTRAINT notifications_subject CHECK (booking_id IS NOT NULL OR listing_id IS NOT NULL);

DROP POLICY listing_suspensions_migrator_update ON app.listing_suspensions;
DROP POLICY listing_suspensions_migrator_insert ON app.listing_suspensions;
DROP POLICY listing_suspensions_host_replaces_verification ON app.listing_suspensions;
CREATE POLICY listing_suspensions_migrator_delete ON app.listing_suspensions
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (paused_by = 'verification');
DELETE FROM app.listing_suspensions WHERE paused_by = 'verification';
DROP POLICY listing_suspensions_migrator_delete ON app.listing_suspensions;
ALTER TABLE app.listing_suspensions
    DROP CONSTRAINT listing_suspensions_paused_by_check,
    ADD CONSTRAINT listing_suspensions_paused_by_check CHECK (paused_by IN ('host_cancellations', 'team')),
    DROP CONSTRAINT listing_suspensions_lift_shape,
    ADD CONSTRAINT listing_suspensions_lift_shape CHECK ((lifted_at IS NULL) = (lifted_by IS NULL));

ALTER TABLE app.property_proof_inquiries DROP COLUMN declined_at;
ALTER TABLE app.property_proofs DROP COLUMN revoked_at;
DROP TABLE app.identity_fingerprints;
DROP INDEX app.identity_verifications_pending_review_idx;
ALTER TABLE app.identity_verifications
    DROP COLUMN revoked_at,
    DROP COLUMN reviewed_at,
    DROP COLUMN reviewed_by,
    DROP COLUMN review_reasons,
    DROP COLUMN review_state;
