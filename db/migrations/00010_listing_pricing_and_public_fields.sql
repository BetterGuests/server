-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN slug               text,
    ADD COLUMN summary            text CHECK (summary IS NULL OR length(summary) <= 500),
    ADD COLUMN host_name          text CHECK (host_name IS NULL OR length(host_name) BETWEEN 1 AND 120),
    ADD COLUMN cleaning_fee_cents integer NOT NULL DEFAULT 0 CHECK (cleaning_fee_cents BETWEEN 0 AND 10000000),
    ADD COLUMN minimum_nights     integer NOT NULL DEFAULT 1 CHECK (minimum_nights BETWEEN 1 AND 365),
    ADD COLUMN currency           text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    ADD COLUMN rating_avg         numeric(3,2) CHECK (rating_avg IS NULL OR rating_avg BETWEEN 1 AND 5),
    ADD COLUMN rating_count       integer NOT NULL DEFAULT 0 CHECK (rating_count >= 0);

CREATE UNIQUE INDEX listings_slug_idx ON app.listings (slug) WHERE slug IS NOT NULL;

ALTER TABLE app.listings ALTER COLUMN address_line1 DROP NOT NULL;

ALTER TABLE app.listings DROP CONSTRAINT listings_property_type_check;
ALTER TABLE app.listings ADD CONSTRAINT listings_property_type_check
    CHECK (property_type IN ('house', 'apartment', 'condo', 'townhouse', 'villa', 'cabin', 'cottage', 'unique'));

-- +goose Down

ALTER TABLE app.listings DROP CONSTRAINT listings_property_type_check;
ALTER TABLE app.listings ADD CONSTRAINT listings_property_type_check
    CHECK (property_type IN ('house', 'apartment', 'condo', 'townhouse', 'villa'));

UPDATE app.listings SET address_line1 = '' WHERE address_line1 IS NULL;
ALTER TABLE app.listings ALTER COLUMN address_line1 SET NOT NULL;

DROP INDEX app.listings_slug_idx;

ALTER TABLE app.listings
    DROP COLUMN rating_count,
    DROP COLUMN rating_avg,
    DROP COLUMN currency,
    DROP COLUMN minimum_nights,
    DROP COLUMN cleaning_fee_cents,
    DROP COLUMN host_name,
    DROP COLUMN summary,
    DROP COLUMN slug;
