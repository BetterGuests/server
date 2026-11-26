-- +goose Up

ALTER TABLE app.account_names
    ADD COLUMN avatar_url text CHECK (avatar_url IS NULL OR (avatar_url ~ '^https://' AND length(avatar_url) <= 2000));

CREATE POLICY account_names_host_contact_select ON app.account_names
    FOR SELECT TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.conversations c
            WHERE c.host_id = account_names.account_id AND c.guest_id = app.current_user_id()
        )
    );

-- +goose Down

DROP POLICY account_names_host_contact_select ON app.account_names;
ALTER TABLE app.account_names DROP COLUMN IF EXISTS avatar_url;
