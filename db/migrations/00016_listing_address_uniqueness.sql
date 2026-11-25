-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN address_key text GENERATED ALWAYS AS (
        CASE
            WHEN nullif(btrim(address_line1), '') IS NULL THEN NULL
            ELSE country_code
                || '|' || lower(regexp_replace(coalesce(postal_code, ''), '[^[:alnum:]]+', '', 'g'))
                || '|' || lower(btrim(regexp_replace(city, '[^[:alnum:]]+', ' ', 'g')))
                || '|' || lower(btrim(regexp_replace(address_line1, '[^[:alnum:]]+', ' ', 'g')))
        END
    ) STORED,
    ADD COLUMN unit_key text GENERATED ALWAYS AS (
        lower(regexp_replace(coalesce(address_line2, ''), '[^[:alnum:]]+', '', 'g'))
    ) STORED;

ALTER TABLE app.listings NO FORCE ROW LEVEL SECURITY;

UPDATE app.listings AS listing
SET status = 'paused', updated_at = now()
FROM (
    SELECT id,
           row_number() OVER (PARTITION BY address_key, unit_key ORDER BY created_at, id) AS position
    FROM app.listings
    WHERE status = 'published' AND address_key IS NOT NULL
) AS ranked
WHERE listing.id = ranked.id AND ranked.position > 1;

UPDATE app.listings AS listing
SET status = 'paused', updated_at = now()
FROM (
    SELECT id,
           row_number() OVER (PARTITION BY place_id, unit_key ORDER BY created_at, id) AS position
    FROM app.listings
    WHERE status = 'published' AND place_id IS NOT NULL
) AS ranked
WHERE listing.id = ranked.id AND ranked.position > 1;

ALTER TABLE app.listings FORCE ROW LEVEL SECURITY;

CREATE UNIQUE INDEX listings_published_address_idx
    ON app.listings (address_key, unit_key)
    WHERE status = 'published' AND address_key IS NOT NULL;

CREATE UNIQUE INDEX listings_published_place_idx
    ON app.listings (place_id, unit_key)
    WHERE status = 'published' AND place_id IS NOT NULL;

-- +goose Down

DROP INDEX app.listings_published_place_idx;
DROP INDEX app.listings_published_address_idx;

ALTER TABLE app.listings
    DROP COLUMN unit_key,
    DROP COLUMN address_key;
