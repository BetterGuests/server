-- +goose Up

CREATE TABLE app.account_names (
    account_id    uuid PRIMARY KEY REFERENCES app.accounts(id),
    display_name  text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 80),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app.conversations (
    id                    uuid PRIMARY KEY,
    listing_id            uuid NOT NULL REFERENCES app.listings(id),
    guest_id              uuid NOT NULL REFERENCES app.accounts(id),
    host_id               uuid NOT NULL REFERENCES app.accounts(id),
    created_at            timestamptz NOT NULL,
    last_message_at       timestamptz,
    last_message_preview  text CHECK (last_message_preview IS NULL OR length(last_message_preview) <= 200),
    guest_read_at         timestamptz,
    host_read_at          timestamptz,
    CHECK (guest_id <> host_id),
    UNIQUE (listing_id, guest_id)
);

CREATE INDEX conversations_guest_id_idx ON app.conversations (guest_id);
CREATE INDEX conversations_host_id_idx ON app.conversations (host_id);
CREATE INDEX conversations_listing_id_idx ON app.conversations (listing_id);

CREATE TABLE app.messages (
    id               uuid PRIMARY KEY,
    conversation_id  uuid NOT NULL REFERENCES app.conversations(id) ON DELETE CASCADE,
    sender_id        uuid NOT NULL REFERENCES app.accounts(id),
    body             text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 4000),
    created_at       timestamptz NOT NULL
);

CREATE INDEX messages_conversation_created_idx ON app.messages (conversation_id, created_at DESC, id DESC);
CREATE INDEX messages_sender_id_idx ON app.messages (sender_id);

ALTER TABLE app.account_names ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_names FORCE ROW LEVEL SECURITY;
ALTER TABLE app.conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.conversations FORCE ROW LEVEL SECURITY;
ALTER TABLE app.messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.messages FORCE ROW LEVEL SECURITY;

CREATE POLICY account_names_select ON app.account_names
    FOR SELECT TO __APP_ROLE__
    USING (
        account_id = app.current_user_id()
        OR EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.guest_id = account_names.account_id AND l.host_id = app.current_user_id()
        )
        OR EXISTS (
            SELECT 1 FROM app.conversations c
            WHERE c.guest_id = account_names.account_id AND c.host_id = app.current_user_id()
        )
    );

CREATE POLICY account_names_insert ON app.account_names
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY account_names_update ON app.account_names
    FOR UPDATE TO __APP_ROLE__
    USING (account_id = app.current_user_id())
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY conversations_select ON app.conversations
    FOR SELECT TO __APP_ROLE__
    USING (guest_id = app.current_user_id() OR host_id = app.current_user_id());

CREATE POLICY conversations_insert_guest ON app.conversations
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.host_id = conversations.host_id AND l.status = 'published'
        )
    );

CREATE POLICY conversations_insert_host ON app.conversations
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.listing_id = conversations.listing_id
              AND b.guest_id = conversations.guest_id
              AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY conversations_update ON app.conversations
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id() OR host_id = app.current_user_id())
    WITH CHECK (guest_id = app.current_user_id() OR host_id = app.current_user_id());

CREATE POLICY messages_select ON app.messages
    FOR SELECT TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.conversations c
            WHERE c.id = messages.conversation_id
              AND (c.guest_id = app.current_user_id() OR c.host_id = app.current_user_id())
        )
    );

CREATE POLICY messages_insert ON app.messages
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        sender_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.conversations c
            WHERE c.id = messages.conversation_id
              AND (c.guest_id = app.current_user_id() OR c.host_id = app.current_user_id())
        )
    );

GRANT SELECT, INSERT, UPDATE ON app.account_names TO __APP_ROLE__;
GRANT SELECT, INSERT ON app.conversations TO __APP_ROLE__;
GRANT UPDATE (last_message_at, last_message_preview, guest_read_at, host_read_at) ON app.conversations TO __APP_ROLE__;
GRANT SELECT, INSERT ON app.messages TO __APP_ROLE__;

-- +goose Down

DROP TABLE IF EXISTS app.messages;
DROP TABLE IF EXISTS app.conversations;
DROP TABLE IF EXISTS app.account_names;
