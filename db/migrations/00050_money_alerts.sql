-- +goose Up

-- +goose StatementBegin
CREATE FUNCTION app.format_cents(cents integer)
RETURNS text
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
    SELECT '$' || to_char(cents / 100.0, 'FM999,999,990.00')
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.format_cents(integer) FROM PUBLIC;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.queue_team_alerts()
RETURNS TABLE (account_id uuid, booking_id uuid, kind text, title text, body text, for_team boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN

    INSERT INTO app.accounts (id)
    SELECT a.account_id FROM app.admin_accounts a
    ON CONFLICT (id) DO NOTHING;

    RETURN QUERY
    WITH wanted AS (
        SELECT a.account_id AS recipient, r.booking_id AS about_booking, NULL::uuid AS about_listing,
               'report_filed' AS event, 'report_filed:' || r.booking_id AS dedupe, true AS team,
               'A guest reported a problem' AS heading,
               left('Review the report about ' || r.listing_title || '.', 500) AS message
        FROM app.stay_reports r
        CROSS JOIN app.admin_accounts a
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
        UNION ALL
        SELECT r.guest_id, r.booking_id, NULL, 'report_received', 'report_received:' || r.booking_id, false,
               'We got your report',
               left('Our team is looking at the problem you reported at ' || r.listing_title || '.', 500)
        FROM app.stay_reports r
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), false,
               'Your listing is paused',
               left(l.title || ' is not taking new bookings. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days'
        UNION ALL
        SELECT a.account_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), true,
               'A listing was paused',
               left(l.title || ' is paused. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        CROSS JOIN app.admin_accounts a
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days' AND a.account_id <> s.host_id
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_unpaused',
               'listing_unpaused:' || s.listing_id || ':' || extract(epoch FROM s.lifted_at), false,
               'Your listing is live again',
               left(l.title || ' can take bookings again.', 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at > now() - interval '3 days'
        UNION ALL
        SELECT e.account_id, NULL, NULL, 'id_expiring',
               'id_expiring:' || e.verification_id || ':' || e.days_ahead, false,
               'Your ID check expires soon',
               'Verify your ID again before ' || to_char(e.expires_at, 'FMMon FMDD, YYYY') || ' to keep booking and hosting.'
        FROM (
            SELECT latest.account_id, latest.verification_id, latest.expires_at,
                   CASE WHEN latest.expires_at - now() <= interval '7 days' THEN 7 ELSE 30 END AS days_ahead
            FROM (
                SELECT DISTINCT ON (v.account_id) v.account_id, v.id AS verification_id,
                       COALESCE(v.last_event_at, v.updated_at) + interval '365 days' AS expires_at
                FROM app.identity_verifications v
                WHERE v.status = 'completed' AND v.revoked_at IS NULL
                  AND (v.review_state IS NULL OR v.review_state = 'cleared')
                ORDER BY v.account_id, COALESCE(v.last_event_at, v.updated_at) DESC
            ) latest
            WHERE latest.expires_at > now() AND latest.expires_at - now() <= interval '30 days'
        ) e
        UNION ALL
        SELECT v.account_id, NULL, NULL, 'identity_in_review', 'identity_in_review:' || v.id, false,
               'We are checking your ID',
               'Our team needs a closer look at your latest ID check.'
        FROM app.identity_verifications v
        WHERE v.review_state = 'pending' AND v.status = 'completed'
        UNION ALL
        SELECT a.account_id, NULL, NULL, 'identity_review', 'identity_review:' || v.id, true,
               'An ID check needs review',
               'An ID check is waiting for the team.'
        FROM app.identity_verifications v
        CROSS JOIN app.admin_accounts a
        WHERE v.review_state = 'pending' AND v.status = 'completed' AND a.account_id <> v.account_id
        UNION ALL
        SELECT s.host_id, s.booking_id, NULL, 'payout_sent', 'payout_sent:' || s.booking_id, false,
               app.format_cents(s.payout_cents) || ' is on its way',
               left('Your payout for ' || l.title || ' is heading to your bank.', 500)
        FROM app.booking_settlements s
        JOIN app.bookings b ON b.id = s.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE s.status = 'settled' AND s.payout_cents > 0 AND s.stripe_transfer_id IS NOT NULL
          AND s.updated_at > now() - interval '3 days'
        UNION ALL
        SELECT s.host_id, s.booking_id, NULL, 'payout_setup_needed', 'payout_setup_needed:' || s.booking_id, false,
               'Set up payouts to get paid',
               left(app.format_cents(s.payout_cents) || ' from ' || l.title || ' is waiting. Add your bank details to receive it.', 500)
        FROM app.booking_settlements s
        JOIN app.bookings b ON b.id = s.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE s.status = 'awaiting_host' AND s.updated_at > now() - interval '3 days'
        UNION ALL
        SELECT s.host_id, s.booking_id, NULL, 'payout_on_hold', 'payout_on_hold:' || s.booking_id, false,
               'Your payout is on hold',
               left('We paused the payout for ' || l.title || ' while our team checks the payment. We will be in touch.', 500)
        FROM app.booking_settlements s
        JOIN app.bookings b ON b.id = s.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE s.status = 'blocked' AND s.updated_at > now() - interval '3 days'
        UNION ALL
        SELECT s.guest_id, s.booking_id, NULL, 'cashback_paid', 'cashback_paid:' || s.booking_id, false,
               'You got ' || app.format_cents(s.refund_due_cents) || ' back',
               left('Your cashback for ' || l.title || ' is on its way to your card. It can take 5 to 10 days to show up.', 500)
        FROM app.booking_settlements s
        JOIN app.bookings b ON b.id = s.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE s.status = 'settled' AND s.refund_due_cents > 0 AND s.stripe_refund_id IS NOT NULL
          AND s.updated_at > now() - interval '3 days'
        UNION ALL
        SELECT r.guest_id, r.booking_id, NULL, 'cashback_earned', 'cashback_earned:' || r.booking_id, false,
               'You earned ' || app.format_cents(r.amount_cents) || ' back',
               left('Cashback for ' || l.title || ' is on its way to your card.', 500)
        FROM app.booking_bundle_refunds r
        JOIN app.bookings b ON b.id = r.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE r.status = 'succeeded' AND r.updated_at > now() - interval '3 days'
        UNION ALL
        SELECT t.guest_id, t.booking_id, NULL, 'photo_retake', 'photo_retake:' || t.booking_id, false,
               'Retake a checkout photo',
               left('One of your photos for ' || l.title || ' needs another try to earn your cashback.', 500)
        FROM app.booking_task_submissions t
        JOIN app.bookings b ON b.id = t.booking_id
        JOIN app.listings l ON l.id = b.listing_id
        WHERE t.status = 'retake' AND t.updated_at > now() - interval '3 days'
    ),
    inserted AS (
        INSERT INTO app.notifications (id, account_id, booking_id, listing_id, kind, event_key, for_team, title, body)
        SELECT gen_random_uuid(), w.recipient, w.about_booking, w.about_listing, w.event, w.dedupe, w.team,
               w.heading, w.message
        FROM wanted w
        ON CONFLICT DO NOTHING
        RETURNING account_id, booking_id, kind, title, body, for_team
    )
    SELECT i.account_id, i.booking_id, i.kind, i.title, i.body, i.for_team FROM inserted i;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.queue_team_alerts()
RETURNS TABLE (account_id uuid, booking_id uuid, kind text, title text, body text, for_team boolean)
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
#variable_conflict use_column
BEGIN

    INSERT INTO app.accounts (id)
    SELECT a.account_id FROM app.admin_accounts a
    ON CONFLICT (id) DO NOTHING;

    RETURN QUERY
    WITH wanted AS (
        SELECT a.account_id AS recipient, r.booking_id AS about_booking, NULL::uuid AS about_listing,
               'report_filed' AS event, 'report_filed:' || r.booking_id AS dedupe, true AS team,
               'A guest reported a problem' AS heading,
               left('Review the report about ' || r.listing_title || '.', 500) AS message
        FROM app.stay_reports r
        CROSS JOIN app.admin_accounts a
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
        UNION ALL
        SELECT r.guest_id, r.booking_id, NULL, 'report_received', 'report_received:' || r.booking_id, false,
               'We got your report',
               left('Our team is looking at the problem you reported at ' || r.listing_title || '.', 500)
        FROM app.stay_reports r
        WHERE r.status = 'open' AND r.created_at > now() - interval '3 days'
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), false,
               'Your listing is paused',
               left(l.title || ' is not taking new bookings. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days'
        UNION ALL
        SELECT a.account_id, NULL, s.listing_id, 'listing_paused',
               'listing_paused:' || s.listing_id || ':' || extract(epoch FROM s.created_at), true,
               'A listing was paused',
               left(l.title || ' is paused. Reason: ' || s.reason, 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        CROSS JOIN app.admin_accounts a
        WHERE s.lifted_at IS NULL AND s.created_at > now() - interval '3 days' AND a.account_id <> s.host_id
        UNION ALL
        SELECT s.host_id, NULL, s.listing_id, 'listing_unpaused',
               'listing_unpaused:' || s.listing_id || ':' || extract(epoch FROM s.lifted_at), false,
               'Your listing is live again',
               left(l.title || ' can take bookings again.', 500)
        FROM app.listing_suspensions s
        JOIN app.listings l ON l.id = s.listing_id
        WHERE s.lifted_at > now() - interval '3 days'
        UNION ALL
        SELECT e.account_id, NULL, NULL, 'id_expiring',
               'id_expiring:' || e.verification_id || ':' || e.days_ahead, false,
               'Your ID check expires soon',
               'Verify your ID again before ' || to_char(e.expires_at, 'FMMon FMDD, YYYY') || ' to keep booking and hosting.'
        FROM (
            SELECT latest.account_id, latest.verification_id, latest.expires_at,
                   CASE WHEN latest.expires_at - now() <= interval '7 days' THEN 7 ELSE 30 END AS days_ahead
            FROM (
                SELECT DISTINCT ON (v.account_id) v.account_id, v.id AS verification_id,
                       COALESCE(v.last_event_at, v.updated_at) + interval '365 days' AS expires_at
                FROM app.identity_verifications v
                WHERE v.status = 'completed' AND v.revoked_at IS NULL
                  AND (v.review_state IS NULL OR v.review_state = 'cleared')
                ORDER BY v.account_id, COALESCE(v.last_event_at, v.updated_at) DESC
            ) latest
            WHERE latest.expires_at > now() AND latest.expires_at - now() <= interval '30 days'
        ) e
        UNION ALL
        SELECT v.account_id, NULL, NULL, 'identity_in_review', 'identity_in_review:' || v.id, false,
               'We are checking your ID',
               'Our team needs a closer look at your latest ID check.'
        FROM app.identity_verifications v
        WHERE v.review_state = 'pending' AND v.status = 'completed'
        UNION ALL
        SELECT a.account_id, NULL, NULL, 'identity_review', 'identity_review:' || v.id, true,
               'An ID check needs review',
               'An ID check is waiting for the team.'
        FROM app.identity_verifications v
        CROSS JOIN app.admin_accounts a
        WHERE v.review_state = 'pending' AND v.status = 'completed' AND a.account_id <> v.account_id
    ),
    inserted AS (
        INSERT INTO app.notifications (id, account_id, booking_id, listing_id, kind, event_key, for_team, title, body)
        SELECT gen_random_uuid(), w.recipient, w.about_booking, w.about_listing, w.event, w.dedupe, w.team,
               w.heading, w.message
        FROM wanted w
        ON CONFLICT DO NOTHING
        RETURNING account_id, booking_id, kind, title, body, for_team
    )
    SELECT i.account_id, i.booking_id, i.kind, i.title, i.body, i.for_team FROM inserted i;
END
$$;
-- +goose StatementEnd

DROP FUNCTION app.format_cents(integer);
