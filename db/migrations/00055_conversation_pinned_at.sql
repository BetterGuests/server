-- +goose Up

ALTER TABLE app.conversations
    ADD COLUMN guest_pinned_at timestamptz,
    ADD COLUMN host_pinned_at  timestamptz;

GRANT UPDATE (guest_pinned_at, host_pinned_at) ON app.conversations TO __APP_ROLE__;

-- +goose Down

REVOKE UPDATE (guest_pinned_at, host_pinned_at) ON app.conversations FROM __APP_ROLE__;

ALTER TABLE app.conversations
    DROP COLUMN IF EXISTS host_pinned_at,
    DROP COLUMN IF EXISTS guest_pinned_at;
