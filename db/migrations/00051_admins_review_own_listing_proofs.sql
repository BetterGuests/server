-- +goose Up

DROP POLICY property_proofs_review ON app.property_proofs;

CREATE POLICY property_proofs_review ON app.property_proofs
    FOR UPDATE TO __APP_ROLE__
    USING (
        app.is_admin()
        AND status = 'submitted'
    )
    WITH CHECK (
        app.is_admin()
        AND status IN ('approved', 'rejected')
        AND reviewed_by = app.current_user_id()
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.revoke_property_proof(target_proof uuid, revoke_reason text, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    proof_status text;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT p.status INTO proof_status
    FROM app.property_proofs p WHERE p.id = target_proof FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF proof_status <> 'approved' THEN
        RETURN 'wrong_state';
    END IF;
    UPDATE app.property_proofs p
    SET status = 'rejected', rejection_reason = revoke_reason, revoked_at = decided,
        reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
    WHERE p.id = target_proof;
    RETURN 'done';
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP POLICY property_proofs_review ON app.property_proofs;

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.revoke_property_proof(target_proof uuid, revoke_reason text, decided timestamptz)
RETURNS text
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    proof_host uuid;
    proof_status text;
BEGIN
    IF NOT app.is_admin() THEN
        RETURN 'forbidden';
    END IF;
    SELECT p.host_id, p.status INTO proof_host, proof_status
    FROM app.property_proofs p WHERE p.id = target_proof FOR UPDATE;
    IF NOT FOUND THEN
        RETURN 'not_found';
    END IF;
    IF proof_host = app.current_user_id() THEN
        RETURN 'own';
    END IF;
    IF proof_status <> 'approved' THEN
        RETURN 'wrong_state';
    END IF;
    UPDATE app.property_proofs p
    SET status = 'rejected', rejection_reason = revoke_reason, revoked_at = decided,
        reviewed_by = app.current_user_id(), reviewed_at = decided, updated_at = decided
    WHERE p.id = target_proof;
    RETURN 'done';
END
$$;
-- +goose StatementEnd
