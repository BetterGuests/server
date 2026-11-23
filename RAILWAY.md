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

Create a Railway service from the same repo. In its settings:

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

Create a second Railway service from the same repo.

- **Build command:** `go build -o server .`
- **Start command:** `./server`
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
    three are required; the server refuses to start without them
  - `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`
  - `S3_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY_ID`,
    `S3_SECRET_ACCESS_KEY` — listing photos and cashback photos; optional
    `S3_URL_STYLE=path` for path-style buckets
  - `SENTRY_DSN`, `SENTRY_ENVIRONMENT`
  - `PORT`, optional `LOG_LEVEL`
  - Firebase variables and `BASE_URL` are no longer read and must not be set.
- **Health checks:** point Railway's healthcheck at `GET /ready` (checks the
  database) and leave `GET /health` for an external uptime monitor that
  should stay green even during a database outage.

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
