-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.require_account_sign_up() RETURNS trigger
    LANGUAGE plpgsql
    SECURITY INVOKER
    SET search_path = pg_catalog, app
AS $$
BEGIN
    IF NEW.clerk_user_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.clerk_user_id IS NULL THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'an account cannot lose its sign-up'
            USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM app.accounts WHERE id = NEW.id) THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM app.admin_accounts WHERE account_id = NEW.id) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'a new account needs a sign-up (clerk_user_id)'
        USING ERRCODE = 'check_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER accounts_require_sign_up
    BEFORE INSERT OR UPDATE OF clerk_user_id ON app.accounts
    FOR EACH ROW EXECUTE FUNCTION app.require_account_sign_up();

-- +goose Down

DROP TRIGGER accounts_require_sign_up ON app.accounts;
DROP FUNCTION app.require_account_sign_up();
