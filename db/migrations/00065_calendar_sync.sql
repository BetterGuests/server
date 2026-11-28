-- +goose Up

CREATE TABLE app.listing_calendar_links (
    listing_id uuid PRIMARY KEY REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id    uuid NOT NULL REFERENCES app.accounts(id),
    token      text NOT NULL UNIQUE CHECK (length(token) BETWEEN 32 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX listing_calendar_links_host_id_idx ON app.listing_calendar_links (host_id);

CREATE TABLE app.listing_calendar_feeds (
    id              uuid PRIMARY KEY,
    listing_id      uuid NOT NULL REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id         uuid NOT NULL REFERENCES app.accounts(id),
    name            text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
    url             text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_attempt_at timestamptz,
    last_synced_at  timestamptz,
    last_error      text CHECK (length(last_error) <= 300),
    UNIQUE (id, listing_id),
    UNIQUE (listing_id, url)
);

CREATE INDEX listing_calendar_feeds_host_id_idx ON app.listing_calendar_feeds (host_id);
CREATE INDEX listing_calendar_feeds_due_idx ON app.listing_calendar_feeds (last_attempt_at NULLS FIRST);

CREATE TABLE app.listing_calendar_blocks (
    id         uuid PRIMARY KEY,
    feed_id    uuid NOT NULL,
    listing_id uuid NOT NULL,
    host_id    uuid NOT NULL REFERENCES app.accounts(id),
    starts_on  date NOT NULL,
    ends_on    date NOT NULL CHECK (ends_on > starts_on),
    FOREIGN KEY (feed_id, listing_id)
        REFERENCES app.listing_calendar_feeds (id, listing_id) ON DELETE CASCADE
);

CREATE INDEX listing_calendar_blocks_feed_idx ON app.listing_calendar_blocks (feed_id, listing_id);
CREATE INDEX listing_calendar_blocks_dates_idx ON app.listing_calendar_blocks (listing_id, starts_on);
CREATE INDEX listing_calendar_blocks_host_id_idx ON app.listing_calendar_blocks (host_id);

ALTER TABLE app.listing_calendar_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_calendar_links FORCE ROW LEVEL SECURITY;
ALTER TABLE app.listing_calendar_feeds ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_calendar_feeds FORCE ROW LEVEL SECURITY;
ALTER TABLE app.listing_calendar_blocks ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.listing_calendar_blocks FORCE ROW LEVEL SECURITY;

CREATE POLICY listing_calendar_links_select ON app.listing_calendar_links
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());
CREATE POLICY listing_calendar_links_insert ON app.listing_calendar_links
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (SELECT 1 FROM app.listings l WHERE l.id = listing_id AND l.host_id = app.current_user_id())
    );
CREATE POLICY listing_calendar_links_update ON app.listing_calendar_links
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id())
    WITH CHECK (host_id = app.current_user_id());

CREATE POLICY listing_calendar_feeds_select ON app.listing_calendar_feeds
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());
CREATE POLICY listing_calendar_feeds_insert ON app.listing_calendar_feeds
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (SELECT 1 FROM app.listings l WHERE l.id = listing_id AND l.host_id = app.current_user_id())
    );
CREATE POLICY listing_calendar_feeds_update ON app.listing_calendar_feeds
    FOR UPDATE TO __APP_ROLE__
    USING (host_id = app.current_user_id())
    WITH CHECK (host_id = app.current_user_id());
CREATE POLICY listing_calendar_feeds_delete ON app.listing_calendar_feeds
    FOR DELETE TO __APP_ROLE__
    USING (host_id = app.current_user_id());

CREATE POLICY listing_calendar_blocks_select ON app.listing_calendar_blocks
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());
CREATE POLICY listing_calendar_blocks_insert ON app.listing_calendar_blocks
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (SELECT 1 FROM app.listings l WHERE l.id = listing_id AND l.host_id = app.current_user_id())
    );
CREATE POLICY listing_calendar_blocks_delete ON app.listing_calendar_blocks
    FOR DELETE TO __APP_ROLE__
    USING (host_id = app.current_user_id());

CREATE POLICY listing_calendar_links_migrator_select ON app.listing_calendar_links
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);
CREATE POLICY listing_calendar_feeds_migrator_select ON app.listing_calendar_feeds
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);
CREATE POLICY listing_calendar_feeds_migrator_update ON app.listing_calendar_feeds
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);
CREATE POLICY listing_calendar_blocks_migrator_select ON app.listing_calendar_blocks
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT, INSERT ON app.listing_calendar_links TO __APP_ROLE__;
GRANT UPDATE (token, created_at) ON app.listing_calendar_links TO __APP_ROLE__;
GRANT SELECT, INSERT, DELETE ON app.listing_calendar_feeds TO __APP_ROLE__;
GRANT UPDATE (last_attempt_at, last_synced_at, last_error) ON app.listing_calendar_feeds TO __APP_ROLE__;
GRANT SELECT, INSERT, DELETE ON app.listing_calendar_blocks TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.lock_listing_calendar(target_listing_id uuid)
RETURNS void
LANGUAGE sql
SET search_path = pg_catalog
AS $$
    SELECT pg_advisory_xact_lock(hashtextextended('listing-calendar:' || target_listing_id::text, 0))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.serialize_calendar_block()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM app.lock_listing_calendar(NEW.listing_id);
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.serialize_calendar_block() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.lock_listing_calendar(uuid) FROM PUBLIC;

