-- +goose Up

CREATE TABLE app.email_preferences (
    account_id        uuid PRIMARY KEY REFERENCES app.accounts(id),
    unsubscribe_token uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    reminders         boolean NOT NULL DEFAULT true,
    messages          boolean NOT NULL DEFAULT true,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app.message_emails (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES app.conversations(id) ON DELETE CASCADE,
    recipient_id    uuid NOT NULL REFERENCES app.accounts(id),
    first_unread_at timestamptz NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    emailed_at      timestamptz,
    email_attempts  integer NOT NULL DEFAULT 0 CHECK (email_attempts >= 0),
    UNIQUE (conversation_id, recipient_id, first_unread_at)
);

CREATE INDEX message_emails_recipient_id_idx ON app.message_emails (recipient_id);
CREATE INDEX message_emails_waiting_idx ON app.message_emails (created_at) WHERE emailed_at IS NULL;

ALTER TABLE app.conversations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.notifications NO FORCE ROW LEVEL SECURITY;

INSERT INTO app.message_emails (conversation_id, recipient_id, first_unread_at, emailed_at)
SELECT side.conversation_id, side.recipient_id, min(m.created_at), now()
FROM (
    SELECT c.id AS conversation_id, c.guest_id AS recipient_id, c.guest_read_at AS read_at FROM app.conversations c
    UNION ALL
    SELECT c.id, c.host_id, c.host_read_at FROM app.conversations c
) side
JOIN app.messages m ON m.conversation_id = side.conversation_id
    AND m.sender_id IS NOT NULL AND m.sender_id <> side.recipient_id AND NOT m.automatic
    AND m.created_at > COALESCE(side.read_at, '-infinity')
GROUP BY side.conversation_id, side.recipient_id;

UPDATE app.notifications
SET emailed_at = now()
WHERE emailed_at IS NULL
  AND kind IN ('booking_requested', 'booking_confirmed', 'booking_declined', 'booking_cancelled',
               'booking_expired', 'review_invite', 'photos_waiting', 'photo_retake');

ALTER TABLE app.conversations FORCE ROW LEVEL SECURITY;
ALTER TABLE app.messages FORCE ROW LEVEL SECURITY;
ALTER TABLE app.notifications FORCE ROW LEVEL SECURITY;

ALTER TABLE app.email_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.email_preferences FORCE ROW LEVEL SECURITY;
ALTER TABLE app.message_emails ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.message_emails FORCE ROW LEVEL SECURITY;

CREATE POLICY email_preferences_select ON app.email_preferences
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());
CREATE POLICY email_preferences_migrator ON app.email_preferences
    FOR ALL TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);
CREATE POLICY message_emails_select ON app.message_emails
    FOR SELECT TO __APP_ROLE__
    USING (recipient_id = app.current_user_id());
CREATE POLICY message_emails_migrator ON app.message_emails
    FOR ALL TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

