-- +goose Up

ALTER TABLE app.property_proofs
    ADD COLUMN relationship text NOT NULL DEFAULT 'owner'
        CHECK (relationship IN ('owner', 'company_owner', 'property_manager', 'renter')),
    ADD COLUMN owner_name text CHECK (owner_name IS NULL OR length(owner_name) BETWEEN 1 AND 200),
    ADD COLUMN owner_contact text CHECK (owner_contact IS NULL OR length(owner_contact) BETWEEN 3 AND 254),
    ADD COLUMN authority_expires_on date,
    ADD CONSTRAINT property_proofs_permission_expires CHECK (
        relationship NOT IN ('property_manager', 'renter') OR authority_expires_on IS NOT NULL
    );

CREATE INDEX property_proofs_expiring_idx
    ON app.property_proofs (authority_expires_on) WHERE status = 'approved';

CREATE TABLE app.property_proof_attachments (
    id               uuid PRIMARY KEY,
    proof_id         uuid NOT NULL REFERENCES app.property_proofs(id) ON DELETE CASCADE,
    host_id          uuid NOT NULL REFERENCES app.accounts(id),
    kind             text NOT NULL CHECK (kind IN ('company_registration', 'signing_authorization', 'management_agreement', 'landlord_permission')),
    object_key       text NOT NULL UNIQUE CHECK (length(object_key) BETWEEN 1 AND 300),
    content_type     text NOT NULL CHECK (content_type IN ('application/pdf', 'image/jpeg', 'image/png', 'image/webp', 'image/heic')),
    size_bytes       bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 15728640),
    file_deleted_at  timestamptz,
    UNIQUE (proof_id, kind)
);

CREATE INDEX property_proof_attachments_host_id_idx ON app.property_proof_attachments (host_id);

ALTER TABLE app.property_proof_attachments ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.property_proof_attachments FORCE ROW LEVEL SECURITY;

CREATE POLICY property_proof_attachments_select ON app.property_proof_attachments
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id() OR app.is_admin());

CREATE POLICY property_proof_attachments_insert ON app.property_proof_attachments
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.property_proofs p
            WHERE p.id = property_proof_attachments.proof_id
              AND p.host_id = app.current_user_id()
              AND p.status = 'submitted'
        )
    );

CREATE POLICY property_proof_attachments_migrator_select ON app.property_proof_attachments
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY property_proof_attachments_migrator_update ON app.property_proof_attachments
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

GRANT SELECT, INSERT ON app.property_proof_attachments TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.expire_property_authority(today date)
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH expired AS (
        UPDATE app.property_proofs p
        SET status = 'rejected',
            rejection_reason = 'Your permission to list this home ended. Send a current document.',
            revoked_at = now(), reviewed_at = now(), updated_at = now()
        WHERE p.status = 'approved' AND p.authority_expires_on < today
        RETURNING p.id
    )
    SELECT count(*)::integer FROM expired
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.expire_property_authority(date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.expire_property_authority(date) TO __APP_ROLE__;

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.release_draft_listing_documents(target_listing uuid)
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
    INSERT INTO app.released_documents (id, host_id, object_key)
    SELECT a.id, a.host_id, a.object_key
    FROM app.property_proof_attachments a
    JOIN app.property_proofs p ON p.id = a.proof_id
    WHERE p.listing_id = target_listing AND a.file_deleted_at IS NULL;
    DELETE FROM app.property_proofs p WHERE p.listing_id = target_listing;
    RETURN true;
END
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
CREATE OR REPLACE FUNCTION app.release_draft_listing_documents(target_listing uuid)
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

DROP FUNCTION app.expire_property_authority(date);
DROP TABLE app.property_proof_attachments;
DROP INDEX app.property_proofs_expiring_idx;
ALTER TABLE app.property_proofs
    DROP CONSTRAINT property_proofs_permission_expires,
    DROP COLUMN relationship,
    DROP COLUMN owner_name,
    DROP COLUMN owner_contact,
    DROP COLUMN authority_expires_on;
