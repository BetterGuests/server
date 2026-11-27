-- +goose Up

CREATE TABLE app.property_proof_scans (
    proof_id        uuid PRIMARY KEY REFERENCES app.property_proofs (id) ON DELETE CASCADE,
    found_name      text CHECK (found_name IS NULL OR length(found_name) <= 200),
    name_verdict    text NOT NULL CHECK (name_verdict IN ('match', 'look-closer', 'no-match', 'not-found')),
    address_matched boolean NOT NULL,
    excerpt         text CHECK (excerpt IS NULL OR length(excerpt) <= 600),
    scan_error      text CHECK (scan_error IS NULL OR length(scan_error) <= 300),
    scanned_at      timestamptz NOT NULL
);

CREATE INDEX property_proof_scans_scanned_at_idx ON app.property_proof_scans (scanned_at);

ALTER TABLE app.property_proof_scans ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.property_proof_scans FORCE ROW LEVEL SECURITY;

CREATE POLICY property_proof_scans_admin_select ON app.property_proof_scans
    FOR SELECT TO __APP_ROLE__
    USING (app.is_admin());

CREATE POLICY property_proof_scans_migrator_select ON app.property_proof_scans
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY property_proof_scans_migrator_insert ON app.property_proof_scans
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY property_proof_scans_migrator_update ON app.property_proof_scans
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

GRANT SELECT ON app.property_proof_scans TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.property_proofs_due_for_scan(batch_size integer)
RETURNS TABLE (
    id uuid,
    object_key text,
    content_type text,
    owner_name text,
    id_name text,
    address_line1 text,
    city text,
    postal_code text
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT p.id, p.object_key, coalesce(p.content_type, ''), coalesce(p.owner_name, ''),
        concat_ws(' ', nullif(n.first_name, ''), nullif(n.middle_name, ''), nullif(n.last_name, ''), nullif(n.suffix, '')),
        coalesce(l.address_line1, ''), coalesce(l.city, ''), coalesce(l.postal_code, '')
    FROM app.property_proofs p
    JOIN app.listings l ON l.id = p.listing_id
    LEFT JOIN LATERAL (
        SELECT first_name, middle_name, last_name, suffix
        FROM app.identity_names
        WHERE account_id = p.host_id
        ORDER BY verified_at DESC, verification_id
        LIMIT 1
    ) n ON true
    WHERE p.status = 'submitted'
      AND p.object_key IS NOT NULL
      AND p.file_deleted_at IS NULL
      AND NOT EXISTS (SELECT 1 FROM app.property_proof_scans s WHERE s.proof_id = p.id)
    ORDER BY p.created_at, p.id
    LIMIT least(greatest(batch_size, 1), 100)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_property_proof_scan(
    target_id uuid,
    found text,
    verdict text,
    address_found boolean,
    text_excerpt text,
    failure text,
    scanned timestamptz
)
RETURNS void
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    INSERT INTO app.property_proof_scans
        (proof_id, found_name, name_verdict, address_matched, excerpt, scan_error, scanned_at)
    SELECT target_id, nullif(found, ''), verdict, address_found, nullif(text_excerpt, ''), nullif(failure, ''), scanned
    WHERE EXISTS (SELECT 1 FROM app.property_proofs p WHERE p.id = target_id AND p.status = 'submitted')
    ON CONFLICT (proof_id) DO NOTHING
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.forget_property_proof_scan_text()
RETURNS integer
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH cleared AS (
        UPDATE app.property_proof_scans s
        SET found_name = NULL, excerpt = NULL
        FROM app.property_proofs p
        WHERE p.id = s.proof_id
          AND p.file_deleted_at IS NOT NULL
          AND (s.found_name IS NOT NULL OR s.excerpt IS NOT NULL)
        RETURNING 1
    )
    SELECT count(*)::integer FROM cleared
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.property_proofs_due_for_scan(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.property_proofs_due_for_scan(integer) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz) TO __APP_ROLE__;
REVOKE ALL ON FUNCTION app.forget_property_proof_scan_text() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.forget_property_proof_scan_text() TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.forget_property_proof_scan_text();
DROP FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz);
DROP FUNCTION app.property_proofs_due_for_scan(integer);
DROP TABLE app.property_proof_scans;
