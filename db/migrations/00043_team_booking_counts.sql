-- +goose Up

CREATE POLICY bookings_team_select ON app.bookings
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

CREATE INDEX bookings_listing_id_created_at_idx ON app.bookings (listing_id, created_at);

-- +goose Down

DROP INDEX app.bookings_listing_id_created_at_idx;
DROP POLICY bookings_team_select ON app.bookings;
