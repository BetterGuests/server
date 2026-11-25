-- +goose Up

CREATE TABLE app.identity_names (
    verification_id  uuid PRIMARY KEY REFERENCES app.identity_verifications(id),
    account_id       uuid NOT NULL REFERENCES app.accounts(id),
    first_name       text NOT NULL CHECK (length(first_name) <= 200),
    middle_name      text NOT NULL CHECK (length(middle_name) <= 200),
    last_name        text NOT NULL CHECK (length(last_name) <= 200),
    suffix           text NOT NULL CHECK (length(suffix) <= 200),
    verified_at      timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (first_name <> '' OR last_name <> '')
);

CREATE INDEX identity_names_account_verified_idx ON app.identity_names (account_id, verified_at DESC);

ALTER TABLE app.identity_names ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.identity_names FORCE ROW LEVEL SECURITY;

CREATE POLICY identity_names_select ON app.identity_names
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id() OR app.is_admin());

CREATE POLICY identity_names_insert ON app.identity_names
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        account_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.identity_verifications v
            WHERE v.id = identity_names.verification_id AND v.account_id = app.current_user_id()
        )
    );

CREATE POLICY identity_names_update ON app.identity_names
    FOR UPDATE TO __APP_ROLE__
    USING (account_id = app.current_user_id())
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY identity_names_delete ON app.identity_names
    FOR DELETE TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY identity_names_migrator_select ON app.identity_names
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY identity_names_migrator_delete ON app.identity_names
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT, UPDATE, DELETE ON app.identity_names TO __APP_ROLE__;

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

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.mark_identity_verification_redacted(target_id uuid)
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_identity_data_deletion()
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

DROP TABLE app.identity_names;
