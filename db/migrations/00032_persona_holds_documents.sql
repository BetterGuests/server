-- +goose Up

ALTER TABLE app.property_proof_inquiries
    ADD COLUMN redacted_at timestamptz,
    ADD COLUMN deletion_requested_at timestamptz;

CREATE INDEX property_proof_inquiries_unredacted_idx
    ON app.property_proof_inquiries (created_at) WHERE redacted_at IS NULL;

CREATE POLICY property_proof_inquiries_migrator_update ON app.property_proof_inquiries
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

ALTER TABLE app.property_proofs
    DROP COLUMN extra_object_keys,
    ALTER COLUMN object_key DROP NOT NULL,
    ALTER COLUMN content_type DROP NOT NULL,
    ALTER COLUMN size_bytes DROP NOT NULL,
    ADD CONSTRAINT property_proofs_file_or_inquiry CHECK (
        (object_key IS NULL) = (content_type IS NULL)
        AND (object_key IS NULL) = (size_bytes IS NULL)
        AND (object_key IS NOT NULL OR persona_inquiry_id IS NOT NULL)
    );

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
    SELECT v.id, v.account_id, v.persona_inquiry_id
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
    ORDER BY COALESCE(v.deletion_requested_at, v.last_event_at, v.created_at)
    LIMIT least(greatest(batch_size, 1), 500)
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
    )
    SELECT f.id, f.persona_inquiry_id,
        EXISTS (
            SELECT 1 FROM app.identity_verification_holds h
            WHERE h.verification_id = f.id AND h.released_at IS NULL
        )
    FROM flagged f
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
    WHERE id = target_id AND redacted_at IS NULL
$$;
-- +goose StatementEnd

ALTER TABLE app.property_proofs
    DROP CONSTRAINT property_proofs_file_or_inquiry,
    ALTER COLUMN object_key SET NOT NULL,
    ALTER COLUMN content_type SET NOT NULL,
    ALTER COLUMN size_bytes SET NOT NULL,
    ADD COLUMN extra_object_keys text[] NOT NULL DEFAULT '{}' CHECK (cardinality(extra_object_keys) <= 9);

DROP POLICY property_proof_inquiries_migrator_update ON app.property_proof_inquiries;
DROP INDEX app.property_proof_inquiries_unredacted_idx;
ALTER TABLE app.property_proof_inquiries DROP COLUMN redacted_at, DROP COLUMN deletion_requested_at;
