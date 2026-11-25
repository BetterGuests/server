-- +goose Up

REVOKE UPDATE ON app.bookings FROM __APP_ROLE__;
GRANT UPDATE (status, decided_at) ON app.bookings TO __APP_ROLE__;

DROP POLICY bookings_update_host ON app.bookings;
CREATE POLICY bookings_update_host ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (
        status = 'requested'
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.host_id = app.current_user_id()
        )
    )
    WITH CHECK (
        status IN ('pending', 'declined')
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.host_id = app.current_user_id()
        )
    );

-- +goose StatementBegin
CREATE FUNCTION app.keep_booked_listings_published()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM app.bookings b
        WHERE b.listing_id = OLD.id
          AND b.status IN ('requested', 'pending', 'confirmed')
          AND b.ends_on >= current_date
    ) THEN
        RAISE EXCEPTION 'listing has current or upcoming bookings' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER listings_keep_booked_published
    BEFORE UPDATE OF status ON app.listings
    FOR EACH ROW
    WHEN (OLD.status = 'published' AND NEW.status <> 'published')
    EXECUTE FUNCTION app.keep_booked_listings_published();

-- +goose Down

DROP TRIGGER listings_keep_booked_published ON app.listings;
DROP FUNCTION app.keep_booked_listings_published();

DROP POLICY bookings_update_host ON app.bookings;
CREATE POLICY bookings_update_host ON app.bookings
    FOR UPDATE TO __APP_ROLE__
    USING (
        EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.host_id = app.current_user_id()
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = listing_id AND l.host_id = app.current_user_id()
        )
    );

REVOKE UPDATE (status, decided_at) ON app.bookings FROM __APP_ROLE__;
GRANT UPDATE ON app.bookings TO __APP_ROLE__;
