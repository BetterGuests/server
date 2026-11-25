-- +goose Up

ALTER TABLE app.identity_verifications ADD COLUMN deletion_requested_at timestamptz;

CREATE INDEX identity_verifications_deletion_requested_idx
    ON app.identity_verifications (deletion_requested_at)
    WHERE redacted_at IS NULL AND deletion_requested_at IS NOT NULL;

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
CREATE FUNCTION app.request_identity_data_deletion()
RETURNS TABLE (id uuid, persona_inquiry_id text, held boolean)
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
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

REVOKE ALL ON FUNCTION app.request_identity_data_deletion() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.request_identity_data_deletion() TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.request_identity_data_deletion();

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
          COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(retention_days, 1))
          AND NOT EXISTS (
            SELECT 1 FROM app.identity_verification_holds h
            WHERE h.verification_id = v.id AND h.released_at IS NULL
          )
        )
        OR COALESCE(v.last_event_at, v.created_at) < now() - make_interval(days => greatest(hold_cap_days, 1))
      )
    ORDER BY COALESCE(v.last_event_at, v.created_at)
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

DROP INDEX app.identity_verifications_deletion_requested_idx;
ALTER TABLE app.identity_verifications DROP COLUMN deletion_requested_at;
