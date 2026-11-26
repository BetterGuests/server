-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.stranded_booking_holds(batch_size integer)
RETURNS TABLE (booking_id uuid, guest_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT b.id, b.guest_id
    FROM app.bookings b
    JOIN app.payments p ON p.booking_id = b.id
    WHERE b.status IN ('declined', 'cancelled')
      AND p.status = 'authorized'
    ORDER BY p.updated_at, b.id
    LIMIT least(greatest(batch_size, 1), 500)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.stranded_booking_holds(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.stranded_booking_holds(integer) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.stranded_booking_holds(integer);
