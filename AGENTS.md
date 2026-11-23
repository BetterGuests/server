# Code style rules

These rules describe how code in this repository is written. Follow them for
every change. When an existing file differs from a rule, match the existing
file style unless the change is specifically about fixing that file.

## Layout

- The server entrypoint is split by job, and `layout_test.go` enforces it:
  - `main.go` declares only `main` and `run`: load config, open the pool,
    call `buildRouteDeps`, create and run the server. It imports no
    `internal/<domain>` package.
  - `wiring.go` is the composition root. It constructs stores, services and
    handlers and returns them as `routeDeps`.
  - `routes.go` is the only file that registers routes (`Handle`/`HandleFunc`)
    and builds the middleware stack. Add a new endpoint here and nowhere else.
  - `server.go` holds the `http.Server` lifecycle and the health and readiness
    handlers.
  A new domain adds one field to `routeDeps`, one construction in
  `buildRouteDeps`, and its routes in `routes.go`.
  Do not create a package that only re-exports route wiring.
- Domain packages live in `internal/<domain>` and use `domain.go`, `service.go`,
  `store.go`, and `http.go`. Keep a domain together rather than splitting it
  across layers.
- `domain.go` contains types only: domain structs, config types, constants, and
  the interfaces the rest of the package implements. Do not put functions,
  methods, or business logic in it.
- `service.go` contains the `<Domain>Service` struct, its `New<Domain>Service`
  constructor, the use-case methods, and the helper functions they use.
- `store.go` contains the persistence implementation, normally
  `Postgres<Domain>Store`, plus its `NewPostgres<Domain>Store` constructor and
  store methods.
- `http.go` contains the `HTTPHandler` struct, its `NewHTTPHandler` constructor,
  and the endpoint methods. Middleware may use a role name (for example
  `Authenticator`) instead of `HTTPHandler`.
- Shared infrastructure lives in packages such as `internal/auth`,
  `internal/logging`, and `internal/middleware` (rate limiting, HTTPS
  enforcement, panic recovery, security headers — cross-cutting HTTP
  concerns that do not belong to any one domain). Infrastructure may depend
  on domain packages; domain packages must not depend on infrastructure.
- Tests live beside the code they test and use the `_test.go` suffix.
- A package name must match its directory name. Never declare `package foo` in
  a directory called `bar`.

## Naming and constructors

- Exported constructors are named `NewX`.
- Service structs are prefixed with their domain and end in `Service`, for
  example `ListingService`, `AccountService`, and `AuthService`. Their
  constructors are `New<Domain>Service`, not `NewService`.
- PostgreSQL implementations are named `Postgres<Domain>Store`, for example
  `PostgresListingStore` and `PostgresAccountStore`.
- Declare interfaces where they are consumed, not where they are implemented.
  Name them for the capability the consumer needs: `Creator`, `Store`,
  `Provisioner`, `ListingCreator`.
- Prefer small capability interfaces over large service interfaces.
- Acronyms follow Go convention: `HTTPHandler`, `userID`, `clerkUserID`.

## Errors

- Define sentinel errors as `var ErrX = errors.New("...")` and compare with
  `errors.Is`.
- Wrap errors with context: `fmt.Errorf("insert listing: %w", err)`.
- Translate persistence-specific errors at the store boundary. Keep database
  details such as PostgreSQL error codes out of services and handlers.
- Return user-safe messages from HTTP handlers. Log internal detail; do not put
  it in the response body.
- Never ignore an error silently. If an error is intentionally discarded, make
  that explicit with `_ =`.
- Do not panic for expected failures.

## Database

- Every query that reads or writes user-owned data runs through
  `db.WithUserTx`. Never query user-owned tables on a raw pool connection.
- Use parameterized SQL only. Never build SQL by concatenating values.
- List columns explicitly. Do not use `SELECT *`.
- Use `NULLIF($n, '')` to turn empty optional strings into `NULL`.
- Provision a missing `app.accounts` row at the start of a user transaction
  (`INSERT ... ON CONFLICT (id) DO NOTHING`) before writing rows that reference
  it.