GRANT SELECT ON app.email_preferences TO __APP_ROLE__;
GRANT SELECT ON app.message_emails TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.queue_rejected_photo_alerts()
RETURNS TABLE (account_id uuid, booking_id uuid, kind text, title text, body text, for_team boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN
    RETURN QUERY
    WITH inserted AS (
        INSERT INTO app.notifications (id, account_id, booking_id, kind, event_key, for_team, title, body)
        SELECT DISTINCT ON (t.booking_id)
               gen_random_uuid(), t.guest_id, t.booking_id, 'photo_retake', 'photo_retake:' || t.booking_id, false,
               'A checkout photo was not accepted',
               left('Your host at ' || l.title || ' did not accept a photo: ' || t.note, 500)
        FROM app.booking_task_submissions t
        JOIN app.bookings b ON b.id = t.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE t.status = 'rejected' AND t.updated_at > now() - interval '3 days'
        ORDER BY t.booking_id, t.updated_at
        ON CONFLICT DO NOTHING
        RETURNING account_id, booking_id, kind, title, body, for_team
    )
    SELECT i.account_id, i.booking_id, i.kind, i.title, i.body, i.for_team FROM inserted i;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.member_emails_awaiting(batch_size integer)
RETURNS TABLE (
    email_id uuid,
    account_id uuid,
    booking_id uuid,
    conversation_id uuid,
    kind text,
    title text,
    body text,
    category text,
    unsubscribe_token uuid,
    as_host boolean
)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN
    INSERT INTO app.message_emails (conversation_id, recipient_id, first_unread_at)
    SELECT side.conversation_id, side.recipient_id, min(m.created_at)
    FROM (
        SELECT c.id AS conversation_id, c.guest_id AS recipient_id, c.guest_read_at AS read_at FROM app.conversations c
        UNION ALL
        SELECT c.id, c.host_id, c.host_read_at FROM app.conversations c
    ) side
    JOIN app.messages m ON m.conversation_id = side.conversation_id
        AND m.sender_id IS NOT NULL AND m.sender_id <> side.recipient_id AND NOT m.automatic
        AND m.created_at > COALESCE(side.read_at, '-infinity')
    GROUP BY side.conversation_id, side.recipient_id
    HAVING min(m.created_at) BETWEEN now() - interval '1 day' AND now() - interval '5 minutes'
    ON CONFLICT DO NOTHING;

    INSERT INTO app.email_preferences (account_id)
    SELECT e.recipient_id FROM app.message_emails e WHERE e.emailed_at IS NULL
    UNION
    SELECT n.account_id FROM app.notifications n
    WHERE n.emailed_at IS NULL AND n.kind IN ('review_invite', 'photos_waiting', 'photo_retake')
    ON CONFLICT DO NOTHING;

    RETURN QUERY
    SELECT due.email_id, due.account_id, due.booking_id, due.conversation_id, due.kind, due.title, due.body,
           due.category, due.unsubscribe_token, due.as_host
    FROM (
        SELECT n.id AS email_id, n.account_id, n.booking_id, NULL::uuid AS conversation_id, n.kind, n.title, n.body,
               CASE WHEN n.kind IN ('review_invite', 'photos_waiting', 'photo_retake') THEN 'reminders' ELSE '' END AS category,
               p.unsubscribe_token,
               COALESCE(l.host_id = n.account_id, false) AS as_host,
               n.email_attempts, n.created_at
        FROM app.notifications n
        LEFT JOIN app.bookings b ON b.id = n.booking_id
        LEFT JOIN app.listings l ON l.id = b.listing_id
        LEFT JOIN app.email_preferences p ON p.account_id = n.account_id
        WHERE n.emailed_at IS NULL
          AND n.email_attempts < 5
          AND n.created_at > now() - interval '3 days'
          AND NOT n.for_team
          AND (
              n.kind IN ('booking_requested', 'booking_confirmed', 'booking_declined', 'booking_cancelled', 'booking_expired')
              OR (n.kind IN ('review_invite', 'photos_waiting', 'photo_retake') AND p.reminders)
          )
        UNION ALL
        SELECT e.id, e.recipient_id, NULL::uuid, e.conversation_id, 'new_message',
               left('New message from ' || sender.name, 120),
               left(sender.name || ' sent you a message about ' || l.title || '.', 500),
               'messages', p.unsubscribe_token, c.host_id = e.recipient_id,
               e.email_attempts, e.created_at
        FROM app.message_emails e
        JOIN app.conversations c ON c.id = e.conversation_id
        JOIN app.listings l ON l.id = c.listing_id
        JOIN app.email_preferences p ON p.account_id = e.recipient_id AND p.messages
        CROSS JOIN LATERAL (
            SELECT CASE
                WHEN c.host_id = e.recipient_id
                    THEN COALESCE((SELECT a.display_name FROM app.account_names a WHERE a.account_id = c.guest_id), 'Your guest')
                ELSE COALESCE(NULLIF(l.host_name, ''), 'Your host')
            END AS name
        ) sender
        WHERE e.emailed_at IS NULL
          AND e.email_attempts < 5
          AND e.created_at > now() - interval '3 days'
          AND COALESCE(CASE WHEN c.host_id = e.recipient_id THEN c.host_read_at ELSE c.guest_read_at END, '-infinity') < e.first_unread_at
    ) due
    ORDER BY due.email_attempts, due.created_at
    LIMIT least(greatest(batch_size, 1), 200);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_member_email_attempt(email_id uuid, delivered boolean)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.notifications n
    SET email_attempts = n.email_attempts + 1,
        emailed_at = CASE WHEN delivered THEN now() ELSE n.emailed_at END
    WHERE n.id = email_id AND n.emailed_at IS NULL;

    UPDATE app.message_emails e
    SET email_attempts = e.email_attempts + 1,
        emailed_at = CASE WHEN delivered THEN now() ELSE e.emailed_at END
    WHERE e.id = email_id AND e.emailed_at IS NULL;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.unsubscribe_email(token uuid, category text)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF category NOT IN ('reminders', 'messages') THEN
        RETURN false;
    END IF;
    UPDATE app.email_preferences p
    SET reminders = p.reminders AND category <> 'reminders',
        messages = p.messages AND category <> 'messages',
        updated_at = now()
    WHERE p.unsubscribe_token = token;
    RETURN FOUND;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.queue_rejected_photo_alerts() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.member_emails_awaiting(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.record_member_email_attempt(uuid, boolean) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.unsubscribe_email(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.queue_rejected_photo_alerts() TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.member_emails_awaiting(integer) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.record_member_email_attempt(uuid, boolean) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.unsubscribe_email(uuid, text) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.unsubscribe_email(uuid, text);
DROP FUNCTION app.record_member_email_attempt(uuid, boolean);
DROP FUNCTION app.member_emails_awaiting(integer);
DROP FUNCTION app.queue_rejected_photo_alerts();
DROP TABLE app.message_emails;
DROP TABLE app.email_preferences;
