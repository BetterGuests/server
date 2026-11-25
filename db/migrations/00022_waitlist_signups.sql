-- +goose Up

CREATE TABLE app.waitlist_signups (
    id          uuid PRIMARY KEY,
    email       text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    country     text CHECK (country ~ '^[A-Z][A-Z0-9]$'),
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE app.waitlist_signups ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.waitlist_signups FORCE ROW LEVEL SECURITY;

CREATE POLICY waitlist_signups_insert ON app.waitlist_signups
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (true);

GRANT INSERT ON app.waitlist_signups TO __APP_ROLE__;

-- +goose Down

DROP TABLE app.waitlist_signups;