- Follow `db/migrations/README.md` for every table migration: `app` schema,
  UUID primary key, explicit constraints, indexes, forced row-level security,
  command-specific policies, explicit grants, and no elevated privileges.
- The app and migrator role names are never literals in migration SQL. Write
  `__APP_ROLE__`/`__MIGRATOR_ROLE__`; `cmd/migrate` renders every embedded
  migration through `migrations.Rendered` (`db/migrations/embed.go`),
  substituting the configured `DB_EXPECTED_ROLE`/`DB_EXPECTED_MIGRATOR_ROLE`
  values before handing the SQL to Goose. `db/bootstrap/` and `db/test/` SQL
  run through `psql` directly instead of Goose, so they take literal role
  names supplied on the `psql` command line, not the placeholders.
- Migrations are append-only. Fix a bad migration with a new migration.
- Fetch a collection with one query (`WHERE id = ANY($1)` or a join), never
  by looping a per-row query over results from an earlier query — that is an
  N+1 query bug.
- A business rule that must hold across concurrent requests (two bookings
  never overlapping a listing's calendar) is enforced by a database
  constraint, not a Go-level check-then-write — see
  `db/migrations/00005_booking_overlap_prevention.sql`'s exclusion
  constraint and `internal/bookings/store.go`. A read in Go, however
  careful, cannot see another transaction's uncommitted write.

## HTTP

- Limit request bodies with `http.MaxBytesReader`.
- Decode JSON with `json.Decoder`, call `DisallowUnknownFields`, and verify the
  body ends with a single JSON value.
- Authenticated handlers read the identity from `accounts.UserID(ctx)`. Never
  trust a user ID supplied in the request body or URL.
- Map domain errors to explicit status codes: `ErrInvalid*` to 400, missing
  identity to 401, ownership failures to 403, missing resources to 404, and
  conflicts to 409. Use 500 for unexpected failures.
- Write JSON responses with `Content-Type: application/json`.

## Authentication and identity

- External identity providers are wrapped behind a small verifier interface so
  handlers and tests never call the provider directly.
- Verified provider identities map to internal account IDs deterministically.
  Do not store or trust provider tokens.
- Identity is carried in `context` through `accounts.WithUserID` and read with
  `accounts.UserID`.
- Never log bearer tokens, credentials, or other secrets.

## Configuration

- Read configuration from environment variables only.
- Validate required configuration at startup and fail fast with a clear error.
- Do not provide defaults for secrets.
- Keep runtime credentials separate from migration credentials.

## Logging

- Use `log/slog` with structured attributes: `slog.Info("message", "key", value)`.
- Log at startup, shutdown, and unexpected failures. Do not log every request.
- Use the package `internal/logging` to configure the process logger.

## Comments

- Code carries no comments: no line comments, block comments, doc comments on
  exported items, or commented-out code. Delete dead code; git has it.
- Make the code say it: rename until a comment would be redundant, extract a
  well-named function, replace a magic number with a named constant.
- Put the reason for a non-obvious decision in a test named after the edge case,
  the commit message or PR description, or a lasting note in this file or
  `SECURITY.md`.
- When editing code that already has comments, remove the ones inside the code
  you touch.
- Machine-read directives are allowed (`//go:embed`, `//go:build`,
  `//go:generate`, `-- +goose ...`, `shellcheck`). Nothing else is.
- This overrides the golint and revive "exported should have comment" checks.
  Disable those rules rather than adding comments.

## Tests

- Prefer table-driven tests for multiple cases.
- Use local stub structs that implement the consumer interface. Do not add a
  mocking framework.
- Tests must not require a live database or external network unless they live
  in an explicit integration test file.
- Run the full suite, including the disposable database tests, with
  `go run ./cmd/dev test`.

## Formatting and checks

Before considering work complete:

```sh
gofmt -l .
go build ./...
go vet ./...
go run ./cmd/dev test
```

`go run ./cmd/dev test` requires Docker and runs every test against a disposable
PostgreSQL container.
