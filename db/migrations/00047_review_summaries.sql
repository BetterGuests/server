-- +goose Up

CREATE TABLE app.review_summaries (
    id           uuid PRIMARY KEY,
    listing_id   uuid UNIQUE REFERENCES app.listings(id) ON DELETE CASCADE,
    guest_id     uuid UNIQUE REFERENCES app.accounts(id) ON DELETE CASCADE,
    fingerprint  text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{32}$'),
    review_count integer NOT NULL CHECK (review_count > 0),
    summary      text NOT NULL CHECK (length(summary) BETWEEN 1 AND 600),
    updated_at   timestamptz NOT NULL,
    checked_at   timestamptz NOT NULL,
    CONSTRAINT review_summaries_one_subject CHECK (num_nonnulls(listing_id, guest_id) = 1)
);

ALTER TABLE app.review_summaries ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.review_summaries FORCE ROW LEVEL SECURITY;

CREATE POLICY review_summaries_select ON app.review_summaries
    FOR SELECT TO __APP_ROLE__
    USING (
        listing_id IS NOT NULL
        OR guest_id = app.current_user_id()
        OR EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.guest_id = review_summaries.guest_id AND l.host_id = app.current_user_id()
        )
    );

CREATE POLICY review_summaries_migrator_select ON app.review_summaries
    FOR SELECT TO __MIGRATOR_ROLE__
    USING (true);

CREATE POLICY review_summaries_migrator_insert ON app.review_summaries
    FOR INSERT TO __MIGRATOR_ROLE__
    WITH CHECK (true);

CREATE POLICY review_summaries_migrator_update ON app.review_summaries
    FOR UPDATE TO __MIGRATOR_ROLE__
    USING (true)
    WITH CHECK (true);

GRANT SELECT ON app.review_summaries TO __APP_ROLE__;

-- +goose StatementBegin
CREATE FUNCTION app.save_review_summary(
    subject_kind text,
    subject_id uuid,
    expected_fingerprint text,
    summarized_count integer,
    summary_text text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    current_fingerprint text;
    current_count integer;
BEGIN
    IF subject_kind = 'listing' THEN
        SELECT md5(coalesce(string_agg(r.id::text, ',' ORDER BY r.id::text), '')), count(*)
        INTO current_fingerprint, current_count
        FROM app.listing_reviews r
        WHERE r.listing_id = subject_id AND r.published_at IS NOT NULL;
    ELSIF subject_kind = 'guest' THEN
        IF NOT EXISTS (
            SELECT 1 FROM app.bookings b
            JOIN app.listings l ON l.id = b.listing_id
            WHERE b.guest_id = subject_id AND l.host_id = app.current_user_id()
        ) THEN
            RETURN false;
        END IF;
        SELECT md5(coalesce(string_agg(g.id::text, ',' ORDER BY g.id::text), '')), count(*)
        INTO current_fingerprint, current_count
        FROM app.guest_reviews g
        WHERE g.guest_id = subject_id AND g.published_at IS NOT NULL;
    ELSE
        RAISE EXCEPTION 'unknown review summary subject %', subject_kind USING ERRCODE = '22023';
    END IF;

    IF current_fingerprint IS DISTINCT FROM expected_fingerprint OR current_count <> summarized_count THEN
        RETURN false;
    END IF;

    IF subject_kind = 'listing' THEN
        INSERT INTO app.review_summaries AS saved
            (id, listing_id, fingerprint, review_count, summary, updated_at, checked_at)
        VALUES (gen_random_uuid(), subject_id, expected_fingerprint, summarized_count, summary_text, now(), now())
        ON CONFLICT (listing_id) DO UPDATE
        SET fingerprint = EXCLUDED.fingerprint, review_count = EXCLUDED.review_count,
            summary = EXCLUDED.summary, checked_at = EXCLUDED.checked_at,
            updated_at = CASE WHEN saved.summary = EXCLUDED.summary THEN saved.updated_at ELSE EXCLUDED.updated_at END;
    ELSE
        INSERT INTO app.review_summaries AS saved
            (id, guest_id, fingerprint, review_count, summary, updated_at, checked_at)
        VALUES (gen_random_uuid(), subject_id, expected_fingerprint, summarized_count, summary_text, now(), now())
        ON CONFLICT (guest_id) DO UPDATE
        SET fingerprint = EXCLUDED.fingerprint, review_count = EXCLUDED.review_count,
            summary = EXCLUDED.summary, checked_at = EXCLUDED.checked_at,
            updated_at = CASE WHEN saved.summary = EXCLUDED.summary THEN saved.updated_at ELSE EXCLUDED.updated_at END;
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.save_review_summary(text, uuid, text, integer, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.save_review_summary(text, uuid, text, integer, text) TO __APP_ROLE__;

-- +goose Down

DROP FUNCTION app.save_review_summary(text, uuid, text, integer, text);
DROP TABLE app.review_summaries;