CREATE TRIGGER listing_calendar_blocks_serialize
    BEFORE INSERT ON app.listing_calendar_blocks
    FOR EACH ROW EXECUTE FUNCTION app.serialize_calendar_block();

-- +goose StatementBegin
CREATE FUNCTION app.keep_bookings_off_blocked_dates()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM app.lock_listing_calendar(NEW.listing_id);
    IF EXISTS (
        SELECT 1 FROM app.listing_calendar_blocks b
        WHERE b.listing_id = NEW.listing_id
          AND daterange(b.starts_on, b.ends_on, '[)') && daterange(NEW.starts_on, NEW.ends_on, '[)')
    ) THEN
        RAISE EXCEPTION 'those dates are blocked by a synced calendar' USING ERRCODE = 'exclusion_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.keep_bookings_off_blocked_dates() FROM PUBLIC;

CREATE TRIGGER bookings_keep_off_blocked_dates_insert
    BEFORE INSERT ON app.bookings
    FOR EACH ROW
    WHEN (NEW.status IN ('requested', 'pending'))
    EXECUTE FUNCTION app.keep_bookings_off_blocked_dates();

CREATE TRIGGER bookings_keep_off_blocked_dates_approval
    BEFORE UPDATE OF status ON app.bookings
    FOR EACH ROW
    WHEN (OLD.status = 'requested' AND NEW.status = 'pending')
    EXECUTE FUNCTION app.keep_bookings_off_blocked_dates();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.listing_booked_dates(target_listing_id uuid, earliest_checkout date, range_limit integer)
RETURNS TABLE (starts_on date, ends_on date)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT held.starts_on, held.ends_on
    FROM (
        SELECT b.starts_on, b.ends_on
        FROM app.bookings b
        WHERE b.listing_id = target_listing_id
          AND (
              b.status IN ('requested', 'pending', 'confirmed')
              OR EXISTS (
                  SELECT 1 FROM app.booking_cancellations x
                  WHERE x.booking_id = b.id AND x.cancelled_by = 'host'
              )
          )
        UNION
        SELECT c.starts_on, c.ends_on
        FROM app.listing_calendar_blocks c
        WHERE c.listing_id = target_listing_id
    ) held
    WHERE held.ends_on >= earliest_checkout
      AND EXISTS (
          SELECT 1 FROM app.listings l
          WHERE l.id = target_listing_id AND l.status = 'published'
      )
    ORDER BY held.starts_on
    LIMIT least(greatest(range_limit, 1), 500)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.calendar_export(link_token text, earliest_checkout date)
RETURNS TABLE (booking_id uuid, starts_on date, ends_on date, created_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, b.starts_on, b.ends_on, b.created_at
    FROM app.listing_calendar_links k
    LEFT JOIN app.bookings b
      ON b.listing_id = k.listing_id
     AND b.status IN ('requested', 'pending', 'confirmed')
     AND b.ends_on >= earliest_checkout
    WHERE k.token = link_token
    ORDER BY b.starts_on
    LIMIT 1000
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_due_calendar_feeds(not_attempted_since timestamptz, batch_size integer)
RETURNS TABLE (id uuid, listing_id uuid, host_id uuid, url text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    UPDATE app.listing_calendar_feeds f
    SET last_attempt_at = now()
    WHERE f.id IN (
        SELECT due.id FROM app.listing_calendar_feeds due
        WHERE due.last_attempt_at IS NULL OR due.last_attempt_at < not_attempted_since
        ORDER BY due.last_attempt_at NULLS FIRST
        LIMIT least(greatest(batch_size, 1), 100)
        FOR UPDATE SKIP LOCKED
    )
    RETURNING f.id, f.listing_id, f.host_id, f.url
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.calendar_export(text, date) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.claim_due_calendar_feeds(timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.calendar_export(text, date) TO __APP_ROLE__;
GRANT EXECUTE ON FUNCTION app.claim_due_calendar_feeds(timestamptz, integer) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.claim_due_calendar_feeds(timestamptz, integer);
DROP FUNCTION app.calendar_export(text, date);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.listing_booked_dates(target_listing_id uuid, earliest_checkout date, range_limit integer)
RETURNS TABLE (starts_on date, ends_on date)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.starts_on, b.ends_on
    FROM app.bookings b
    JOIN app.listings l ON l.id = b.listing_id
    WHERE b.listing_id = target_listing_id
      AND l.status = 'published'
      AND (
          b.status IN ('requested', 'pending', 'confirmed')
          OR EXISTS (
              SELECT 1 FROM app.booking_cancellations c
              WHERE c.booking_id = b.id AND c.cancelled_by = 'host'
          )
      )
      AND b.ends_on >= earliest_checkout
    ORDER BY b.starts_on
    LIMIT least(greatest(range_limit, 1), 500)
$$;
-- +goose StatementEnd

DROP TRIGGER bookings_keep_off_blocked_dates_approval ON app.bookings;
DROP TRIGGER bookings_keep_off_blocked_dates_insert ON app.bookings;
DROP FUNCTION app.keep_bookings_off_blocked_dates();
DROP TABLE app.listing_calendar_blocks;
DROP FUNCTION app.serialize_calendar_block();
DROP FUNCTION app.lock_listing_calendar(uuid);
DROP TABLE app.listing_calendar_feeds;
DROP TABLE app.listing_calendar_links;
