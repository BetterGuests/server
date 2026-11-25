-- +goose Up

CREATE TABLE app.property_proof_inquiries (
    id                  uuid PRIMARY KEY,
    persona_inquiry_id  text NOT NULL UNIQUE CHECK (length(persona_inquiry_id) BETWEEN 5 AND 100),
    listing_id          uuid NOT NULL REFERENCES app.listings(id) ON DELETE CASCADE,
    host_id             uuid NOT NULL REFERENCES app.accounts(id),
    document_type       text NOT NULL CHECK (document_type IN ('utility_bill', 'deed', 'lease', 'property_tax_bill')),
    address_key         text,
    unit_key            text,
    created_at          timestamptz NOT NULL
);

CREATE INDEX property_proof_inquiries_listing_id_idx ON app.property_proof_inquiries (listing_id);
CREATE INDEX property_proof_inquiries_host_id_idx ON app.property_proof_inquiries (host_id);

ALTER TABLE app.property_proof_inquiries ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.property_proof_inquiries FORCE ROW LEVEL SECURITY;

CREATE POLICY property_proof_inquiries_select ON app.property_proof_inquiries
    FOR SELECT TO __APP_ROLE__
    USING (host_id = app.current_user_id());

CREATE POLICY property_proof_inquiries_insert ON app.property_proof_inquiries
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (
        host_id = app.current_user_id()
        AND EXISTS (
            SELECT 1 FROM app.listings l
            WHERE l.id = property_proof_inquiries.listing_id AND l.host_id = app.current_user_id()
        )
    );

GRANT SELECT, INSERT ON app.property_proof_inquiries TO __APP_ROLE__;

ALTER TABLE app.property_proofs
    ADD COLUMN persona_inquiry_id text UNIQUE REFERENCES app.property_proof_inquiries(persona_inquiry_id),
    ADD COLUMN extra_object_keys text[] NOT NULL DEFAULT '{}' CHECK (cardinality(extra_object_keys) <= 9);

CREATE POLICY property_proofs_migrator_select ON app.property_proofs
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY property_proofs_migrator_update ON app.property_proofs
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

CREATE POLICY property_proof_inquiries_migrator_select ON app.property_proof_inquiries
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY listings_migrator_proof_select ON app.listings
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.approve_persona_property_proof(target_inquiry text, decided_at timestamptz)
RETURNS boolean
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH approved AS (
        UPDATE app.property_proofs p
        SET status = 'approved', reviewed_at = decided_at, updated_at = decided_at
        FROM app.property_proof_inquiries i, app.listings l
        WHERE p.persona_inquiry_id = target_inquiry
          AND i.persona_inquiry_id = p.persona_inquiry_id
          AND l.id = p.listing_id
          AND p.host_id = app.current_user_id()
          AND i.host_id = p.host_id
          AND i.listing_id = p.listing_id
          AND l.host_id = p.host_id
          AND p.status = 'submitted'
          AND i.address_key IS NOT DISTINCT FROM l.address_key
          AND i.unit_key IS NOT DISTINCT FROM l.unit_key
        RETURNING p.id
    )
    SELECT EXISTS (SELECT 1 FROM approved)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.approve_persona_property_proof(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.approve_persona_property_proof(text, timestamptz) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.approve_persona_property_proof(text, timestamptz);
DROP POLICY listings_migrator_proof_select ON app.listings;
DROP POLICY property_proof_inquiries_migrator_select ON app.property_proof_inquiries;
DROP POLICY property_proofs_migrator_update ON app.property_proofs;
DROP POLICY property_proofs_migrator_select ON app.property_proofs;
ALTER TABLE app.property_proofs DROP COLUMN extra_object_keys, DROP COLUMN persona_inquiry_id;
DROP TABLE app.property_proof_inquiries;
