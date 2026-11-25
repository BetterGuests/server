-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN check_in_time  time NOT NULL DEFAULT '16:00',
    ADD COLUMN check_out_time time NOT NULL DEFAULT '11:00',
    ADD COLUMN time_zone      text NOT NULL DEFAULT 'America/New_York'
        CHECK (time_zone IN (
            'America/New_York', 'America/Chicago', 'America/Denver', 'America/Phoenix',
            'America/Los_Angeles', 'America/Anchorage', 'Pacific/Honolulu'
        ));

CREATE TABLE app.listing_arrival_guides (
    listing_id             uuid PRIMARY KEY REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id                uuid NOT NULL REFERENCES app.accounts(id),
    entry_instructions     text NOT NULL CHECK (length(entry_instructions) BETWEEN 1 AND 4000),
    entry_code             text CHECK (entry_code IS NULL OR length(entry_code) <= 60),
    wifi_name              text CHECK (wifi_name IS NULL OR length(wifi_name) <= 100),
    wifi_password          text CHECK (wifi_password IS NULL OR length(wifi_password) <= 100),
    checkout_instructions  text CHECK (checkout_instructions IS NULL OR length(checkout_instructions) <= 2000),
    updated_at             timestamptz NOT NULL
);

CREATE INDEX listing_arrival_guides_host_id_idx ON app.listing_arrival_guides (host_id);

CREATE TABLE app.booking_stays (
    booking_id      uuid PRIMARY KEY REFERENCES app.bookings(id),
    guest_id        uuid NOT NULL REFERENCES app.accounts(id),
    checked_in_at   timestamptz NOT NULL,
    checked_out_at  timestamptz,
    CHECK (checked_out_at IS NULL OR checked_out_at >= checked_in_at)
);

CREATE INDEX booking_stays_guest_id_idx ON app.booking_stays (guest_id);

ALTER TABLE app.listing_arrival_guides ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_arrival_guides FORCE ROW LEVEL SECURITY;
ALTER TABLE app.booking_stays ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.booking_stays FORCE ROW LEVEL SECURITY;

CREATE POLICY listing_arrival_guides_select ON app.listing_arrival_guides
    FOR SELECT TO __APP_ROLE__
    USING (
        host_id = app.current_user_id()
        OR EXISTS (
            SELECT 1 FROM app.bookings b
            WHERE b.listing_id = listing_arrival_guides.listing_id
              AND b.guest_id = app.current_user_id()
              AND b.status = 'confirmed'
              AND current_date BETWEEN b.starts_on - 3 AND b.ends_on
        )
    );

CREATE POLICY listing_arrival_guides_insert ON app.listing_arrival_guides
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_arrival_guides.listing_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY listing_arrival_guides_update ON app.listing_arrival_guides
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id())
    WITH CHECK (host_id = app.current_user_id());

CREATE POLICY listing_arrival_guides_migrator_select ON app.listing_arrival_guides
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY booking_stays_select ON app.booking_stays
    FOR SELECT TO __APP_ROLE__
    USING (EXISTS (SELECT 1 FROM app.bookings b WHERE b.id = booking_stays.booking_id));

CREATE POLICY booking_stays_insert ON app.booking_stays
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND checked_out_at IS NULL
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            WHERE b.id = booking_stays.booking_id
              AND b.guest_id = app.current_user_id()
              AND b.status = 'confirmed'
        )
    );

CREATE POLICY booking_stays_update ON app.booking_stays
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id() AND checked_out_at IS NULL)
    WITH CHECK (guest_id = app.current_user_id());

CREATE POLICY booking_stays_migrator_select ON app.booking_stays
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT, UPDATE ON app.listing_arrival_guides TO __APP_ROLE__;
GRANT SELECT, INSERT ON app.booking_stays TO __APP_ROLE__;
GRANT UPDATE (checked_out_at) ON app.booking_stays TO __APP_ROLE__;

