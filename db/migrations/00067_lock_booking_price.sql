-- +goose Up

ALTER TABLE app.bookings
    ADD COLUMN nightly_price_cents integer
    CHECK (nightly_price_cents IS NULL OR nightly_price_cents BETWEEN 100 AND 10000000);

ALTER TABLE app.bookings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.listings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.payments NO FORCE ROW LEVEL SECURITY;

UPDATE app.bookings b
SET nightly_price_cents = COALESCE(
    (
        SELECT (p.amount_cents - p.guest_fee_cents) / (b.ends_on - b.starts_on)
        FROM app.payments p
        WHERE p.booking_id = b.id
          AND (p.amount_cents - p.guest_fee_cents) % (b.ends_on - b.starts_on) = 0
          AND (p.amount_cents - p.guest_fee_cents) / (b.ends_on - b.starts_on) BETWEEN 100 AND 10000000
    ),
    (SELECT l.nightly_price_cents FROM app.listings l WHERE l.id = b.listing_id)
);

ALTER TABLE app.bookings FORCE ROW LEVEL SECURITY;
ALTER TABLE app.listings FORCE ROW LEVEL SECURITY;
ALTER TABLE app.payments FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE FUNCTION app.lock_booking_price()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    SELECT l.nightly_price_cents INTO NEW.nightly_price_cents
    FROM app.listings l
    WHERE l.id = NEW.listing_id;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER bookings_lock_price
    BEFORE INSERT ON app.bookings
    FOR EACH ROW
    EXECUTE FUNCTION app.lock_booking_price();

-- +goose Down

DROP TRIGGER bookings_lock_price ON app.bookings;
DROP FUNCTION app.lock_booking_price();
ALTER TABLE app.bookings DROP COLUMN nightly_price_cents;
