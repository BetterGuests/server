-- +goose Up

ALTER TABLE app.accounts
    ADD COLUMN banner_key text
    CHECK (banner_key IS NULL OR length(banner_key) BETWEEN 1 AND 255);

CREATE POLICY accounts_update ON app.accounts
    FOR UPDATE TO __APP_ROLE__
    USING (id = app.current_user_id())
    WITH CHECK (id = app.current_user_id());

GRANT UPDATE (banner_key) ON app.accounts TO __APP_ROLE__;

-- +goose Down

REVOKE UPDATE (banner_key) ON app.accounts FROM __APP_ROLE__;

DROP POLICY IF EXISTS accounts_update ON app.accounts;

ALTER TABLE app.accounts DROP COLUMN IF EXISTS banner_key;
