-- +goose Up

ALTER TABLE app.messages
    ADD COLUMN reply_to_id uuid REFERENCES app.messages(id) ON DELETE SET NULL;

CREATE INDEX messages_reply_to_id_idx ON app.messages (reply_to_id) WHERE reply_to_id IS NOT NULL;

CREATE POLICY messages_delete_own ON app.messages
    FOR DELETE TO __APP_ROLE__
    USING (sender_id = app.current_user_id());

GRANT DELETE ON app.messages TO __APP_ROLE__;

CREATE TABLE app.message_reactions (
    id          uuid PRIMARY KEY,
    message_id  uuid NOT NULL REFERENCES app.messages(id) ON DELETE CASCADE,
    account_id  uuid NOT NULL REFERENCES app.accounts(id),
    emoji       text NOT NULL CHECK (emoji IN ('👍', '❤️', '😂', '😮', '😢', '🙏')),
    created_at  timestamptz NOT NULL,
    UNIQUE (message_id, account_id, emoji)
);

CREATE INDEX message_reactions_account_id_idx ON app.message_reactions (account_id);

ALTER TABLE app.message_reactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.message_reactions FORCE ROW LEVEL SECURITY;

CREATE POLICY message_reactions_select ON app.message_reactions
    FOR SELECT TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.messages m
            JOIN app.conversations c ON c.id = m.conversation_id
            WHERE m.id = message_reactions.message_id
              AND (c.guest_id = app.current_user_id() OR c.host_id = app.current_user_id())
        )
    );

CREATE POLICY message_reactions_insert ON app.message_reactions
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        account_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.messages m
            JOIN app.conversations c ON c.id = m.conversation_id
            WHERE m.id = message_reactions.message_id
              AND (c.guest_id = app.current_user_id() OR c.host_id = app.current_user_id())
        )
    );

CREATE POLICY message_reactions_delete ON app.message_reactions
    FOR DELETE TO __APP_ROLE__
    USING (account_id = app.current_user_id());

GRANT SELECT, INSERT, DELETE ON app.message_reactions TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.reply_minutes(responder uuid)
RETURNS integer
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH ordered AS (
        SELECT m.sender_id, m.created_at,
               lag(m.sender_id) OVER w AS previous_sender,
               lag(m.created_at) OVER w AS previous_at
        FROM app.messages m
        JOIN app.conversations c ON c.id = m.conversation_id
        WHERE (c.guest_id = responder OR c.host_id = responder)
          AND m.sender_id IS NOT NULL
          AND m.created_at > now() - interval '90 days'
        WINDOW w AS (PARTITION BY m.conversation_id ORDER BY m.created_at, m.id)
    ),
    replies AS (
        SELECT LEAST(extract(epoch FROM o.created_at - o.previous_at), 172800) AS seconds
        FROM ordered o
        WHERE o.sender_id = responder
          AND o.previous_sender IS NOT NULL
          AND o.previous_sender <> responder
    )
    SELECT CASE WHEN count(*) >= 3 THEN ceil(avg(r.seconds) / 60)::integer END
    FROM replies r
    WHERE EXISTS (
        SELECT 1 FROM app.conversations c
        WHERE (c.guest_id = app.current_user_id() AND c.host_id = responder)
           OR (c.host_id = app.current_user_id() AND c.guest_id = responder)
    )
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.reply_minutes(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.reply_minutes(uuid) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION IF EXISTS app.reply_minutes(uuid);
DROP TABLE IF EXISTS app.message_reactions;
REVOKE DELETE ON app.messages FROM __APP_ROLE__;
DROP POLICY IF EXISTS messages_delete_own ON app.messages;
DROP INDEX IF EXISTS app.messages_reply_to_id_idx;
ALTER TABLE app.messages DROP COLUMN IF EXISTS reply_to_id;
