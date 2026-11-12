
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
