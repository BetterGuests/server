-- +goose Up

CREATE TABLE app.admin_accounts (
    account_id  uuid PRIMARY KEY,
    email       text NOT NULL CHECK (length(email) BETWEEN 3 AND 254),
    granted_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX admin_accounts_email_idx ON app.admin_accounts (lower(email));

ALTER TABLE app.admin_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.admin_accounts FORCE ROW LEVEL SECURITY;

CREATE POLICY admin_accounts_select_self ON app.admin_accounts
    FOR SELECT TO __APP_ROLE__
    USING (account_id = app.current_user_id());

CREATE POLICY admin_accounts_migrator_select ON app.admin_accounts
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY admin_accounts_migrator_insert ON app.admin_accounts
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY admin_accounts_migrator_delete ON app.admin_accounts
    FOR DELETE TO __MIGRATOR_ROLE__
    USING (true);

GRANT SELECT ON app.admin_accounts TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.is_admin()
RETURNS boolean
LANGUAGE sql
STABLE
PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (
        SELECT 1 FROM app.admin_accounts WHERE account_id = app.current_user_id()
    )
$$;

REVOKE ALL ON FUNCTION app.is_admin() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.is_admin() TO __APP_ROLE__;
-- +goose StatementEnd

CREATE TABLE app.property_proofs (
    id               uuid PRIMARY KEY,
    listing_id       uuid NOT NULL REFERENCES app.listings(id),
    host_id          uuid NOT NULL REFERENCES app.accounts(id),
    document_type    text NOT NULL CHECK (document_type IN ('utility_bill', 'deed', 'lease', 'property_tax_bill')),
    object_key       text NOT NULL UNIQUE CHECK (length(object_key) BETWEEN 1 AND 300),
    content_type     text NOT NULL CHECK (content_type IN ('application/pdf', 'image/jpeg', 'image/png', 'image/webp', 'image/heic')),
    size_bytes       bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 15728640),
    address_key      text,
    unit_key         text,
    status           text NOT NULL CHECK (status IN ('submitted', 'approved', 'rejected')),
    reviewed_by      uuid REFERENCES app.accounts(id),
    reviewed_at      timestamptz,
    rejection_reason text CHECK (rejection_reason IS NULL OR length(rejection_reason) BETWEEN 1 AND 500),
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    CHECK ((status = 'submitted') = (reviewed_by IS NULL AND reviewed_at IS NULL)),
    CHECK ((status = 'rejected') = (rejection_reason IS NOT NULL))
);

CREATE INDEX property_proofs_listing_id_idx ON app.property_proofs (listing_id);
CREATE INDEX property_proofs_host_id_idx ON app.property_proofs (host_id);
CREATE INDEX property_proofs_status_created_idx ON app.property_proofs (status, created_at);
CREATE UNIQUE INDEX property_proofs_one_submitted_per_listing_idx
    ON app.property_proofs (listing_id) WHERE status = 'submitted';

ALTER TABLE app.property_proofs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.property_proofs FORCE ROW LEVEL SECURITY;

CREATE POLICY property_proofs_select ON app.property_proofs
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id() OR app.is_admin());

CREATE POLICY property_proofs_insert ON app.property_proofs
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND status = 'submitted'
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = property_proofs.listing_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY property_proofs_review ON app.property_proofs
    FOR UPDATE TO __APP_ROLE__
    USING (
        app.is_admin()
        AND status = 'submitted'
        AND host_id <> app.current_user_id()
    )
    WITH CHECK (
        app.is_admin()
        AND status IN ('approved', 'rejected')
        AND reviewed_by = app.current_user_id()
    );

GRANT SELECT, INSERT ON app.property_proofs TO __APP_ROLE__;
GRANT UPDATE (status, reviewed_by, reviewed_at, rejection_reason, updated_at)
    ON app.property_proofs TO __APP_ROLE__;

CREATE POLICY listings_admin_select ON app.listings
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

-- +goose Down

DROP POLICY listings_admin_select ON app.listings;
DROP TABLE app.property_proofs;
DROP FUNCTION app.is_admin();
DROP TABLE app.admin_accounts;
