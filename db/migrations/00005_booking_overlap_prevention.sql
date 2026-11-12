-- +goose Up

-- Prevents two pending/confirmed bookings for the same listing from ever
-- covering overlapping date ranges, even under concurrent requests. A
-- service-level "check existing bookings, then insert" has a race: two
-- requests can both pass the check before either commits. An exclusion
-- constraint is enforced by the database itself as part of the insert, so
-- the second of two concurrent conflicting inserts fails atomically instead
-- of racing.
--
-- btree_gist is a trusted extension (installable by a role with CREATE on
-- the target schema, no superuser needed) that adds the "=" operator class
-- GiST needs to also exclude on listing_id, not just on date-range overlap.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- Cancelled bookings never block a new booking for the same dates; only
-- pending/confirmed bookings represent a real hold on the listing's
-- calendar. The '[)' bound makes end-on-day-N / start-on-day-N back-to-back
-- bookings not overlap, matching a checkout-morning / check-in-afternoon
-- turnover.
ALTER TABLE app.bookings
    ADD CONSTRAINT bookings_no_overlap
    EXCLUDE USING gist (
        listing_id WITH =,
        daterange(starts_on, ends_on, '[)') WITH &&
    )
    WHERE (status IN ('pending', 'confirmed'));

-- +goose Down

ALTER TABLE app.bookings DROP CONSTRAINT IF EXISTS bookings_no_overlap;
