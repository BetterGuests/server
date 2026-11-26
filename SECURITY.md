
# Security Checklist

## PostgreSQL Security & Performance

- [x] Enable Row-Level Security (RLS) where appropriate
- [x] Use parameterized SQL queries
- [x] Apply least-privilege database roles
- [x] Enforce NOT NULL, UNIQUE, CHECK, and foreign key constraints
- [x] Use transactions for multi-step operations
- [x] Prevent overlapping property bookings
- [x] Avoid SELECT * queries
- [x] Prevent N+1 query problems
- [x] Set query execution timeouts
- [x] Configure database connection pooling
- [x] Create indexes for frequently queried columns
- [ ] Implement pagination and query limits
- [x] Configure automated database backups
- [x] Test database restoration procedures
- [x] Use versioned database migrations
- [x] Never expose database credentials in logs
- [x] Keep PostgreSQL inaccessible from the public internet
- [x] Test RLS policies and database permissions
- [x] Monitor slow queries and connection usage

## Go API Security

- [x] Enforce HTTPS for all production requests
- [x] Implement secure user authentication
- [x] Enforce authorization for every protected resource
- [x] Validate and sanitize inputs where appropriate
- [x] Limit HTTP request body sizes
- [x] Implement rate limiting
- [x] Configure a strict CORS origin allowlist
- [x] Implement CSRF protection for cookie-based authentication
- [x] Configure HTTP server timeouts
- [x] Secure session cookies and access tokens
- [x] Return safe error messages without exposing internals
- [ ] Use idempotency keys for payment operations
- [ ] Verify incoming webhook signatures
- [x] Log authentication failures and sensitive operations
- [x] Avoid logging passwords, tokens, or sensitive user data
- [x] Store production secrets in Railway environment variables
- [x] Protect Railway and GitHub accounts with MFA
- [x] Restrict production deployment permissions
- [x] Keep Go dependencies updated
- [x] Run go vet and govulncheck
- [x] Write tests for unauthorized and forbidden requests
- [x] Test invalid inputs and oversized requests
- [x] Configure health checks and service monitoring

## API Authentication Guidelines

- [x] Use sessions or access tokens for authenticated guests
- [ ] Use API keys or service credentials for trusted integrations
- [x] Never embed secret API keys in public clients
- [x] Keep genuinely public endpoints accessible without authentication
- [x] Verify ownership when accessing bookings and properties
- [x] Use role-based or permission-based authorization where needed
- [ ] Implement secure token expiration and revocation
- [x] Use short-lived credentials where practical

## Testing & Deployment

- [x] Write failing tests before implementing critical business rules
- [x] Test database transactions and rollbacks
- [x] Test concurrent booking attempts
- [x] Test authentication and authorization boundaries
- [x] Test database connection failures
- [x] Test HTTP handler error responses
- [x] Run automated tests before deployment
- [x] Run migrations through a controlled deployment process
- [x] Keep production and testing credentials separate in application code
- [x] Configure logging and alerts for critical failures
- [x] Test backup recovery and incident response

---

## Notes on items that need a human to finish, are "N/A", or are blocked on an unbuilt feature

A checked box above means the behavior is implemented and, where testable
without live infrastructure, covered by a test — not that every box is
equally deep. A few are worth reading before you trust them blindly:

- **Implement pagination and query limits**: left unchecked. `listings`,
  `reviews`, and `bookings` only expose `Create` so far — there is no
  `List`/collection endpoint yet for a client to page through. When one is
  added, give it an explicit, server-clamped limit from the start (see
  `internal/messages/service.go` in the sibling RealTalk codebase this
  template also produced, for the `defaultListLimit`/`maxListLimit`
  pattern) rather than an unbounded `SELECT`.
- **Use idempotency keys for payment operations** / **Verify incoming
  webhook signatures**: left unchecked. `internal/payments` and
  `internal/notifications` are still stub `domain.go` files with no
  `service.go`/`store.go`/`http.go` — there is no Stripe integration wired
  up yet to secure. Build idempotency-key support and webhook signature
  verification in from the start once that domain exists, not after.
