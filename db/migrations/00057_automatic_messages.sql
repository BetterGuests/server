-- +goose Up

CREATE TABLE app.listing_auto_messages (
    id           uuid PRIMARY KEY,
    listing_id   uuid NOT NULL REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id      uuid NOT NULL REFERENCES app.accounts(id),
    name         text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
    trigger      text NOT NULL CHECK (trigger IN (
                     'booking_confirmed', 'before_check_in', 'check_in_day', 'check_out_eve', 'check_out_day'
                 )),
    body         text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 2000),
    private      boolean NOT NULL,
    enabled      boolean NOT NULL,
    active_since timestamptz NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);

CREATE INDEX listing_auto_messages_listing_idx ON app.listing_auto_messages (listing_id, created_at, id);
CREATE INDEX listing_auto_messages_host_id_idx ON app.listing_auto_messages (host_id);

ALTER TABLE app.messages
    ADD COLUMN automatic boolean NOT NULL DEFAULT false,
    ADD COLUMN private   boolean NOT NULL DEFAULT false;
ALTER TABLE app.messages ADD CONSTRAINT messages_automatic_shape CHECK (
    (NOT private OR automatic) AND (NOT automatic OR sender_id IS NOT NULL)
);

CREATE TABLE app.booking_auto_messages (
    booking_id      uuid NOT NULL REFERENCES app.bookings(id),
    auto_message_id uuid NOT NULL,
    message_id      uuid REFERENCES app.messages(id) ON DELETE SET NULL,
    sent_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (booking_id, auto_message_id)
);

CREATE INDEX booking_auto_messages_message_id_idx ON app.booking_auto_messages (message_id);

CREATE TABLE app.message_private_bodies (
    message_id uuid PRIMARY KEY REFERENCES app.messages(id) ON DELETE CASCADE,
    booking_id uuid NOT NULL REFERENCES app.bookings(id),
    body       text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 2000)
);

CREATE INDEX message_private_bodies_booking_id_idx ON app.message_private_bodies (booking_id);

ALTER TABLE app.listing_auto_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_auto_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE app.booking_auto_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_auto_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE app.message_private_bodies ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.message_private_bodies FORCE ROW LEVEL SECURITY;

CREATE POLICY listing_auto_messages_select ON app.listing_auto_messages
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());

CREATE POLICY listing_auto_messages_insert ON app.listing_auto_messages
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_auto_messages.listing_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY listing_auto_messages_update ON app.listing_auto_messages
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id())
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_auto_messages.listing_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY listing_auto_messages_delete ON app.listing_auto_messages
    FOR DELETE TO __APP_ROLE__
    USING (host_id = app.current_user_id());

CREATE POLICY listing_auto_messages_migrator_select ON app.listing_auto_messages
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_auto_messages_select ON app.booking_auto_messages
    FOR SELECT TO __APP_ROLE__
    USING (EXISTS (
        SELECT 1 FROM app.bookings b
        JOIN app.listings l ON l.id = b.listing_id
        WHERE b.id = booking_auto_messages.booking_id AND l.host_id = app.current_user_id()
    ));

CREATE POLICY booking_auto_messages_migrator_select ON app.booking_auto_messages
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_auto_messages_migrator_insert ON app.booking_auto_messages
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY message_private_bodies_select ON app.message_private_bodies
    FOR SELECT TO __APP_ROLE__
    USING (EXISTS (
        SELECT 1 FROM app.bookings b
        JOIN app.listings l ON l.id = b.listing_id
        WHERE b.id = message_private_bodies.booking_id
          AND (
              l.host_id = app.current_user_id()
              OR (
                  b.guest_id = app.current_user_id()
                  AND b.status = 'confirmed'
                  AND current_date BETWEEN b.starts_on - 3 AND b.ends_on
              )
          )
    ));

CREATE POLICY message_private_bodies_migrator_insert ON app.message_private_bodies
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY messages_automatic_only_by_job ON app.messages
    AS RESTRICTIVE
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (NOT automatic AND NOT private);

CREATE POLICY messages_migrator_insert_automatic ON app.messages
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (automatic AND sender_id IS NOT NULL);

