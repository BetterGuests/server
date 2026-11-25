-- +goose Up

CREATE POLICY listings_availability_select ON app.listings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (status = 'published');

CREATE POLICY bookings_availability_select ON app.bookings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (status IN ('requested', 'pending', 'confirmed'));

-- +goose StatementBegin
CREATE FUNCTION app.listing_booked_dates(target_listing_id uuid, earliest_checkout date, range_limit integer)
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
      AND b.status IN ('requested', 'pending', 'confirmed')
      AND b.ends_on >= earliest_checkout
    ORDER BY b.starts_on
    LIMIT least(greatest(range_limit, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.listing_booked_dates(uuid, date, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.listing_booked_dates(uuid, date, integer) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.listing_booked_dates(uuid, date, integer);
DROP POLICY bookings_availability_select ON app.bookings;
DROP POLICY listings_availability_select ON app.listings;
