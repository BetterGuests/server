-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN host_name_follows_account boolean NOT NULL DEFAULT true;

ALTER TABLE app.listings NO FORCE ROW LEVEL SECURITY;

UPDATE app.listings SET host_name_follows_account = false WHERE host_name IS NOT NULL;

UPDATE app.listings AS l
SET host_name = n.display_name,
    host_avatar_url = CASE WHEN n.avatar_url ~ '^https://' AND length(n.avatar_url) <= 500 THEN n.avatar_url END
FROM app.account_names n
WHERE n.account_id = l.host_id AND l.host_name_follows_account;

ALTER TABLE app.listings FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE FUNCTION app.name_listing_after_host()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' AND NEW.host_name IS NOT NULL THEN
        NEW.host_name_follows_account := false;
    END IF;
    IF NEW.host_name_follows_account THEN
        NEW.host_name := NULL;
        NEW.host_avatar_url := NULL;
        SELECT n.display_name,
               CASE WHEN n.avatar_url ~ '^https://' AND length(n.avatar_url) <= 500 THEN n.avatar_url END
        INTO NEW.host_name, NEW.host_avatar_url
        FROM app.account_names n
        WHERE n.account_id = NEW.host_id;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.rename_listings_after_host()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    UPDATE app.listings
    SET host_name = NEW.display_name,
        host_avatar_url = CASE WHEN NEW.avatar_url ~ '^https://' AND length(NEW.avatar_url) <= 500 THEN NEW.avatar_url END
    WHERE host_id = NEW.account_id AND host_name_follows_account;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER listings_name_after_host
    BEFORE INSERT OR UPDATE OF host_id ON app.listings
    FOR EACH ROW EXECUTE FUNCTION app.name_listing_after_host();

CREATE TRIGGER account_names_rename_listings
    AFTER INSERT OR UPDATE OF display_name, avatar_url ON app.account_names
    FOR EACH ROW EXECUTE FUNCTION app.rename_listings_after_host();

-- +goose Down

DROP TRIGGER account_names_rename_listings ON app.account_names;
DROP TRIGGER listings_name_after_host ON app.listings;
DROP FUNCTION app.rename_listings_after_host();
DROP FUNCTION app.name_listing_after_host();

ALTER TABLE app.listings DROP COLUMN host_name_follows_account;