ALTER TABLE app.listing_reviews
    ADD COLUMN author_name    text CHECK (author_name IS NULL OR length(author_name) BETWEEN 1 AND 120),
    ADD COLUMN published_at   timestamptz,
    ADD COLUMN cleanliness    integer CHECK (cleanliness BETWEEN 1 AND 5),
    ADD COLUMN accuracy       integer CHECK (accuracy BETWEEN 1 AND 5),
    ADD COLUMN check_in       integer CHECK (check_in BETWEEN 1 AND 5),
    ADD COLUMN communication  integer CHECK (communication BETWEEN 1 AND 5),
    ADD COLUMN location       integer CHECK (location BETWEEN 1 AND 5),
    ADD COLUMN value          integer CHECK (value BETWEEN 1 AND 5);

CREATE POLICY listing_reviews_migrator_select ON app.listing_reviews
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY listing_reviews_migrator_update ON app.listing_reviews
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

UPDATE app.listing_reviews SET published_at = created_at;

DROP POLICY reviews_select ON app.listing_reviews;
DROP POLICY reviews_insert ON app.listing_reviews;
DROP POLICY reviews_update ON app.listing_reviews;
DROP POLICY reviews_delete ON app.listing_reviews;
REVOKE UPDATE, DELETE ON app.listing_reviews FROM __APP_ROLE__;

CREATE POLICY reviews_select ON app.listing_reviews
    FOR SELECT TO __APP_ROLE__
    USING (published_at IS NOT NULL OR guest_id = app.current_user_id());

CREATE POLICY reviews_insert ON app.listing_reviews
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND published_at IS NULL
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            WHERE b.id = listing_reviews.booking_id
              AND b.guest_id = app.current_user_id()
              AND b.listing_id = listing_reviews.listing_id
              AND b.status = 'completed'
              AND b.completed_at > now() - interval '14 days'
        )
    );

CREATE TABLE app.guest_reviews (
    id            uuid PRIMARY KEY,
    booking_id    uuid NOT NULL UNIQUE REFERENCES app.bookings(id),
    listing_id    uuid NOT NULL REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id       uuid NOT NULL REFERENCES app.accounts(id),
    guest_id      uuid NOT NULL REFERENCES app.accounts(id),
    rating        integer NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment       text NOT NULL CHECK (length(comment) BETWEEN 1 AND 4000),
    author_name   text CHECK (author_name IS NULL OR length(author_name) BETWEEN 1 AND 120),
    created_at    timestamptz NOT NULL,
    published_at  timestamptz,
    CHECK (host_id <> guest_id)
);

CREATE INDEX guest_reviews_guest_id_idx ON app.guest_reviews (guest_id);
CREATE INDEX guest_reviews_host_id_idx ON app.guest_reviews (host_id);
CREATE INDEX guest_reviews_listing_id_idx ON app.guest_reviews (listing_id);

ALTER TABLE app.guest_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.guest_reviews FORCE ROW LEVEL SECURITY;

CREATE POLICY guest_reviews_select ON app.guest_reviews
    FOR SELECT TO __APP_ROLE__
    USING (
        host_id = app.current_user_id()
        OR (
            published_at IS NOT NULL
            AND (
                guest_id = app.current_user_id()
                OR EXISTS (
                    SELECT 1 FROM app.bookings b
                    JOIN app.listings l ON l.id = b.listing_id
                    WHERE b.guest_id = guest_reviews.guest_id AND l.host_id = app.current_user_id()
                )
            )
        )
    );

CREATE POLICY guest_reviews_insert ON app.guest_reviews
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND published_at IS NULL
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.id = guest_reviews.booking_id
              AND b.listing_id = guest_reviews.listing_id
              AND b.guest_id = guest_reviews.guest_id
              AND l.host_id = app.current_user_id()
              AND b.status = 'completed'
              AND b.completed_at > now() - interval '14 days'
        )
    );

CREATE POLICY guest_reviews_migrator_select ON app.guest_reviews
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY guest_reviews_migrator_update ON app.guest_reviews
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

GRANT SELECT, INSERT ON app.guest_reviews TO __APP_ROLE__;

CREATE TABLE app.notifications (
    id          uuid PRIMARY KEY,
    account_id  uuid NOT NULL REFERENCES app.accounts(id),
    booking_id  uuid NOT NULL REFERENCES app.bookings(id),
    kind        text NOT NULL CHECK (kind ~ '^[a-z_]{1,40}$'),
    title       text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    body        text NOT NULL CHECK (length(body) BETWEEN 1 AND 500),
    created_at  timestamptz NOT NULL DEFAULT now(),
    read_at     timestamptz,
    UNIQUE (account_id, booking_id, kind)
);

