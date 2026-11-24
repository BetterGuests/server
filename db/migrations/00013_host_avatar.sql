-- +goose Up

ALTER TABLE app.listings
    ADD COLUMN host_avatar_url text CHECK (host_avatar_url IS NULL OR (host_avatar_url ~ '^(/|https://)' AND length(host_avatar_url) <= 500));

UPDATE app.listings
SET host_avatar_url = '/hosts/coast-to-coast-getaways.png'
WHERE host_name = 'Coast to Coast Getaways';

-- +goose Down

ALTER TABLE app.listings DROP COLUMN IF EXISTS host_avatar_url;
