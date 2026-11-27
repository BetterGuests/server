-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.listing_taken_down(target uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (
        SELECT 1 FROM app.listing_suspensions s
        WHERE s.listing_id = target AND s.paused_by = 'team' AND s.lifted_at IS NULL
    )
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.listing_taken_down(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.listing_taken_down(uuid) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.refuse_paused_approval()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF OLD.status = 'requested' AND NEW.status = 'pending' AND EXISTS (
        SELECT 1 FROM app.listing_suspensions s
        WHERE s.listing_id = NEW.listing_id AND s.lifted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'listing is paused' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.refuse_paused_approval() FROM PUBLIC;

CREATE TRIGGER bookings_refuse_paused_approval
    BEFORE UPDATE OF status ON app.bookings
    FOR EACH ROW
    EXECUTE FUNCTION app.refuse_paused_approval();

-- +goose Down

DROP TRIGGER bookings_refuse_paused_approval ON app.bookings;
DROP FUNCTION app.refuse_paused_approval();
DROP FUNCTION app.listing_taken_down(uuid);
