-- +goose Up

ALTER TABLE app.property_proofs ADD COLUMN auto_approved_at timestamptz;

ALTER TABLE app.property_proofs DROP CONSTRAINT property_proofs_check;

ALTER TABLE app.property_proofs ADD CONSTRAINT property_proofs_decision_recorded CHECK (
    CASE WHEN status = 'submitted'
        THEN reviewed_by IS NULL AND reviewed_at IS NULL AND auto_approved_at IS NULL
        ELSE reviewed_at IS NOT NULL AND (reviewed_by IS NOT NULL OR auto_approved_at IS NOT NULL)
    END
);

ALTER TABLE app.property_proof_scans ADD COLUMN document_date date;

DROP FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz);

-- +goose StatementBegin
CREATE FUNCTION app.record_property_proof_scan(
    target_id uuid,
    name_found text,
    verdict text,
    address_found boolean,
    text_excerpt text,
    failure text,
    scanned timestamptz,
    latest_date date,
    auto_approve boolean
)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    proof_status text;
    proof_host uuid;
    proof_type text;
    proof_relationship text;
    proof_owner text;
    submitted_on date;
    identity_current boolean;
BEGIN
    SELECT p.status, p.host_id, p.document_type, p.relationship, coalesce(p.owner_name, ''),
           (p.created_at AT TIME ZONE 'UTC')::date
    INTO proof_status, proof_host, proof_type, proof_relationship, proof_owner, submitted_on
    FROM app.property_proofs p WHERE p.id = target_id FOR UPDATE;
    IF NOT FOUND OR proof_status <> 'submitted' THEN
        RETURN 'skipped';
    END IF;

    INSERT INTO app.property_proof_scans
        (proof_id, found_name, name_verdict, address_matched, excerpt, scan_error, scanned_at, document_date)
    VALUES (target_id, nullif(name_found, ''), verdict, address_found, nullif(text_excerpt, ''), nullif(failure, ''), scanned, latest_date)
    ON CONFLICT (proof_id) DO NOTHING;
    IF NOT FOUND THEN
        RETURN 'skipped';
    END IF;

    IF NOT auto_approve
       OR verdict <> 'match'
       OR NOT address_found
       OR nullif(failure, '') IS NOT NULL
       OR proof_type <> 'utility_bill'
       OR proof_relationship <> 'owner'
       OR proof_owner <> ''
       OR latest_date IS NULL
       OR latest_date < submitted_on - 90
       OR latest_date > (scanned AT TIME ZONE 'UTC')::date THEN
        RETURN 'recorded';
    END IF;

    SELECT coalesce(bool_and(v.status = 'completed' AND v.revoked_at IS NULL
                             AND (v.review_state IS NULL OR v.review_state = 'cleared')), false)
    INTO identity_current
    FROM (
        SELECT n.verification_id
        FROM app.identity_names n
        WHERE n.account_id = proof_host
        ORDER BY n.verified_at DESC, n.verification_id
        LIMIT 1
    ) latest
    JOIN app.identity_verifications v ON v.id = latest.verification_id;
    IF NOT identity_current THEN
        RETURN 'recorded';
    END IF;

    UPDATE app.property_proofs p
    SET status = 'approved', reviewed_at = scanned, auto_approved_at = scanned, updated_at = scanned
    WHERE p.id = target_id;
    RETURN 'approved';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz, date, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz, date, boolean) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz, date, boolean);

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

REVOKE ALL ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.record_property_proof_scan(uuid, text, text, boolean, text, text, timestamptz) TO __APP_ROLE__;

ALTER TABLE app.property_proof_scans DROP COLUMN document_date;

UPDATE app.property_proofs
SET status = 'submitted', reviewed_at = NULL, auto_approved_at = NULL
WHERE reviewed_by IS NULL AND auto_approved_at IS NOT NULL;

ALTER TABLE app.property_proofs DROP CONSTRAINT property_proofs_decision_recorded;

ALTER TABLE app.property_proofs ADD CONSTRAINT property_proofs_check
    CHECK ((status = 'submitted') = (reviewed_by IS NULL AND reviewed_at IS NULL));

ALTER TABLE app.property_proofs DROP COLUMN auto_approved_at;
