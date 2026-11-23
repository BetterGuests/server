# BetterGuests Server

Go API for Better Guests.

See `RAILWAY.md` for how this deploys: a one-shot migrate service plus a
horizontally scaled api service, split so the api service never holds
migration credentials.

## Developer command

The repository has one developer command:

```sh
go run ./cmd/dev setup
go run ./cmd/dev migrate
go run ./cmd/dev migration-status
go run ./cmd/dev test
go run ./cmd/dev run
```

`setup` creates an ignored `.env` from `.env.example` only when `.env` does
not already exist. It never overwrites credentials. Go does not automatically
load `.env`; `cmd/dev` exports it before running the API or migrations. Tests
use only the disposable local database described below.

## One-command tests

With Docker Desktop running, execute:

```sh
go run ./cmd/dev test
```

This command creates a disposable PostgreSQL container, bootstraps the local
database roles, applies all embedded Goose migrations, and runs every Go test.
It then removes the container and its data, including after a failed test.

The test command never reads `.env`. It hard-overrides `DATABASE_URL`,
`MIGRATION_DATABASE_URL`, `TEST_DATABASE_URL`, and
`TEST_MIGRATION_DATABASE_URL` with loopback-only URLs for the container, so a
Railway or production URL in your shell or `.env` cannot be used by tests.

## Database roles

- The app role (`DB_EXPECTED_ROLE`, `betterguests_app` by default) is the API
  runtime role. It cannot create schemas or tables, own objects, change RLS,
  truncate tables, or assume the migration role.
- The migrator role (`DB_EXPECTED_MIGRATOR_ROLE`, `betterguests_migrator` by
  default) owns the `app` schema and applies reviewed schema migrations. The
  migration command refuses any other database role.

The API verifies `current_user` at startup and refuses to start unless it is
connected as the configured app role.

Both role names are environment variables, not literals baked into the
database: `db/bootstrap/001_roles_and_schema.sql` creates whatever roles the
administrator names when running it, and `cmd/migrate` renders every
embedded migration (see `db/migrations/embed.go`'s `Rendered`) with
`DB_EXPECTED_ROLE`/`DB_EXPECTED_MIGRATOR_ROLE` substituted in before handing
it to Goose, since `GRANT ... TO` and `CREATE POLICY ... TO` can't bind a
role name as a query parameter. Renaming the roles later is one environment
variable, not a grep across every migration file.

## Bootstrap and migrations

`db/bootstrap/001_roles_and_schema.sql` is an administrator-only,
once-per-database bootstrap. It is grouped with the migration files for easy
discovery, but it is deliberately excluded from Goose because it creates the
migrator role that must already exist before Goose can connect.

Normal versioned migrations live in `db/migrations` and are compiled into the
migration executable. Follow `db/migrations/README.md` for the required RLS,
grant, constraint, and indexing pattern.

The integration tests intentionally create and drop a fixture table and execute
hostile SQL. Run them through `go run ./cmd/dev test`; its URLs are fixed to the
disposable local container and cannot inherit production credentials.

## Environment

- `DATABASE_URL`: runtime connection URL for the app role
- `MIGRATION_DATABASE_URL`: migration URL for the migrator role
- `DB_EXPECTED_ROLE`: the app role name, e.g. `betterguests_app`. The running
  server refuses to start unless `current_user` matches this. Migrations
  also read it, substituting it into every `GRANT`/`CREATE POLICY ... TO`
  statement — see "Database roles" above.
- `DB_EXPECTED_MIGRATOR_ROLE`: the migrator role name, e.g.
  `betterguests_migrator`. `cmd/migrate` refuses to run unless connected as
  this role, and substitutes it the same way `DB_EXPECTED_ROLE` is
  substituted.
- `DB_APPLICATION_NAME`: shown in PostgreSQL logs/monitoring; defaults to
  `api`.
- `DB_MAX_CONNS=5` initially; total across replicas must remain below the role's
  20-connection limit
- `DB_MIN_IDLE_CONNS=1` by default. Connections kept open and pre-opened in
  parallel at startup so the first requests skip the connection handshake. Set
  it equal to `DB_MAX_CONNS` when the database is remote (a tunnel adds seconds
  to each new connection). It must be between 0 and `DB_MAX_CONNS`.
- `DB_RESET_ON_RELEASE=true` by default. Runs `RESET ALL` every time a pooled
  connection is returned, so session state from one request can never reach the
  next. It costs one database round trip per release. Set it to `false` only
  for local development against a remote database. Never in production.
- `CLERK_SECRET_KEY`: Clerk secret key. The API verifies each bearer token
  with it and maps the Clerk user ID to a stable `app.accounts` row on first
  use.
- `PORT`: HTTP port

## Tests

Go tests stay beside the packages they test using the standard `_test.go`
naming convention. This gives tests direct package access and keeps ownership
clear. The database suite uses two disposable accounts to test cross-account
access, identity spoofing, SQL injection, role escalation, DDL, RLS disabling,
truncation, migration-table access, and pooled session-state leakage.