- **CSRF protection for cookie-based authentication** / **secure session
  cookies and access tokens**: checked as *not applicable*, not as
  implemented. This API never sets a cookie — authentication is a Clerk
  bearer token the client must explicitly attach to every request (see
  `internal/auth`) — so there is no ambient credential for a CSRF attack to
  ride on, and no session cookie to secure. If a cookie-based flow is ever
  added, these two need real implementations, not this note.
- **Use API keys or service credentials for trusted integrations**: left
  unchecked. There is no trusted server-to-server integration yet (no
  Stripe, no webhook sender) for a credential to authenticate. Add one when
  the first such integration is built — the Stripe-Signature pattern this
  template's RealTalk codebase uses for its webhook is the reference.
- **Role-based or permission-based authorization**: there is no `role`
  column or RBAC flag. Authorization is relationship-based: PostgreSQL RLS
  policies grant access by `host_id`/`guest_id` match (see
  `db/migrations/00002_create_tables.sql`), which is a permission model,
  just not a named-role one. Add an explicit role system when a feature
  needs a permission that isn't "do you own or participate in this row"
  (e.g. a support/admin account).
- **Implement secure token expiration and revocation** — genuinely
  unchecked. Clerk issues short-lived session JWTs and lets you revoke a
  session from the Clerk dashboard/API, but nothing in this codebase adds a
  local revocation check (e.g. a `revoked_at` on `app.accounts` checked on
  every request), so a revoked-at-Clerk session is only rejected once its
  JWT naturally expires rather than immediately. Close this by adding that
  column and checking it in `auth.AuthService.Authenticate`.
- **Restrict production deployment permissions**: `.github/workflows/ci.yml`
  gates merges on tests passing; add GitHub branch protection on `main`
  (required reviews/status checks) too — see `RAILWAY.md` §6.
- **Configure automated database backups**: `scripts/backup.sh` exists and
  `db/restore_integration_test.go` proves the backup/restore round trip
  works, but nothing runs `backup.sh` on a schedule yet — wire that into a
  Railway cron service (or your CI scheduler) per `RAILWAY.md` §6.
- **Prevent overlapping property bookings** / **test concurrent booking
  attempts**: enforced by a PostgreSQL exclusion constraint
  (`db/migrations/00005_booking_overlap_prevention.sql`), not a Go-level
  check-then-insert, and proven under real concurrent goroutines/
  transactions in `internal/bookings/store_integration_test.go`'s
  `TestConcurrentBookingAttemptsOnlyOneWins`.
- Everything in this file was last verified by `gofmt -l .`, `go build
  ./...`, and `go vet ./...` passing, plus the full non-integration test
  suite (`go test ./...`). The Docker-dependent integration suite (`go run
  ./cmd/dev test`, which includes the booking-overlap concurrency test and
  the backup/restore drill) was **not** run in this pass — Docker wasn't
  available in the environment this work was done in. Run it yourself
  before deploying anything that touched `db/`, `internal/bookings`, or the
  scripts above.

## Age and consent

- [x] Age confirmation is stored server side; only adults get a row (database CHECK enforces 18 or older)
- [x] Booking, checkout, listing creation and verification require a confirmed adult account
- [x] Biometric consent is recorded with a policy version and timestamp before any Persona inquiry is created
- [x] A minor's date of birth is never stored
- [ ] Withdrawal endpoint for biometric consent

## Trips, reviews and notifications

- Entry codes and Wi-Fi passwords live in `app.listing_arrival_guides`. The
  host can read their own guide. A guest can read it only with a confirmed
  booking near its dates, and the API narrows that to 48 hours before
  check-in until checkout. Notifications and thread notes never contain them.
- Reviews are double-blind. A review has no `published_at` until both sides
  wrote one or 14 days passed, only `app.publish_due_reviews()` sets it, and
  the API role cannot update or delete reviews.
- Five `SECURITY DEFINER` functions let the background job act without a
  signed-in user: `complete_finished_stays`, `publish_due_reviews`,
  `stay_journey`, `deliver_stay_notification` and `post_stay_message`. The
  first two take their clock from the database. The last two only address the
  guest or host of the named booking. None is reachable from a request without
  going through a service that checks the caller.

## Guards on bookings and listings

