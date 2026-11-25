-- +goose Up

ALTER TABLE app.identity_verifications ADD COLUMN redacted_at timestamptz;

CREATE INDEX identity_verifications_redaction_due_idx
    ON app.identity_verifications ((COALESCE(last_event_at, created_at)))
    WHERE redacted_at IS NULL;

CREATE POLICY identity_verifications_migrator_select ON app.identity_verifications
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY identity_verifications_migrator_update ON app.identity_verifications
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE TABLE app.identity_verification_holds (
    id               uuid PRIMARY KEY,
    verification_id  uuid NOT NULL REFERENCES app.identity_verifications(id),
    case_reference   text NOT NULL CHECK (length(case_reference) BETWEEN 1 AND 200),
    approved_by      text NOT NULL CHECK (length(approved_by) BETWEEN 1 AND 200),
    placed_at        timestamptz NOT NULL DEFAULT now(),
    released_at      timestamptz,
    released_by      text CHECK (released_by IS NULL OR length(released_by) BETWEEN 1 AND 200),
    CHECK ((released_at IS NULL) = (released_by IS NULL))
);

CREATE INDEX identity_verification_holds_verification_id_idx
    ON app.identity_verification_holds (verification_id);

CREATE UNIQUE INDEX identity_verification_holds_one_active_idx
    ON app.identity_verification_holds (verification_id)
    WHERE released_at IS NULL;

ALTER TABLE app.identity_verification_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.identity_verification_holds FORCE ROW LEVEL SECURITY;

CREATE POLICY identity_verification_holds_app_denied ON app.identity_verification_holds
    FOR SELECT TO __APP_ROLE__
    USING (false);

CREATE POLICY identity_verification_holds_migrator_select ON app.identity_verification_holds
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY identity_verification_holds_migrator_insert ON app.identity_verification_holds
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY identity_verification_holds_migrator_update ON app.identity_verification_holds
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.identity_verifications_due_for_redaction(
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

REVOKE ALL ON FUNCTION app.identity_verifications_due_for_redaction(integer, integer, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.identity_verifications_due_for_redaction(integer, integer, integer) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.mark_identity_verification_redacted(target_id uuid)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.identity_verifications
    SET redacted_at = now(), updated_at = now()
    WHERE id = target_id AND redacted_at IS NULL
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.mark_identity_verification_redacted(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.mark_identity_verification_redacted(uuid) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.mark_identity_verification_redacted(uuid);
DROP FUNCTION app.identity_verifications_due_for_redaction(integer, integer, integer);
DROP TABLE app.identity_verification_holds;
DROP POLICY identity_verifications_migrator_update ON app.identity_verifications;
DROP POLICY identity_verifications_migrator_select ON app.identity_verifications;
DROP INDEX app.identity_verifications_redaction_due_idx;
ALTER TABLE app.identity_verifications DROP COLUMN redacted_at;
