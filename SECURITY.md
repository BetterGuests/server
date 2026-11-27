
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
  Guests can also read arrival details through private automatic messages, under
  the same window (see Automatic messages).
- Reviews are double-blind. A review has no `published_at` until both sides
  wrote one or 14 days passed, only `app.publish_due_reviews()` sets it, and
  the API role cannot update or delete reviews.
- Seven `SECURITY DEFINER` functions let the background job act without a
  signed-in user: `complete_finished_stays`, `publish_due_reviews`,
  `stay_journey`, `deliver_stay_notification`, `post_stay_message`,
  `auto_message_candidates` and `post_auto_message`. The first two take their
  clock from the database. The posting functions only address the guest or host
  of the named booking. None is reachable from a request without going through a
  service that checks the caller.

## Automatic messages

- Only a listing's host can read or change its automatic messages
  (`app.listing_auto_messages`, forced row-level security). A listing holds at
  most 10.
- A message goes out only for a confirmed booking with a succeeded payment, no
  recorded checkout, and before checkout time. `post_auto_message` rechecks
  this, that the template is still enabled and unchanged, and posts it as the
  host.
- `app.booking_auto_messages` has one row per booking and template, written in
  the same transaction as the message, so retries and parallel runs cannot send
  twice. The API role cannot write it. Deleting a sent message does not resend it.
- A new, re-enabled or re-timed message is not sent for moments that had already
  passed.
- A message is private when the host marks it so, when it uses an arrival
  fill-in, or when its text contains the saved entry code or Wi-Fi password
  (3 characters or longer). Private messages are not posted earlier than 48 hours
  before check-in, and ones with arrival fill-ins wait until a guide is saved.
- A private message stores only a fixed notice in `app.messages.body`, so
  conversation previews, reply quotes, realtime events, notifications and email
  never carry its details. The text is in `app.message_private_bodies` with its
  fill-ins unresolved. Listing messages opens it: always for the host, and for
  the guest only while the booking is confirmed and paid, from 48 hours before
  check-in until checkout time or the recorded checkout. Row-level security
  backs this with a wider date check. Arrival fill-ins show the guide as it is
  when read.
- The web app leaves those opened details out of its offline copy.
- Limit: text a host types by hand (for example where a key is) in a message not
  marked private, or after changing the saved code, is an ordinary message. It
  stays readable and appears in the conversation preview. The editor says to turn
  privacy on for such text.
- Only the background job can create a message marked automatic or private; a
  restrictive policy stops the API role from doing so.

## Guards on bookings and listings

- The API role may change only `status` and `decided_at` on a booking, and a
  host may only move a request to approved or declined.
- A listing cannot leave `published` while it has a current or upcoming
  booking, and the booking foreign key stops it being deleted.
- A request cannot be approved after its check-in date. A booking the guest was
  charged for cannot be cancelled, and a cancel only goes through once the card
  hold is released.
- Check-in times and time zone are locked while a guest is staying.

## Payout holds

- A team takedown holds the host payout and the guest's cash back for that
  listing, because both settle together. Restoring the listing releases them on
  the next run. Automatic pauses hold nothing.
- A blocked settlement is listed only to admins and can only be queued again by
  an admin who is neither its host nor its guest. The next run reads the charge
  again. A dispute stops it unless every dispute on the payment was won or closed
  as an inquiry.

## Cancellations and refunds

- Refund amounts are worked out on the server from the stored payment. The
  client only echoes the amount it showed, and a mismatch cancels nothing.
- A cancellation or decision is recorded first, then refunded. Failed refunds
  stay `pending`, are retried by the job, and hold the host payout.
- Only an admin who is neither the guest nor the host can decide a problem
  report; row-level security enforces it.
- Paused listings live in `app.listing_suspensions`. Rows are never removed; an
  admin lifts a pause from `/admin/listings`, and a database trigger stops
  anyone else doing it.
- Any active pause refuses new bookings and approvals. A team pause is a take
  down: the listing also leaves search, its page, its booked dates and new
  enquiries. The team can take down a listing that is already paused for another
  reason. Automatic pauses leave the listing visible. Guests who already booked
  keep their trip page and conversation.
- Email carries the notification title, body and a link, never entry codes or
  report details. Provider errors are logged without the address.

## Reports about listings, hosts and guests

- The reporter is the signed-in account. The reported account is looked up on
  the server from the listing, booking or conversation named in the request, so
  a request never carries another person's account ID. Row-level security
  repeats that relationship check on insert.
- A listing can be reported by anyone signed in. A host can also be reported
  from a published listing. A guest can only be reported by a host who has a
  booking or conversation with them.
- A report is readable by its author and by admins. The reported account cannot
  read or close it, even if it is an admin.
- Only an admin closes a report, and only once. Closing records an outcome and a
  note; it changes nothing about the account or listing.
- One open report per author and listing, and per author and reported person.
  Filing is limited to 10 an hour.
- A new report puts a notification and an email in front of every admin except
  the reported one. Both name only the kind of target and the listing title,
  never the reason, the details or anyone's name.
- Names and the listing title are copied into the report so it stays readable if
  they later change. Message contents are not copied, and admins gain no access
  to conversations.

## Banned accounts

- A banned account is refused by `auth.AuthService`, so every signed-in request
  and realtime connection fails with `account_banned`. Open connections are
  closed on the replica that handled the ban. If the ban cannot be checked, the
  request fails.
- Only an admin bans or lifts, through `app.account_bans`. Admins cannot be
  banned. Rows are never removed.
- Banning pauses the account's published listings. A database trigger refuses new
  bookings and approvals that involve a banned account, and stops anyone
  reopening a listing paused by the ban.
- A ban moves no money. An admin cancels upcoming bookings one at a time, each
  with a full refund, and only before check-in.
- A banned host's share of a settlement waits as `awaiting_host`; the guest is
  still refunded their cash back. Money already transferred to the host's Stripe
  account is not affected.
- A banned account's listings and reviews stay visible unless the team takes a
  listing down.

## Identity standing

- A check counts only while it passed, is not revoked, is not waiting for or
  refused at review, and is under 365 days old. Booking, publishing, approving a
  booking need such a check. Earning cashback needs
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

## Authority to list

- A host states whether they own the home, own it through a company, manage it or
  rent it. Every submission needs an admin, with the supporting files for that
  connection. Persona checks identity only.
- Managers and renters give the owner's name, a contact and an end date. The six
  hourly job withdraws approvals past that date, which pauses the listing.
- Supporting files follow the main document's deletion schedule. Owner details are
  erased when the main file is deleted.
- Not built: contacting the owner automatically, checking company registries, and
  co-host accounts.
