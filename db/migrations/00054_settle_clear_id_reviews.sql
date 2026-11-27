-- +goose Up

ALTER TABLE app.property_proofs DROP CONSTRAINT property_proofs_decision_recorded;

ALTER TABLE app.property_proofs ADD CONSTRAINT property_proofs_decision_recorded CHECK (
    CASE WHEN status = 'submitted'
        THEN reviewed_by IS NULL AND reviewed_at IS NULL AND auto_approved_at IS NULL
        ELSE reviewed_at IS NOT NULL
             AND (reviewed_by IS NOT NULL OR auto_approved_at IS NOT NULL OR revoked_at IS NOT NULL)
    END
);

-- +goose StatementBegin
CREATE FUNCTION app.settle_identity_review(target_verification uuid, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    held app.identity_verifications%ROWTYPE;
BEGIN
    SELECT * INTO held FROM app.identity_verifications v
    WHERE v.id = target_verification AND v.account_id = app.current_user_id()
    FOR UPDATE;
    IF NOT FOUND OR held.review_state IS DISTINCT FROM 'pending' OR held.status <> 'completed' THEN
        RETURN 'held';
    END IF;
    IF 'under_age' = ANY (held.review_reasons) THEN
        UPDATE app.identity_verifications v
        SET review_state = 'rejected', reviewed_at = decided, updated_at = decided
        WHERE v.id = held.id;
        UPDATE app.identity_verifications v
        SET revoked_at = decided, updated_at = decided
        WHERE v.account_id = held.account_id AND v.id <> held.id
          AND v.status = 'completed' AND v.revoked_at IS NULL;
        RETURN 'rejected';
    END IF;
    IF held.review_reasons = ARRAY['name_unconfirmed'] THEN
        UPDATE app.identity_verifications v
        SET review_state = 'cleared', reviewed_at = decided, updated_at = decided
        WHERE v.id = held.id;
        UPDATE app.property_proofs p
        SET status = 'rejected', rejection_reason = 'The name on your ID changed. Send this document again.',
            revoked_at = decided, reviewed_at = decided, updated_at = decided
        WHERE p.host_id = held.account_id AND p.status = 'approved';
        RETURN 'cleared';
    END IF;
    RETURN 'held';
END
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.settle_identity_review(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.settle_identity_review(uuid, timestamptz) TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.identity_review_inquiries(target_verification uuid)
RETURNS TABLE (role text, persona_inquiry_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    WITH target AS (
        SELECT v.id, v.account_id, v.persona_inquiry_id
        FROM app.identity_verifications v
        WHERE app.is_admin() AND v.id = target_verification
          AND v.review_state = 'pending' AND v.status = 'completed'
    )
    SELECT 'this', t.persona_inquiry_id FROM target t
    UNION ALL
    (SELECT 'previous', old.persona_inquiry_id
     FROM target t
     JOIN app.identity_verifications old ON old.account_id = t.account_id AND old.id <> t.id
     WHERE old.status = 'completed' AND old.redacted_at IS NULL AND old.deletion_requested_at IS NULL
     ORDER BY COALESCE(old.last_event_at, old.updated_at) DESC, old.id
     LIMIT 1)
    UNION ALL
    (SELECT 'other_account', other.persona_inquiry_id
     FROM target t
     JOIN app.identity_fingerprints mine ON mine.verification_id = t.id
     JOIN app.identity_fingerprints theirs ON theirs.fingerprint = mine.fingerprint AND theirs.account_id <> t.account_id
     JOIN app.identity_verifications other ON other.id = theirs.verification_id
     WHERE other.redacted_at IS NULL AND other.deletion_requested_at IS NULL
     ORDER BY COALESCE(other.last_event_at, other.updated_at) DESC, other.id
     LIMIT 1)
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.identity_review_inquiries(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.identity_review_inquiries(uuid) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.identity_review_inquiries(uuid);
DROP FUNCTION app.settle_identity_review(uuid, timestamptz);

ALTER TABLE app.property_proofs DROP CONSTRAINT property_proofs_decision_recorded;

ALTER TABLE app.property_proofs ADD CONSTRAINT property_proofs_decision_recorded CHECK (
    CASE WHEN status = 'submitted'
        THEN reviewed_by IS NULL AND reviewed_at IS NULL AND auto_approved_at IS NULL
        ELSE reviewed_at IS NOT NULL AND (reviewed_by IS NOT NULL OR auto_approved_at IS NOT NULL)
    END
);
