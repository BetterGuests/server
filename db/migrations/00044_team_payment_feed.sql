-- +goose Up

CREATE POLICY payments_team_select ON app.payments
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

CREATE INDEX payments_updated_at_idx ON app.payments (updated_at);
CREATE INDEX bookings_created_at_idx ON app.bookings (created_at);

-- +goose Down

DROP INDEX app.bookings_created_at_idx;
DROP INDEX app.payments_updated_at_idx;
DROP POLICY payments_team_select ON app.payments;
