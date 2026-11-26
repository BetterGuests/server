-- +goose Up

ALTER TABLE app.property_proofs ADD COLUMN file_deleted_at timestamptz;

CREATE INDEX property_proofs_stored_files_idx
    ON app.property_proofs (status)
    WHERE object_key IS NOT NULL AND file_deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION app.property_proof_files_due_for_deletion(batch_size integer)
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
CREATE FUNCTION app.mark_property_proof_file_deleted(target_id uuid)
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

REVOKE ALL ON FUNCTION app.property_proof_files_due_for_deletion(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.property_proof_files_due_for_deletion(integer) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.mark_property_proof_file_deleted(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.mark_property_proof_file_deleted(uuid) TO __APP_ROLE__;

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

-- +goose Down

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
            p.status = 'rejected'
            OR (p.status = 'approved' AND p.reviewed_at < now() - make_interval(days => 90))
            OR (p.status IS DISTINCT FROM 'approved' AND i.deletion_requested_at IS NOT NULL)
            OR (p.id IS NULL AND i.created_at < now() - make_interval(days => greatest(retention_days, 1)))
          )
    ) due
    ORDER BY due.due_since
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

DROP FUNCTION app.mark_property_proof_file_deleted(uuid);
DROP FUNCTION app.property_proof_files_due_for_deletion(integer);
DROP INDEX app.property_proofs_stored_files_idx;
ALTER TABLE app.property_proofs DROP COLUMN file_deleted_at;