- The API role may change only `status` and `decided_at` on a booking, and a
  host may only move a request to approved or declined.
- A listing cannot leave `published` while it has a current or upcoming
  booking, and the booking foreign key stops it being deleted.
- A request cannot be approved after its check-in date. A booking the guest was
  charged for cannot be cancelled, and a cancel only goes through once the card
  hold is released.
- Check-in times and time zone are locked while a guest is staying.

## Cancellations and refunds

- Refund amounts are worked out on the server from the stored payment. The
  client only echoes the amount it showed, and a mismatch cancels nothing.
- A cancellation or decision is recorded first, then refunded. Failed refunds
  stay `pending`, are retried by the job, and hold the host payout.
- Only an admin who is neither the guest nor the host can decide a problem
  report; a database trigger enforces it.
- Paused listings live in `app.listing_suspensions`. Rows are never removed; an
  admin lifts a pause from `/admin/listings`, and a database trigger stops
  anyone else doing it.
- Email carries the notification title, body and a link, never entry codes or
  report details. Provider errors are logged without the address.

## Identity standing

- A check counts only while it passed, is not revoked, is not waiting for or
  refused at review, and is under 365 days old. Booking, publishing, approving a
  booking and starting a document check need such a check. Earning cashback needs
  one that passed and was not revoked, at any age.
- A check is held for an admin when the name differs from the last accepted ID or
  cannot be compared, the ID shows someone under 18, the identity is on another
  account, or an earlier check was declined. Admins cannot review themselves.
  Persona deletes the inquiry after `ID_RETENTION_DAYS`, so decide before then.
- A Persona decline revokes earlier passes.
- Listings pause at once when a host's verification is revoked or a proof is
  withdrawn, and 30 days after it expires. They resume when the cause is gone.
  The 365 and 30 day figures also appear in `app.queue_team_alerts`.
- The duplicate check stores an HMAC of name and birth date under
  `ID_FINGERPRINT_KEY`, never the birth date. It is deleted with the ID name.

## Automatic property document checks

- Only deeds, and property tax bills issued in the last 90 days, can skip an
  admin. Utility bills and leases are always read by a person.
- A proof is approved without an admin only when the signed Persona webhook says
  the inquiry completed, the document verification passed with no failed check,
  and the extracted name and address match the host's ID name and the listing.
  The name must belong to one person on the document, and the state and unit must
  agree. Everything else stays in the admin queue.
- A Persona decline revokes the proof and stops that inquiry ever approving.
  Admins can withdraw any approved proof.
- Only inquiries this server created are accepted, tied to one host, listing and
  address. `app.approve_persona_property_proof` is `SECURITY DEFINER` and limited
  to that host's submitted proof while the listing address is unchanged. It
  trusts the API to have verified the webhook first.
- Checked document files stay in Persona. We store the result and the inquiry ID.
  Admins open the inquiry in the Persona dashboard. Manual uploads are still kept
  in our storage.
- The ID deletion job redacts rejected document inquiries, inquiries that never
  produced a result after `ID_RETENTION_DAYS`, and unapproved ones when a host
  asks to delete ID data.
- Manually uploaded files are deleted from storage by a six hourly job: rejected
  ones on its next run, approved ones 90 days after approval, withdrawn approvals
  90 days after withdrawal. The proof row stays.
- Approved document inquiries are kept 90 days after approval for disputes, then
  redacted. A host's deletion request does not shorten this, and identity holds do
  not extend it. The listing stays approved.

- A host may delete a draft listing that has ownership documents, unless an
  approval on it was withdrawn in the last 90 days.
  `app.release_draft_listing_documents` queues each stored file and Persona
  inquiry for the deletion jobs, then removes the proof rows. The application
  role still cannot delete a proof directly.

## Authority to list

- A host states whether they own the home, own it through a company, manage it or
  rent it. Only an owner's document can be approved automatically; the rest need
  an admin, with the supporting files for that connection.
- Managers and renters give the owner's name, a contact and an end date. The six
  hourly job withdraws approvals past that date, which pauses the listing.
- Supporting files follow the main document's deletion schedule. Owner details are
  erased when the main file is deleted.
- Not built: contacting the owner automatically, checking company registries, and
  co-host accounts.
