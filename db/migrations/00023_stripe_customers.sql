-- +goose Up

CREATE TABLE app.stripe_customers (
    account_id          uuid PRIMARY KEY REFERENCES app.accounts(id),
    stripe_customer_id  text NOT NULL UNIQUE CHECK (length(stripe_customer_id) BETWEEN 1 AND 200),
    created_at          timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE app.stripe_customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.stripe_customers FORCE ROW LEVEL SECURITY;

CREATE POLICY stripe_customers_select ON app.stripe_customers
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY stripe_customers_insert ON app.stripe_customers
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

GRANT SELECT, INSERT ON app.stripe_customers TO __APP_ROLE__;

-- +goose Down

DROP TABLE IF EXISTS app.stripe_customers;
