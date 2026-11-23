-- +goose Up

CREATE TABLE app.age_confirmations (
    account_id     uuid PRIMARY KEY REFERENCES app.accounts(id),
    date_of_birth  date NOT NULL CHECK (date_of_birth <= (CURRENT_DATE - INTERVAL '18 years')::date AND date_of_birth >= DATE '1900-01-01'),
    confirmed_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE app.age_confirmations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.age_confirmations FORCE ROW LEVEL SECURITY;

CREATE POLICY age_confirmations_select ON app.age_confirmations
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY age_confirmations_insert ON app.age_confirmations
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

GRANT SELECT, INSERT ON app.age_confirmations TO __APP_ROLE__;

CREATE TABLE app.consents (
    id              uuid PRIMARY KEY,
    account_id      uuid NOT NULL REFERENCES app.accounts(id),
    kind            text NOT NULL CHECK (kind IN ('biometric')),
    policy_version  text NOT NULL CHECK (length(policy_version) BETWEEN 1 AND 40),
    granted_at      timestamptz NOT NULL DEFAULT now(),
    withdrawn_at    timestamptz,
    CHECK (withdrawn_at IS NULL OR withdrawn_at >= granted_at),
    UNIQUE (account_id, kind, policy_version)
);

CREATE INDEX consents_account_kind_idx ON app.consents (account_id, kind);

ALTER TABLE app.consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.consents FORCE ROW LEVEL SECURITY;

CREATE POLICY consents_select ON app.consents
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY consents_insert ON app.consents
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY consents_update ON app.consents
    FOR UPDATE TO __APP_ROLE__
    USING (account_id = app.current_user_id())
    WITH CHECK (account_id = app.current_user_id());

GRANT SELECT, INSERT, UPDATE ON app.consents TO __APP_ROLE__;

-- +goose Down

DROP TABLE IF EXISTS app.consents;
DROP TABLE IF EXISTS app.age_confirmations;
