# Deploying to Railway

This project deploys to Railway as three pieces in one project: a Postgres
database, a one-shot **migrate** service, and the long-running **api**
service. The migrate service is the only thing that ever holds migration
credentials; the api service never does.

```
┌────────────┐      MIGRATION_DATABASE_URL      ┌────────────┐
│  migrate   │ ───────────────────────────────▶ │            │
│ (one-shot) │                                   │  Postgres  │
└────────────┘                                   │            │
                                                  │            │
┌────────────┐         DATABASE_URL              │            │
│    api     │ ───────────────────────────────▶  │            │
│ (replicas) │ ◀── Railway load balancer ──┐      └────────────┘
└────────────┘                             │
      ▲                                    │
      └── scale by raising replica count ──┘
```

Both services build from this same repository; only the build/start command
and environment variables differ. Neither needs a Dockerfile — Railway's
Nixpacks builder detects `go.mod` and builds Go binaries on its own — but
its default build only builds the root package, so the migrate service
needs an explicit build command pointing at `cmd/migrate` (see step 3).
Override both the build and start command on each service rather than
leaving the start command as `go run ...`: `go run` recompiles from source
on every container start, which is slower and needs the Go toolchain
present at runtime; building a binary once and running that is what you
want in production.

## 1. Provision Postgres

Add a Postgres database to the Railway project. Railway gives you an admin
connection string (superuser) — keep it only for the one-time bootstrap
below and in Railway's own secret storage; the application never uses it.

## 2. Bootstrap roles (one time, per environment)

`db/bootstrap/001_roles_and_schema.sql` is deliberately **not** run by
either service — it creates the migrator role that Goose needs to already
exist, and it needs the admin connection to do that. Run it once, manually:

```sh
railway connect postgres
\i db/bootstrap/001_roles_and_schema.sql
```

Then set passwords for both roles, out of band (the bootstrap script
intentionally never stores them):

```sql
ALTER ROLE betterguests_migrator PASSWORD '...';
ALTER ROLE betterguests_app PASSWORD '...';
```

Build `DATABASE_URL` and `MIGRATION_DATABASE_URL` from those two passwords
and Railway's Postgres host — see `db/migrations/README.md` and
`README.md`'s "Database roles" section for what each role is allowed to do.
Repeat this step whenever you provision a new environment (staging,
production); it is not part of the versioned migration history, so Goose
will never run it for you.

## 3. The migrate service

Create a Railway service from the same repo. In its settings, set
**Config file path** to `/deploy/railway.migrate.json`, which sets:

- **Build command:** `go build -o migrate ./cmd/migrate`
- **Start command:** `./migrate up`
- **Restart policy:** Never. It's a one-shot job: it applies whatever
  migrations haven't run, exits 0, and Railway should leave it stopped
  instead of relaunching it in a loop.
- **Variables** (scoped to this service only — do not put these in a
  project-wide shared variable group, or the api service will inherit
  migration credentials it should never see):
  - `MIGRATION_DATABASE_URL` — the `betterguests_migrator` connection string
  - `DB_EXPECTED_ROLE` — `betterguests_app` (migrations render
    `__APP_ROLE__` into every `GRANT`/`CREATE POLICY ... TO`, so this must
    be set here too, not just on the api service)
  - `DB_EXPECTED_MIGRATOR_ROLE` — `betterguests_migrator`

Redeploy this service whenever a new migration ships. Watch its deploy log
for Goose's "no migrations to run" or the list of migrations it applied,
then move to step 4. Because migrations in this repo are append-only and
additive (see `db/migrations/README.md`), the api service's currently
running code keeps working against the new schema even before it redeploys
— there's no hard ordering requirement, just a preference to apply schema
changes before the code that depends on them goes live.

## 4. The api service

Create a second Railway service from the same repo and set its **Config
file path** to `/deploy/railway.api.json`. That file sets:

- **Build command:** `go build -o server .`
- **Start command:** `./server`
- **Healthcheck:** `GET /ready`, with 120 seconds to pass
- **Restart policy:** Always
- **Draining:** 15 seconds between SIGTERM and SIGKILL

Neither service may use the default `/railway.json` path: both build from this
repo, and one root file would give the migrate service the api's restart policy.
The dashboard cannot override a value the config file sets.

