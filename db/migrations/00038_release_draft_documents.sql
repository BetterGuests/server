-- +goose Up

CREATE TABLE app.released_documents (
    id                  uuid PRIMARY KEY,
    host_id             uuid NOT NULL REFERENCES app.accounts(id),
    object_key          text CHECK (length(object_key) BETWEEN 1 AND 300),
    persona_inquiry_id  text CHECK (length(persona_inquiry_id) BETWEEN 5 AND 100),
    released_at         timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,
    CHECK ((object_key IS NULL) <> (persona_inquiry_id IS NULL))
);

CREATE INDEX released_documents_pending_idx ON app.released_documents (released_at) WHERE deleted_at IS NULL;
CREATE INDEX released_documents_host_id_idx ON app.released_documents (host_id);

ALTER TABLE app.released_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.released_documents FORCE ROW LEVEL SECURITY;

CREATE POLICY released_documents_app_denied ON app.released_documents
    FOR SELECT TO __APP_ROLE__
    USING (false);

CREATE POLICY released_documents_migrator_select ON app.released_documents
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY released_documents_migrator_insert ON app.released_documents
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY released_documents_migrator_update ON app.released_documents
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE POLICY property_proofs_migrator_delete ON app.property_proofs
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.release_draft_listing_documents(target_listing uuid)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM 1 FROM app.listings l
    WHERE l.id = target_listing AND l.host_id = app.current_user_id() AND l.status = 'draft'
    FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF EXISTS (
        SELECT 1 FROM app.property_proofs p
        WHERE p.listing_id = target_listing AND p.revoked_at >= now() - make_interval(days => 90)
    ) THEN
        RETURN false;
    END IF;
    INSERT INTO app.released_documents (id, host_id, object_key)
    SELECT p.id, p.host_id, p.object_key
    FROM app.property_proofs p
    WHERE p.listing_id = target_listing AND p.object_key IS NOT NULL AND p.file_deleted_at IS NULL;
    INSERT INTO app.released_documents (id, host_id, persona_inquiry_id)
    SELECT i.id, i.host_id, i.persona_inquiry_id
    FROM app.property_proof_inquiries i
    WHERE i.listing_id = target_listing AND i.redacted_at IS NULL;
    DELETE FROM app.property_proofs p WHERE p.listing_id = target_listing;
    RETURN true;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.release_draft_listing_documents(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.release_draft_listing_documents(uuid) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.property_proof_files_due_for_deletion(batch_size integer)
RETURNS TABLE (id uuid, object_key text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT p.id, p.object_key
    FROM app.property_proofs p
    WHERE p.object_key IS NOT NULL
      AND p.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL)
        OR p.revoked_at < now() - make_interval(days => 90)
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      )
    UNION ALL
    SELECT r.id, r.object_key
    FROM app.released_documents r
    WHERE r.object_key IS NOT NULL AND r.deleted_at IS NULL
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
    SET file_deleted_at = now()
    WHERE p.id = target_id
      AND p.file_deleted_at IS NULL
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.identity_verifications_due_for_redaction(
    retention_days integer,
    hold_cap_days integer,
    batch_size integer
)
RETURNS TABLE (id uuid, account_id uuid, persona_inquiry_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT due.id, due.account_id, due.persona_inquiry_id
    FROM (
        SELECT v.id, v.account_id, v.persona_inquiry_id,
            COALESCE(v.deletion_requested_at, v.last_event_at, v.created_at) AS due_since
        FROM app.identity_verifications v
        WHERE v.redacted_at IS NULL
          AND (
            (
              (
                COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(retention_days, 1))
                OR v.deletion_requested_at IS NOT NULL
              )
              AND NOT EXISTS (
                SELECT 1 FROM app.identity_verification_holds h
                WHERE h.verification_id = v.id AND h.released_at IS NULL
              )
            )
            OR COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(hold_cap_days, 1))
          )
        UNION ALL
        SELECT i.id, i.host_id, i.persona_inquiry_id,
            COALESCE(i.deletion_requested_at, p.reviewed_at, i.created_at)
        FROM app.property_proof_inquiries i
        LEFT JOIN app.property_proofs p ON p.persona_inquiry_id = i.persona_inquiry_id
        WHERE i.redacted_at IS NULL
          AND (
            (p.status = 'rejected' AND (p.revoked_at IS NULL OR p.revoked_at < now() - make_interval(days => 90)))
            OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
            OR (p.status IS DISTINCT FROM 'approved' AND i.deletion_requested_at IS NOT NULL)
            OR (p.id IS NULL AND i.created_at < now() - make_interval(days => greatest(retention_days, 1)))
          )
        UNION ALL
        SELECT r.id, r.host_id, r.persona_inquiry_id, r.released_at
        FROM app.released_documents r
        WHERE r.persona_inquiry_id IS NOT NULL AND r.deleted_at IS NULL
    ) due
    ORDER BY due.due_since
    LIMIT least(greatest(batch_size, 1), 500)
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
    WHERE id = target_id AND redacted_at IS NULL;

    UPDATE app.released_documents
    SET deleted_at = now()
    WHERE id = target_id AND persona_inquiry_id IS NOT NULL AND deleted_at IS NULL
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.property_proof_files_due_for_deletion(batch_size integer)
RETURNS TABLE (id uuid, object_key text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT p.id, p.object_key
    FROM app.property_proofs p
    WHERE p.object_key IS NOT NULL
      AND p.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL)
        OR p.revoked_at < now() - make_interval(days => 90)
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      )
    ORDER BY p.updated_at, p.id
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
    SET file_deleted_at = now()
    WHERE p.id = target_id
      AND p.file_deleted_at IS NULL
      AND (
        (p.status = 'rejected' AND p.revoked_at IS NULL)
        OR p.revoked_at < now() - make_interval(days => 90)
        OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
      )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.identity_verifications_due_for_redaction(
    retention_days integer,
    hold_cap_days integer,
    batch_size integer
)
RETURNS TABLE (id uuid, account_id uuid, persona_inquiry_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT due.id, due.account_id, due.persona_inquiry_id
    FROM (
        SELECT v.id, v.account_id, v.persona_inquiry_id,
            COALESCE(v.deletion_requested_at, v.last_event_at, v.created_at) AS due_since
        FROM app.identity_verifications v
        WHERE v.redacted_at IS NULL
          AND (
            (
              (
                COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(retention_days, 1))
                OR v.deletion_requested_at IS NOT NULL
              )
              AND NOT EXISTS (
                SELECT 1 FROM app.identity_verification_holds h
                WHERE h.verification_id = v.id AND h.released_at IS NULL
              )
            )
            OR COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(hold_cap_days, 1))
          )
        UNION ALL
        SELECT i.id, i.host_id, i.persona_inquiry_id,
            COALESCE(i.deletion_requested_at, p.reviewed_at, i.created_at)
        FROM app.property_proof_inquiries i
        LEFT JOIN app.property_proofs p ON p.persona_inquiry_id = i.persona_inquiry_id
        WHERE i.redacted_at IS NULL
          AND (
            (p.status = 'rejected' AND (p.revoked_at IS NULL OR p.revoked_at < now() - make_interval(days => 90)))
            OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
            OR (p.status IS DISTINCT FROM 'approved' AND i.deletion_requested_at IS NOT NULL)
            OR (p.id IS NULL AND i.created_at < now() - make_interval(days => greatest(retention_days, 1)))
          )
    ) due
    ORDER BY due.due_since
    LIMIT least(greatest(batch_size, 1), 500)
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

DROP FUNCTION app.release_draft_listing_documents(uuid);
DROP POLICY property_proofs_migrator_delete ON app.property_proofs;
DROP TABLE app.released_documents;
