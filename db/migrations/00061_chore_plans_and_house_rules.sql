-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN chore_plan jsonb
        CHECK (chore_plan IS NULL OR (jsonb_typeof(chore_plan) = 'array' AND pg_column_size(chore_plan) <= 131072)),
    ADD COLUMN house_rules_set boolean NOT NULL DEFAULT false;

ALTER TABLE app.bookings
    ADD COLUMN chore_plan jsonb
        CHECK (chore_plan IS NULL OR (jsonb_typeof(chore_plan) = 'array' AND pg_column_size(chore_plan) <= 131072));

-- +goose StatementBegin
CREATE FUNCTION app.copy_chore_plan_to_booking()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    NEW.chore_plan := (SELECT l.chore_plan FROM app.listings l WHERE l.id = NEW.listing_id);
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.keep_booking_chore_plan()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'the chores agreed at booking cannot change' USING ERRCODE = '42501';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER bookings_copy_chore_plan
    BEFORE INSERT ON app.bookings
    FOR EACH ROW EXECUTE FUNCTION app.copy_chore_plan_to_booking();

CREATE TRIGGER bookings_keep_chore_plan
    BEFORE UPDATE ON app.bookings
    FOR EACH ROW
    WHEN (OLD.chore_plan IS DISTINCT FROM NEW.chore_plan)
    EXECUTE FUNCTION app.keep_booking_chore_plan();

-- +goose StatementBegin
CREATE FUNCTION app.settlement_chore_plan(settling_booking_id uuid)
RETURNS jsonb
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.chore_plan FROM app.bookings b WHERE b.id = settling_booking_id
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.settlement_chore_plan(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.settlement_chore_plan(uuid) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.settlement_chore_plan(uuid);
DROP TRIGGER bookings_keep_chore_plan ON app.bookings;
DROP TRIGGER bookings_copy_chore_plan ON app.bookings;
DROP FUNCTION app.keep_booking_chore_plan();
DROP FUNCTION app.copy_chore_plan_to_booking();
ALTER TABLE app.bookings DROP COLUMN chore_plan;
ALTER TABLE app.listings DROP COLUMN house_rules_set, DROP COLUMN chore_plan;
