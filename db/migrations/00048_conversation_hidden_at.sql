-- +goose Up

ALTER TABLE app.conversations
    ADD COLUMN guest_hidden_at timestamptz,
    ADD COLUMN host_hidden_at  timestamptz;

GRANT UPDATE (guest_hidden_at, host_hidden_at) ON app.conversations TO __APP_ROLE__;

-- +goose Down

REVOKE UPDATE (guest_hidden_at, host_hidden_at) ON app.conversations FROM __APP_ROLE__;

ALTER TABLE app.conversations
    DROP COLUMN IF EXISTS host_hidden_at,
    DROP COLUMN IF EXISTS guest_hidden_at;
