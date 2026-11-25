-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.refuse_overlapping_trip()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.guest_id::text, 0));
    IF EXISTS (
        SELECT 1 FROM app.bookings b
        WHERE b.guest_id = NEW.guest_id
          AND b.status IN ('requested', 'pending', 'confirmed')
          AND daterange(b.starts_on, b.ends_on, '[)') && daterange(NEW.starts_on, NEW.ends_on, '[)')
    ) THEN
        RAISE EXCEPTION 'guest already has a trip on these dates' USING ERRCODE = 'BG001';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.refuse_overlapping_trip() FROM PUBLIC;

CREATE TRIGGER bookings_one_trip_at_a_time
    BEFORE INSERT ON app.bookings
    FOR EACH ROW
    WHEN (NEW.status IN ('requested', 'pending', 'confirmed'))
    EXECUTE FUNCTION app.refuse_overlapping_trip();

-- +goose Down

DROP TRIGGER bookings_one_trip_at_a_time ON app.bookings;
DROP FUNCTION app.refuse_overlapping_trip();