GRANT SELECT, INSERT, UPDATE, DELETE ON app.listing_auto_messages TO __APP_ROLE__;
GRANT SELECT ON app.booking_auto_messages TO __APP_ROLE__;
GRANT SELECT ON app.message_private_bodies TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.auto_message_candidates(after_booking_id uuid, after_auto_message_id uuid, batch_size integer)
RETURNS TABLE (
    booking_id uuid,
    auto_message_id uuid,
    trigger text,
    body text,
    private boolean,
    quotes_secret boolean,
    has_arrival_guide boolean,
    active_since timestamptz,
    updated_at timestamptz,
    confirmed_at timestamptz,
    starts_on date,
    ends_on date,
    check_in_time text,
    check_out_time text,
    time_zone text,
    guest_name text,
    listing_title text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, t.id, t.trigger, t.body, t.private,
           coalesce(length(a.entry_code) >= 3 AND position(lower(a.entry_code) IN lower(t.body)) > 0, false)
               OR coalesce(length(a.wifi_password) >= 3 AND position(lower(a.wifi_password) IN lower(t.body)) > 0, false),
           a.listing_id IS NOT NULL, t.active_since, t.updated_at,
           coalesce(p.last_event_at, p.updated_at),
           b.starts_on, b.ends_on,
           to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
           coalesce(n.display_name, ''), l.title
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    JOIN app.payments p ON p.booking_id = b.id AND p.status = 'succeeded'
    JOIN app.listing_auto_messages t ON t.listing_id = l.id AND t.host_id = l.host_id AND t.enabled
    LEFT JOIN app.listing_arrival_guides a ON a.listing_id = l.id
    LEFT JOIN app.booking_stays s ON s.booking_id = b.id
    LEFT JOIN app.account_names n ON n.account_id = b.guest_id
    WHERE (b.id, t.id) > (after_booking_id, after_auto_message_id)
      AND b.status = 'confirmed'
      AND b.guest_id <> l.host_id
      AND b.ends_on >= current_date - 1
      AND (t.trigger = 'booking_confirmed' OR b.starts_on <= current_date + 4)
      AND s.checked_out_at IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM app.booking_auto_messages d
          WHERE d.booking_id = b.id AND d.auto_message_id = t.id
      )
    ORDER BY b.id, t.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.post_auto_message(
    target_booking_id uuid,
    target_auto_message_id uuid,
    expected_updated_at timestamptz,
    new_message_id uuid,
    new_conversation_id uuid,
    visible_body text,
    private_body text
)
RETURNS TABLE (conversation_id uuid, guest_id uuid, host_id uuid, created_at timestamptz)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
DECLARE
    target_listing uuid;
    target_guest uuid;
    target_host uuid;
    thread uuid;
    posted_at timestamptz := clock_timestamp();
BEGIN
    SELECT b.listing_id, b.guest_id INTO target_listing, target_guest
    FROM app.bookings b
    WHERE b.id = target_booking_id AND b.status = 'confirmed'
    FOR SHARE;

    SELECT l.host_id INTO target_host
    FROM app.listings l
    JOIN app.listing_auto_messages t ON t.listing_id = l.id AND t.host_id = l.host_id
    WHERE l.id = target_listing
      AND t.id = target_auto_message_id
      AND t.enabled
      AND t.updated_at = expected_updated_at;

    IF target_host IS NULL OR target_guest = target_host THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM app.payments p WHERE p.booking_id = target_booking_id AND p.status = 'succeeded')
       OR EXISTS (SELECT 1 FROM app.booking_stays s WHERE s.booking_id = target_booking_id AND s.checked_out_at IS NOT NULL)
    THEN
        RETURN;
    END IF;

    INSERT INTO app.booking_auto_messages (booking_id, auto_message_id)
    VALUES (target_booking_id, target_auto_message_id)
    ON CONFLICT (booking_id, auto_message_id) DO NOTHING;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    INSERT INTO app.conversations (id, listing_id, guest_id, host_id, created_at)
    VALUES (new_conversation_id, target_listing, target_guest, target_host, posted_at)
    ON CONFLICT (listing_id, guest_id) DO NOTHING;

    SELECT c.id INTO thread
    FROM app.conversations c
    WHERE c.listing_id = target_listing AND c.guest_id = target_guest;

    INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at, automatic, private)
    VALUES (new_message_id, thread, target_host, visible_body, posted_at, true, private_body IS NOT NULL);

    IF private_body IS NOT NULL THEN
        INSERT INTO app.message_private_bodies (message_id, booking_id, body)
        VALUES (new_message_id, target_booking_id, private_body);
    END IF;

    UPDATE app.booking_auto_messages d
    SET message_id = new_message_id
    WHERE d.booking_id = target_booking_id AND d.auto_message_id = target_auto_message_id;

    UPDATE app.conversations c
    SET last_message_at = posted_at, last_message_preview = left(visible_body, 200)
    WHERE c.id = thread;

    RETURN QUERY SELECT thread, target_guest, target_host, posted_at;
END
$$;
-- +goose StatementEnd

CREATE POLICY booking_auto_messages_migrator_update ON app.booking_auto_messages
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

REVOKE ALL ON FUNCTION app.auto_message_candidates(uuid, uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.post_auto_message(uuid, uuid, timestamptz, uuid, uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.auto_message_candidates(uuid, uuid, integer) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.post_auto_message(uuid, uuid, timestamptz, uuid, uuid, text, text) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.reply_minutes(responder uuid)
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
          AND NOT m.automatic
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

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.reply_minutes(responder uuid)
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

DROP FUNCTION app.post_auto_message(uuid, uuid, timestamptz, uuid, uuid, text, text);
DROP FUNCTION app.auto_message_candidates(uuid, uuid, integer);
DROP POLICY messages_migrator_insert_automatic ON app.messages;
DROP POLICY messages_automatic_only_by_job ON app.messages;
DROP TABLE app.message_private_bodies;
DROP TABLE app.booking_auto_messages;
ALTER TABLE app.messages DROP CONSTRAINT messages_automatic_shape;
ALTER TABLE app.messages DROP COLUMN private, DROP COLUMN automatic;
DROP TABLE app.listing_auto_messages;