- **Variables** (service-scoped; notice `MIGRATION_DATABASE_URL` is absent
  — this service has no way to reach the database as the migrator role,
  by construction, not just by convention):
  - `DATABASE_URL` — the `betterguests_app` connection string
  - `DB_EXPECTED_ROLE` — `betterguests_app`
  - `DB_APPLICATION_NAME` — e.g. `betterguests-api`
  - `DB_MAX_CONNS` — see scaling math below
  - `ALLOWED_ORIGINS` — JSON array of the web origins allowed to call the API
  - `CLERK_SECRET_KEY`
  - `PERSONA_API_KEY`, `PERSONA_TEMPLATE_ID`, `PERSONA_WEBHOOK_SECRET` — all
    three are required; the server refuses to start without them. The API key
    must be allowed to redact inquiries, because the server deletes ID data
    through Persona's redact call.
  - The Persona webhook must include related objects in its payload. The server
    saves the host's name from the passed `verification/government-id` entry and
    shows it to proof reviewers. Without it, reviewers see "No ID name on file",
    nobody's age is checked against their ID, and a host who verifies again is
    held for review because the names cannot be compared.
  - Optional `ID_FINGERPRINT_KEY` (at least 32 random characters) turns on
    detection of one person on several accounts. Keep it stable: changing it
    stops new checks matching earlier ones.
  - `ID_RETENTION_DAYS` (default 30), `ID_HOLD_CAP_DAYS` (default 1095),
    `ID_PURGE_INTERVAL` (default 6h), `ID_PURGE_BATCH` (default 50),
    `ID_PURGE_ENABLED` (default true) — the ID deletion job. Do not also turn on
    Persona's organization-wide auto redaction: it cannot skip a held ID.
  - `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`
  - Settlement (cashback refunds and host payouts), off by default:
    `SETTLEMENT_ENABLED`, `SETTLEMENT_STARTS_ON` (required when enabled; stays
    that ended earlier are never settled), `PLATFORM_FEE_PERCENT` (default 12),
    `SETTLEMENT_INTERVAL` (default 1h), `SETTLEMENT_BATCH` (default 50) and
    `PAYOUTS_WEB_URL` (the web origin hosts return to after Stripe onboarding)
  - Optional email: `RESEND_API_KEY`, `EMAIL_FROM` (an address on a domain
    verified in Resend) and `APP_WEB_URL` (the web origin used in email links).
    Without the key no email is sent; with it the other two are required.
    Reports and listing pauses are emailed to the hosts, guests and admins
    involved. Admin mail goes to the address in `app.admin_accounts`.
  - `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`,
    `S3_SECRET_ACCESS_KEY` — listing photos and cashback photos; optional
    `S3_URL_STYLE=path` for path-style buckets. Profile banners upload from the
    browser straight to the bucket, so run `scripts/bucket-cors.sh` once per
    bucket (it needs the `S3_*` variables, `ALLOWED_ORIGINS`, `jq` and the AWS
    CLI; set `DRY_RUN=1` to print the rules first)
  - `ANTHROPIC_API_KEY`: every new listing photo is sent to Claude, which
    refuses explicit, violent, hateful and unrelated images, personal documents
    and advertised contact details. Without the key photos are accepted
    unscreened and the server logs a warning at startup
  - `SENTRY_DSN`, `SENTRY_ENVIRONMENT`
  - Optional `GEOAPIFY_API_KEY`: `GET /api/v1/listings/{id}/nearby?category=drinks`
    asks Geoapify Places for bars, cafes and similar places around a stay's
    public (rounded) coordinates. Without the key the route is not mounted
    and the server logs a warning at startup; the web app then falls back to
    name search. The free plan allows 3,000 credits a day and requires
    Geoapify and OpenStreetMap attribution; the web app shows both under the
    map's info button. Answers are cached in memory for 24 hours per stay and
    category.
  - `PORT`, optional `LOG_LEVEL`
  - Firebase variables and `BASE_URL` are no longer read and must not be set.
- **Health checks:** Railway's healthcheck uses `GET /ready` (checks the
  database). Leave `GET /health` for an external uptime monitor that should
  stay green even during a database outage.

### How the api stays up

- **Database not ready at boot.** The server retries the first connection with
  backoff (0.5s doubling to 8s) for up to 60 seconds before exiting. A wrong
  role or a bad `DATABASE_URL` fails at once instead.
- **Dropped connections.** The pool checks idle connections every 30 seconds,
  drops dead ones and opens new ones. Connections are recycled every 30 to 33
  minutes, spread out so they don't all reconnect together.
- **A blip mid-request.** Starting a transaction is retried up to 3 times when
  a connection can't be opened or failed before anything was sent. Statements
  inside a transaction are never retried, so a write never runs twice.
- **A background job panics.** ID redaction, document retention, verification
  pauses, settlement and trip updates each restart on their own (1s backoff
  doubling to 1 minute). The panic is logged at error level, which sends it to
  Sentry. Requests keep being served meanwhile.
- **The process exits.** Railway restarts it (restart policy Always). On a
  redeploy, Railway sends SIGTERM; the server stops taking connections,
  finishes requests for up to 10 seconds, stops the jobs and closes the pool,
  inside the 15-second draining window.

## US-only access and the waitlist

The web service limits access by the `CF-IPCountry` header that Cloudflare adds.
Visitors outside the United States and its territories see the waitlist and the
legal pages only.

- The web hostname must be proxied through Cloudflare (orange cloud) with IP
  geolocation turned on. A request without the header is let in.
- Remove the web service's public `*.up.railway.app` domain, or anyone using it
  skips the check.
- The check is in the web app only. The API still answers requests from any
  country.

Signups go to `app.waitlist_signups`. The API can only insert into it, so read
it with the Postgres admin connection:

```sql
SELECT email, country, created_at FROM app.waitlist_signups ORDER BY created_at;
```

## 5. Scaling the api service

