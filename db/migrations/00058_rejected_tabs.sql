-- +goose Up

ALTER TABLE app.property_proofs
    ADD COLUMN file_erase_requested_at timestamptz,
    ADD COLUMN overturned_at timestamptz,
    ADD COLUMN overturn_note text CHECK (overturn_note IS NULL OR length(overturn_note) BETWEEN 1 AND 1000),
    ADD CONSTRAINT property_proofs_overturn_recorded CHECK ((overturned_at IS NULL) = (overturn_note IS NULL));

ALTER TABLE app.identity_verifications
    ADD COLUMN overturned_at timestamptz,
    ADD COLUMN overturn_note text CHECK (overturn_note IS NULL OR length(overturn_note) BETWEEN 1 AND 1000),
    ADD CONSTRAINT identity_verifications_overturn_recorded CHECK ((overturned_at IS NULL) = (overturn_note IS NULL));

CREATE INDEX identity_verifications_rejected_review_idx
    ON app.identity_verifications (reviewed_at DESC) WHERE review_state = 'rejected';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.property_proof_files_due_for_deletion(batch_size integer)
RETURNS TABLE (id uuid, object_key text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH finished AS (
        SELECT p.id, p.object_key, p.file_deleted_at, p.updated_at
        FROM app.property_proofs p
        WHERE (p.status = 'rejected' AND p.revoked_at IS NULL AND (p.reviewed_at < now() - make_interval(days => 30) OR p.file_erase_requested_at IS NOT NULL))
           OR (p.revoked_at < now() - make_interval(days => 90) OR (p.revoked_at IS NOT NULL AND p.file_erase_requested_at IS NOT NULL))
           OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
    )
    SELECT due.id, due.object_key
    FROM (
        SELECT f.id, f.object_key, f.updated_at AS due_since
        FROM finished f
        WHERE f.object_key IS NOT NULL AND f.file_deleted_at IS NULL
        UNION ALL
        SELECT a.id, a.object_key, f.updated_at
        FROM app.property_proof_attachments a
        JOIN finished f ON f.id = a.proof_id
        WHERE a.file_deleted_at IS NULL
        UNION ALL
        SELECT r.id, r.object_key, r.released_at
        FROM app.released_documents r
        WHERE r.object_key IS NOT NULL AND r.deleted_at IS NULL
    ) due
    ORDER BY due.due_since, due.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.mark_property_proof_file_deleted(target_id uuid)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.property_proofs p
    SET file_deleted_at = now(), owner_name = NULL, owner_contact = NULL
    WHERE p.id = target_id
      AND p.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL AND (p.reviewed_at < now() - make_interval(days => 30) OR p.file_erase_requested_at IS NOT NULL))
        OR (p.revoked_at < now() - make_interval(days => 90) OR (p.revoked_at IS NOT NULL AND p.file_erase_requested_at IS NOT NULL))
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      );

    UPDATE app.property_proof_attachments a
    SET file_deleted_at = now()
    FROM app.property_proofs p
    WHERE a.id = target_id
      AND p.id = a.proof_id
      AND a.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL AND (p.reviewed_at < now() - make_interval(days => 30) OR p.file_erase_requested_at IS NOT NULL))
        OR (p.revoked_at < now() - make_interval(days => 90) OR (p.revoked_at IS NOT NULL AND p.file_erase_requested_at IS NOT NULL))
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      );

    UPDATE app.released_documents
    SET deleted_at = now()
    WHERE id = target_id AND object_key IS NOT NULL AND deleted_at IS NULL
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.identity_review_inquiries(target_verification uuid)
RETURNS TABLE (role text, persona_inquiry_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH target AS (
        SELECT v.id, v.account_id, v.persona_inquiry_id
        FROM app.identity_verifications v
        WHERE app.is_admin() AND v.id = target_verification
          AND v.review_state IN ('pending', 'rejected') AND v.status = 'completed'
          AND v.deletion_requested_at IS NULL AND v.redacted_at IS NULL
    )
    SELECT 'this', t.persona_inquiry_id FROM target t
    UNION ALL
    (SELECT 'previous', old.persona_inquiry_id
     FROM target t
     JOIN app.identity_verifications old ON old.account_id = t.account_id AND old.id <> t.id
     WHERE old.status = 'completed' AND old.redacted_at IS NULL AND old.deletion_requested_at IS NULL
     ORDER BY COALESCE(old.last_event_at, old.updated_at) DESC, old.id
     LIMIT 1)
    UNION ALL
    (SELECT 'other_account', other.persona_inquiry_id
     FROM target t
     JOIN app.identity_fingerprints mine ON mine.verification_id = t.id
     JOIN app.identity_fingerprints theirs ON theirs.fingerprint = mine.fingerprint AND theirs.account_id <> t.account_id
     JOIN app.identity_verifications other ON other.id = theirs.verification_id
     WHERE other.redacted_at IS NULL AND other.deletion_requested_at IS NULL
     ORDER BY COALESCE(other.last_event_at, other.updated_at) DESC, other.id
     LIMIT 1)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.rejected_identity_reviews(batch_size integer)
RETURNS TABLE (
    verification_id uuid,
    account_id uuid,
    persona_inquiry_id text,
    reasons text[],
    completed_at timestamptz,
    new_name text,
    previous_name text,
    rejected_at timestamptz,
    automatic boolean,
    erased boolean
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
           ), ''),
           COALESCE(v.reviewed_at, v.updated_at),
           v.reviewed_by IS NULL,
           v.deletion_requested_at IS NOT NULL OR v.redacted_at IS NOT NULL
    FROM app.identity_verifications v
    WHERE app.is_admin() AND v.review_state = 'rejected' AND v.status = 'completed'
    ORDER BY COALESCE(v.reviewed_at, v.updated_at) DESC, v.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.rejected_identity_reviews(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.rejected_identity_reviews(integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.overturn_identity_review(target_verification uuid, note text, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    rejected app.identity_verifications%ROWTYPE;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT * INTO rejected FROM app.identity_verifications v WHERE v.id = target_verification FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF rejected.account_id = app.current_user_id() THEN
        RETURN 'own';
    END IF;
    IF rejected.review_state IS DISTINCT FROM 'rejected' OR rejected.status <> 'completed' OR rejected.revoked_at IS NOT NULL THEN
        RETURN 'wrong_state';
    END IF;
    IF rejected.deletion_requested_at IS NOT NULL OR rejected.redacted_at IS NOT NULL THEN
        RETURN 'erased';
    END IF;
    IF EXISTS (
        SELECT 1 FROM app.identity_verifications newer
        WHERE newer.account_id = rejected.account_id AND newer.id <> rejected.id
          AND newer.status = 'completed'
          AND COALESCE(newer.last_event_at, newer.updated_at) > COALESCE(rejected.last_event_at, rejected.updated_at)
    ) THEN
        RETURN 'superseded';
    END IF;
    UPDATE app.identity_verifications v
    SET review_state = 'cleared', reviewed_by = app.current_user_id(), reviewed_at = decided,
        overturned_at = decided, overturn_note = note, updated_at = decided
    WHERE v.id = rejected.id;
    IF rejected.review_reasons && ARRAY['name_changed', 'name_unconfirmed'] THEN
        UPDATE app.property_proofs p
        SET status = 'rejected', rejection_reason = 'The name on your ID changed. Send this document again.',
            revoked_at = decided, reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
        WHERE p.host_id = rejected.account_id AND p.status = 'approved';
    END IF;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.overturn_identity_review(uuid, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.overturn_identity_review(uuid, text, timestamptz) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.erase_rejected_identity(target_verification uuid, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    rejected app.identity_verifications%ROWTYPE;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT * INTO rejected FROM app.identity_verifications v WHERE v.id = target_verification FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF rejected.review_state IS DISTINCT FROM 'rejected' THEN
        RETURN 'wrong_state';
    END IF;
    UPDATE app.identity_verifications v
    SET deletion_requested_at = COALESCE(v.deletion_requested_at, decided), updated_at = decided
    WHERE v.id = rejected.id;
    IF NOT EXISTS (
        SELECT 1 FROM app.identity_verification_holds h
        WHERE h.verification_id = rejected.id AND h.released_at IS NULL
    ) THEN
        DELETE FROM app.identity_names n WHERE n.verification_id = rejected.id;
        DELETE FROM app.identity_fingerprints f WHERE f.verification_id = rejected.id;
    END IF;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.erase_rejected_identity(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.erase_rejected_identity(uuid, timestamptz) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.overturn_property_proof(target_proof uuid, note text, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    rejected app.property_proofs%ROWTYPE;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT * INTO rejected FROM app.property_proofs p WHERE p.id = target_proof FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF rejected.status <> 'rejected' THEN
        RETURN 'wrong_state';
    END IF;
    IF rejected.object_key IS NULL OR rejected.file_deleted_at IS NOT NULL OR rejected.file_erase_requested_at IS NOT NULL THEN
        RETURN 'erased';
    END IF;
    IF rejected.authority_expires_on IS NOT NULL AND rejected.authority_expires_on < (decided AT TIME ZONE 'UTC')::date THEN
        RETURN 'expired';
    END IF;
    IF EXISTS (
        SELECT 1 FROM app.property_proofs newer
        WHERE newer.listing_id = rejected.listing_id AND newer.id <> rejected.id
          AND (newer.created_at > rejected.created_at OR newer.status IN ('submitted', 'approved'))
    ) THEN
        RETURN 'superseded';
    END IF;
    UPDATE app.property_proofs p
    SET status = 'approved', rejection_reason = NULL, revoked_at = NULL, auto_approved_at = NULL,
        reviewed_by = app.current_user_id(), reviewed_at = decided,
        overturned_at = decided, overturn_note = note, updated_at = decided
    WHERE p.id = rejected.id;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.overturn_property_proof(uuid, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.overturn_property_proof(uuid, text, timestamptz) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.erase_rejected_property_proof(target_proof uuid, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.property_proofs p WHERE p.id = target_proof) THEN
        RETURN 'not_found';
    END IF;
    UPDATE app.property_proofs p
    SET file_erase_requested_at = COALESCE(p.file_erase_requested_at, decided), updated_at = decided
    WHERE p.id = target_proof AND p.status = 'rejected';
    IF NOT FOUND THEN
        RETURN 'wrong_state';
    END IF;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.erase_rejected_property_proof(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.erase_rejected_property_proof(uuid, timestamptz) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.erase_rejected_property_proof(uuid, timestamptz);
DROP FUNCTION app.overturn_property_proof(uuid, text, timestamptz);
DROP FUNCTION app.erase_rejected_identity(uuid, timestamptz);
DROP FUNCTION app.overturn_identity_review(uuid, text, timestamptz);
DROP FUNCTION app.rejected_identity_reviews(integer);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.identity_review_inquiries(target_verification uuid)
RETURNS TABLE (role text, persona_inquiry_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH target AS (
        SELECT v.id, v.account_id, v.persona_inquiry_id
        FROM app.identity_verifications v
        WHERE app.is_admin() AND v.id = target_verification
          AND v.review_state = 'pending' AND v.status = 'completed'
    )
    SELECT 'this', t.persona_inquiry_id FROM target t
    UNION ALL
    (SELECT 'previous', old.persona_inquiry_id
     FROM target t
     JOIN app.identity_verifications old ON old.account_id = t.account_id AND old.id <> t.id
     WHERE old.status = 'completed' AND old.redacted_at IS NULL AND old.deletion_requested_at IS NULL
     ORDER BY COALESCE(old.last_event_at, old.updated_at) DESC, old.id
     LIMIT 1)
    UNION ALL
    (SELECT 'other_account', other.persona_inquiry_id
     FROM target t
     JOIN app.identity_fingerprints mine ON mine.verification_id = t.id
     JOIN app.identity_fingerprints theirs ON theirs.fingerprint = mine.fingerprint AND theirs.account_id <> t.account_id
     JOIN app.identity_verifications other ON other.id = theirs.verification_id
     WHERE other.redacted_at IS NULL AND other.deletion_requested_at IS NULL
     ORDER BY COALESCE(other.last_event_at, other.updated_at) DESC, other.id
     LIMIT 1)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.property_proof_files_due_for_deletion(batch_size integer)
RETURNS TABLE (id uuid, object_key text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH finished AS (
        SELECT p.id, p.object_key, p.file_deleted_at, p.updated_at
        FROM app.property_proofs p
        WHERE (p.status = 'rejected' AND p.revoked_at IS NULL)
           OR p.revoked_at < now() - make_interval(days => 90)
           OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
    )
    SELECT due.id, due.object_key
    FROM (
        SELECT f.id, f.object_key, f.updated_at AS due_since
        FROM finished f
        WHERE f.object_key IS NOT NULL AND f.file_deleted_at IS NULL
        UNION ALL
        SELECT a.id, a.object_key, f.updated_at
        FROM app.property_proof_attachments a
        JOIN finished f ON f.id = a.proof_id
        WHERE a.file_deleted_at IS NULL
        UNION ALL
        SELECT r.id, r.object_key, r.released_at
        FROM app.released_documents r
        WHERE r.object_key IS NOT NULL AND r.deleted_at IS NULL
    ) due
    ORDER BY due.due_since, due.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.mark_property_proof_file_deleted(target_id uuid)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.property_proofs p
    SET file_deleted_at = now(), owner_name = NULL, owner_contact = NULL
    WHERE p.id = target_id
      AND p.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL)
        OR p.revoked_at < now() - make_interval(days => 90)
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      );

    UPDATE app.property_proof_attachments a
    SET file_deleted_at = now()
    FROM app.property_proofs p
    WHERE a.id = target_id
      AND p.id = a.proof_id
      AND a.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL)
        OR p.revoked_at < now() - make_interval(days => 90)
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      );

    UPDATE app.released_documents
    SET deleted_at = now()
    WHERE id = target_id AND object_key IS NOT NULL AND deleted_at IS NULL
$$;
-- +goose StatementEnd

DROP INDEX app.identity_verifications_rejected_review_idx;

ALTER TABLE app.identity_verifications
    DROP CONSTRAINT identity_verifications_overturn_recorded,
    DROP COLUMN overturn_note,
    DROP COLUMN overturned_at;

ALTER TABLE app.property_proofs
    DROP CONSTRAINT property_proofs_overturn_recorded,
    DROP COLUMN overturn_note,
    DROP COLUMN overturned_at,
    DROP COLUMN file_erase_requested_at;