CREATE INDEX notifications_account_created_idx ON app.notifications (account_id, created_at DESC, id DESC);
CREATE INDEX notifications_booking_id_idx ON app.notifications (booking_id);

ALTER TABLE app.notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.notifications FORCE ROW LEVEL SECURITY;

CREATE POLICY notifications_select ON app.notifications
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY notifications_update ON app.notifications
    FOR UPDATE TO __APP_ROLE__
    USING (account_id = app.current_user_id())
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY notifications_migrator_select ON app.notifications
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY notifications_migrator_insert ON app.notifications
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

GRANT SELECT ON app.notifications TO __APP_ROLE__;
GRANT UPDATE (read_at) ON app.notifications TO __APP_ROLE__;

ALTER TABLE app.messages ALTER COLUMN sender_id DROP NOT NULL;
ALTER TABLE app.messages
    ADD COLUMN booking_id  uuid REFERENCES app.bookings(id),
    ADD COLUMN event_kind  text CHECK (event_kind IS NULL OR event_kind ~ '^[a-z_]{1,40}$');
ALTER TABLE app.messages ADD CONSTRAINT messages_system_shape CHECK (
    (sender_id IS NULL) = (event_kind IS NOT NULL)
    AND (sender_id IS NULL) = (booking_id IS NOT NULL)
);

CREATE UNIQUE INDEX messages_one_per_booking_event_idx
    ON app.messages (booking_id, event_kind) WHERE event_kind IS NOT NULL;

CREATE POLICY messages_migrator_select ON app.messages
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY messages_migrator_insert ON app.messages
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (sender_id IS NULL);

CREATE POLICY conversations_migrator_select ON app.conversations
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY conversations_migrator_insert ON app.conversations
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY conversations_migrator_update ON app.conversations
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE POLICY listings_journey_select ON app.listings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY listings_rating_update ON app.listings
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE POLICY bookings_completion_update ON app.bookings
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (status = 'confirmed')
    WITH CHECK (status = 'completed');

CREATE POLICY account_names_journey_select ON app.account_names
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.complete_finished_stays(batch_size integer)
RETURNS SETOF uuid
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH due AS (
        SELECT b.id,
               least(now(), (b.ends_on + l.check_out_time) AT TIME ZONE l.time_zone) AS finished_at
        FROM app.bookings b
        JOIN app.listings l ON l.id = b.listing_id
        WHERE b.status = 'confirmed'
          AND (
              (b.ends_on + l.check_out_time) AT TIME ZONE l.time_zone <= now()
              OR EXISTS (
                  SELECT 1 FROM app.booking_stays s
                  WHERE s.booking_id = b.id
                    AND s.checked_out_at IS NOT NULL
                    AND (s.checked_out_at AT TIME ZONE l.time_zone)::date >= b.ends_on
              )
          )
        ORDER BY b.ends_on, b.id
        LIMIT least(greatest(batch_size, 1), 500)
    )
    UPDATE app.bookings b
    SET status = 'completed', completed_at = due.finished_at
    FROM due
    WHERE b.id = due.id AND b.status = 'confirmed'
    RETURNING b.id
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.publish_due_reviews()
RETURNS SETOF uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    due uuid[];
BEGIN
    SELECT coalesce(array_agg(b.id), '{}') INTO due
    FROM app.bookings b
    WHERE b.status = 'completed'
      AND (
          EXISTS (SELECT 1 FROM app.listing_reviews r WHERE r.booking_id = b.id AND r.published_at IS NULL)
          OR EXISTS (SELECT 1 FROM app.guest_reviews g WHERE g.booking_id = b.id AND g.published_at IS NULL)
      )
      AND (
          b.completed_at <= now() - interval '14 days'
          OR (
              EXISTS (SELECT 1 FROM app.listing_reviews r WHERE r.booking_id = b.id)
              AND EXISTS (SELECT 1 FROM app.guest_reviews g WHERE g.booking_id = b.id)
          )
      );

    UPDATE app.listing_reviews r SET published_at = now()
    WHERE r.booking_id = ANY (due) AND r.published_at IS NULL;

    UPDATE app.guest_reviews g SET published_at = now()
    WHERE g.booking_id = ANY (due) AND g.published_at IS NULL;

    UPDATE app.listings l
    SET rating_avg = summary.average, rating_count = summary.total
    FROM (
        SELECT r.listing_id, round(avg(r.rating), 2) AS average, count(*)::integer AS total
        FROM app.listing_reviews r
        WHERE r.published_at IS NOT NULL
          AND r.listing_id IN (SELECT t.listing_id FROM app.listing_reviews t WHERE t.booking_id = ANY (due))
        GROUP BY r.listing_id
    ) summary
    WHERE l.id = summary.listing_id;

    RETURN QUERY SELECT unnest(due);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.stay_journey(after_booking_id uuid, batch_size integer)