Railway scales a service horizontally by running more replicas of it; its
built-in load balancer distributes incoming requests across them without
any extra configuration — "duplicating the service" is the whole mechanism,
there's no separate load balancer resource to set up.

The one thing that doesn't scale for free is the database connection pool:
`betterguests_app` has a hard `CONNECTION LIMIT 20` set in the bootstrap SQL
(see `db/db.go`'s `roleConnLimit`). Every replica opens its own pool of up
to `DB_MAX_CONNS` connections, so:

```
replica_count × DB_MAX_CONNS ≤ 20
```

For example, 4 replicas at `DB_MAX_CONNS=5` uses the full budget. Going
beyond 4 replicas means lowering `DB_MAX_CONNS` per replica, or raising the
role's connection limit — the latter requires an admin to reconnect with
the Postgres admin credentials and run `ALTER ROLE betterguests_app
CONNECTION LIMIT <n>;` (bootstrap, not a migration, so it's a manual step
like step 2).

## 6. Backups, deployment permissions, and alerting

- **Backups.** Railway's Postgres plugin keeps its own volume snapshots, but
  those live and die with the Railway project — they don't protect against
  deleting the project itself, and a snapshot restore is Railway's UI flow,
  not something this repo scripts. For a backup you control independently of
  Railway, run `scripts/backup.sh` with the Postgres admin connection string
  (the same one `db/bootstrap/001_roles_and_schema.sql` uses) on a schedule —
  a Railway cron service running this repo's image with `./backup.sh
  "$ADMIN_DATABASE_URL"` as its start command is the natural fit — and ship
  the output (`./backups/betterguests-*.dump`) to storage that outlives the
  Railway project, such as object storage. `scripts/backup.sh` must run as
  the admin role, not `betterguests_app`/`betterguests_migrator`: both have
  `NOBYPASSRLS`, so a backup taken as either would silently contain zero
  rows from every row-level-security-protected table.
- **Restoring.** `scripts/restore.sh <dump-file> <admin-database-url>`
  restores a `scripts/backup.sh` dump into a target database already
  bootstrapped with the same `betterguests_app`/`betterguests_migrator`
  role names (step 1–2 above, run against the new database before
  restoring into it). `db/restore_integration_test.go` runs both scripts
  end to end against the disposable test database as part of `go run
  ./cmd/dev test` — insert a marker row, back up, delete the marker,
  restore, confirm it's back — so a change to either script that breaks
  recovery fails CI, not a real incident. Practice the full drill
  (provision a scratch Postgres on Railway, bootstrap it, restore the
  latest backup into it, smoke-test the API against it) on a schedule, not
  only when `go run ./cmd/dev test` passes against the disposable
  container.
- **Restricting production deploys.** Require a pull request and passing CI
  (`.github/workflows/ci.yml`) before anything merges to `main` — enable
  GitHub branch protection on `main` requiring status checks and at least
  one review (`gh api repos/{owner}/{repo}/branches/main/protection ...`,
  or the GitHub UI). On Railway, scope who can trigger a production deploy
  by restricting project membership and relying on Railway's "deploy on
  push to main" trigger instead of handing out manual deploy access; nobody
  should need to run a production deploy from their own machine.
- **Logging and alerts for critical failures.** Every unexpected failure
  already logs at `slog.Error` in structured JSON (see `internal/logging`),
  and `internal/middleware.Recover` guarantees a panic logs at `Error`
  instead of just closing the connection. Point whatever log drain or
  alerting rule your hosting/observability provider offers at
  `level=error` on this service's log stream — that single rule catches
  panics, authentication-service failures, and webhook-processing failures
  without this repo needing to integrate a specific alerting vendor's SDK.

## 7. Rolling back

A bad deploy of the api service is a normal Railway rollback — redeploy the
previous build. A bad migration is not: per `db/migrations/README.md`,
migrations are append-only, so the fix is always a new forward migration,
never editing or re-running an old one. `cmd/migrate` only exposes
`up`/`status`/`version` — there is no `down` command to run against
Railway. Each migration's `-- +goose Down` block exists for local
development only; in an emergency, write and test the compensating forward
migration against the disposable test database (`go run ./cmd/dev test`),
then ship it through the migrate service like any other change.

## Settlement

A stay settles 72 hours after checkout, taken as 18:00 UTC on `ends_on`. The job
refunds the guest's cashback, then transfers the charge minus the platform fee
and cashback to the host. Fee and amounts are fixed the first time a booking is
processed.

Before turning it on:

- Enable Connect in the Stripe Dashboard and test the whole flow in a sandbox.
- Keep enough platform balance for refunds, for example by using manual payouts.
- Ask Stripe how long the platform may hold funds for bookings made far ahead.

A booking with status `awaiting_host` has refunded the guest and is waiting for
the host to finish Stripe onboarding. It is retried on every run.

A booking with status `blocked` needs a person: the payment is disputed, was
refunded by hand, or its refund failed. `reason` says which. Nothing retries it
until the row is fixed with the migration role:

```sql
SELECT booking_id, reason FROM app.booking_settlements WHERE status = 'blocked';
```

Not handled automatically: disputes or refund failures that arrive after a
booking is `settled`, and cancellations after the guest has been charged.
