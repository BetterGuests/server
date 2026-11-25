-- +goose Up

ALTER TABLE app.payments DROP CONSTRAINT payments_status_check;
ALTER TABLE app.payments ADD CONSTRAINT payments_status_check
    CHECK (status IN ('pending', 'authorized', 'succeeded', 'failed', 'canceled'));

-- +goose Down

UPDATE app.payments SET status = 'pending' WHERE status = 'authorized';
ALTER TABLE app.payments DROP CONSTRAINT payments_status_check;
ALTER TABLE app.payments ADD CONSTRAINT payments_status_check
    CHECK (status IN ('pending', 'succeeded', 'failed', 'canceled'));