RETURNS TABLE (
    booking_id uuid,
    listing_title text,
    status text,
    payment_status text,
    guest_id uuid,
    host_id uuid,
    guest_name text,
    starts_on date,
    ends_on date,
    check_in_time text,
    check_out_time text,
    time_zone text,
    completed_at timestamptz,
    checked_in_at timestamptz,
    checked_out_at timestamptz,
    has_arrival_guide boolean,
    guest_reviewed boolean,
    host_reviewed boolean,
    guest_review_published boolean,
    host_review_published boolean,
    sent_notifications text[],
    posted_events text[]
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, l.title, b.status, coalesce(p.status, ''), b.guest_id, l.host_id,
           coalesce(n.display_name, ''), b.starts_on, b.ends_on,
           to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI'), l.time_zone,
           b.completed_at, s.checked_in_at, s.checked_out_at,
           EXISTS (SELECT 1 FROM app.listing_arrival_guides a WHERE a.listing_id = l.id),
           r.id IS NOT NULL, g.id IS NOT NULL,
           r.published_at IS NOT NULL, g.published_at IS NOT NULL,
           ARRAY(SELECT x.kind || ':' || x.account_id::text FROM app.notifications x WHERE x.booking_id = b.id),
           ARRAY(SELECT m.event_kind FROM app.messages m WHERE m.booking_id = b.id AND m.event_kind IS NOT NULL)
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    LEFT JOIN app.payments p ON p.booking_id = b.id
    LEFT JOIN app.account_names n ON n.account_id = b.guest_id
    LEFT JOIN app.booking_stays s ON s.booking_id = b.id
    LEFT JOIN app.listing_reviews r ON r.booking_id = b.id
    LEFT JOIN app.guest_reviews g ON g.booking_id = b.id
    WHERE b.id > after_booking_id
      AND (
          (b.status = 'completed' AND b.completed_at > now() - interval '21 days')
          OR (b.status <> 'completed' AND b.ends_on >= current_date - 1)
      )
    ORDER BY b.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.deliver_stay_notification(
    target_booking_id uuid,
    event_name text,
    recipient_id uuid,
    notification_id uuid,
    notification_title text,
    notification_body text
)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH inserted AS (
        INSERT INTO app.notifications (id, account_id, booking_id, kind, title, body)
        SELECT notification_id, recipient_id, b.id, event_name, notification_title, notification_body
        FROM app.bookings b
        JOIN app.listings l ON l.id = b.listing_id
        WHERE b.id = target_booking_id AND recipient_id IN (b.guest_id, l.host_id)
        ON CONFLICT (account_id, booking_id, kind) DO NOTHING
        RETURNING 1
    )
    SELECT EXISTS (SELECT 1 FROM inserted)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.post_stay_message(
    target_booking_id uuid,
    event_name text,
    message_id uuid,
    new_conversation_id uuid,
    message_body text
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
    posted_at timestamptz := now();
BEGIN
    SELECT b.listing_id, b.guest_id, l.host_id INTO target_listing, target_guest, target_host
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    WHERE b.id = target_booking_id;

    IF target_listing IS NULL OR target_guest = target_host THEN
        RETURN;
    END IF;

    INSERT INTO app.conversations (id, listing_id, guest_id, host_id, created_at)
    VALUES (new_conversation_id, target_listing, target_guest, target_host, posted_at)
    ON CONFLICT (listing_id, guest_id) DO NOTHING;

    SELECT c.id INTO thread
    FROM app.conversations c
    WHERE c.listing_id = target_listing AND c.guest_id = target_guest;

    INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at, booking_id, event_kind)
    VALUES (message_id, thread, NULL, message_body, posted_at, target_booking_id, event_name)
    ON CONFLICT (booking_id, event_kind) WHERE event_kind IS NOT NULL DO NOTHING;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE app.conversations c
    SET last_message_at = posted_at, last_message_preview = left(message_body, 200)
    WHERE c.id = thread;

    RETURN QUERY SELECT thread, target_guest, target_host, posted_at;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.complete_finished_stays(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.publish_due_reviews() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.stay_journey(uuid, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.deliver_stay_notification(uuid, text, uuid, uuid, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.post_stay_message(uuid, text, uuid, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.complete_finished_stays(integer) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.publish_due_reviews() TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.stay_journey(uuid, integer) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.deliver_stay_notification(uuid, text, uuid, uuid, text, text) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.post_stay_message(uuid, text, uuid, uuid, text) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.post_stay_message(uuid, text, uuid, uuid, text);
DROP FUNCTION app.deliver_stay_notification(uuid, text, uuid, uuid, text, text);
DROP FUNCTION app.stay_journey(uuid, integer);
DROP FUNCTION app.publish_due_reviews();
DROP FUNCTION app.complete_finished_stays(integer);

DROP POLICY account_names_journey_select ON app.account_names;
DROP POLICY bookings_completion_update ON app.bookings;
DROP POLICY listings_rating_update ON app.listings;
DROP POLICY listings_journey_select ON app.listings;
DROP POLICY conversations_migrator_update ON app.conversations;
DROP POLICY conversations_migrator_insert ON app.conversations;
DROP POLICY conversations_migrator_select ON app.conversations;
DROP POLICY messages_migrator_insert ON app.messages;
DROP INDEX app.messages_one_per_booking_event_idx;
ALTER TABLE app.messages DROP CONSTRAINT messages_system_shape;
CREATE POLICY messages_migrator_delete ON app.messages
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (sender_id IS NULL);
DELETE FROM app.messages WHERE sender_id IS NULL;
DROP POLICY messages_migrator_delete ON app.messages;
DROP POLICY messages_migrator_select ON app.messages;
ALTER TABLE app.messages DROP COLUMN event_kind, DROP COLUMN booking_id;
ALTER TABLE app.messages ALTER COLUMN sender_id SET NOT NULL;

DROP TABLE app.notifications;
DROP TABLE app.guest_reviews;

DROP POLICY reviews_insert ON app.listing_reviews;
DROP POLICY reviews_select ON app.listing_reviews;
DROP POLICY listing_reviews_migrator_update ON app.listing_reviews;
DROP POLICY listing_reviews_migrator_select ON app.listing_reviews;

CREATE POLICY reviews_select ON app.listing_reviews
    FOR SELECT TO __APP_ROLE__
    USING (
        guest_id = app.current_user_id()
        OR EXISTS (SELECT 1 FROM app.listings l WHERE l.id = listing_id)
    );

CREATE POLICY reviews_insert ON app.listing_reviews
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        guest_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.bookings b
            WHERE b.id = booking_id
              AND b.guest_id = app.current_user_id()
              AND b.listing_id = listing_id
              AND b.status = 'completed'
        )
    );

CREATE POLICY reviews_update ON app.listing_reviews
    FOR UPDATE TO __APP_ROLE__
    USING (guest_id = app.current_user_id())
    WITH CHECK (guest_id = app.current_user_id());

CREATE POLICY reviews_delete ON app.listing_reviews
    FOR DELETE TO __APP_ROLE__
    USING (guest_id = app.current_user_id());

GRANT UPDATE, DELETE ON app.listing_reviews TO __APP_ROLE__;

ALTER TABLE app.listing_reviews
    DROP COLUMN value,
    DROP COLUMN location,
    DROP COLUMN communication,
    DROP COLUMN check_in,
    DROP COLUMN accuracy,
    DROP COLUMN cleanliness,
    DROP COLUMN published_at,
    DROP COLUMN author_name;

DROP TABLE app.booking_stays;
DROP TABLE app.listing_arrival_guides;
ALTER TABLE app.listings DROP COLUMN time_zone, DROP COLUMN check_out_time, DROP COLUMN check_in_time;
