-- +goose Up


CREATE TABLE app.identity_verifications (
    id                  uuid PRIMARY KEY,
    account_id          uuid NOT NULL REFERENCES app.accounts(id),
    persona_inquiry_id  text NOT NULL UNIQUE CHECK (length(persona_inquiry_id) BETWEEN 1 AND 200),
    status              text NOT NULL CHECK (status IN ('created', 'pending', 'needs_review', 'completed', 'failed', 'declined', 'expired')),
    last_event_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX identity_verifications_account_id_idx ON app.identity_verifications (account_id);
CREATE INDEX identity_verifications_account_status_idx ON app.identity_verifications (account_id, status);

ALTER TABLE app.identity_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.identity_verifications FORCE ROW LEVEL SECURITY;

CREATE POLICY identity_verifications_select ON app.identity_verifications
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY identity_verifications_insert ON app.identity_verifications
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (account_id = app.current_user_id());

CREATE POLICY identity_verifications_update ON app.identity_verifications
    FOR UPDATE TO __APP_ROLE__
    USING (account_id = app.current_user_id())
    WITH CHECK (account_id = app.current_user_id());

GRANT SELECT, INSERT, UPDATE ON app.identity_verifications TO __APP_ROLE__;


-- +goose Down

DROP TABLE IF EXISTS app.identity_verifications;
