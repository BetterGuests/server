# Migration security contract

Application migrations run as the migrator role (`DB_EXPECTED_MIGRATOR_ROLE`,
`betterguests_migrator` by default). The API runs as the app role
(`DB_EXPECTED_ROLE`, `betterguests_app` by default); it must never own
database objects or receive schema creation privileges.

Write `__APP_ROLE__` and `__MIGRATOR_ROLE__` in migration SQL instead of a
literal role name. `cmd/migrate` renders every embedded file through
`migrations.Rendered` before handing it to Goose, substituting those
placeholders for the configured `DB_EXPECTED_ROLE`/`DB_EXPECTED_MIGRATOR_ROLE`
values — this is necessary because `GRANT ... TO` and `CREATE POLICY ... TO`
can't bind a role name as a query parameter. `db/bootstrap/001_roles_and_schema.sql`
and `db/test/001_local_roles.sql` are run through `psql` directly rather than
Goose, so they use plain literal role names instead (an administrator or
`cmd/dev test` supplies the actual names when invoking `psql`).

Every tenant-owned table migration must, before the transaction commits:

1. Use the `app` schema and a UUID primary key.
2. Add explicit `NOT NULL`, foreign-key, unique, and check constraints.
3. Add indexes for foreign keys and expected filters.
4. Enable and force row-level security.
5. Create command-specific policies for `__APP_ROLE__` using
   `app.current_user_id()`.
6. Use both `USING` and `WITH CHECK` where writes are allowed.
7. Explicitly grant only the operations the API needs.
8. Never grant `TRUNCATE`, `REFERENCES`, `TRIGGER`, ownership, or schema
   creation to the API role.

Example:

```sql
CREATE TABLE app.example (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL,
    value text NOT NULL CHECK (length(value) <= 1000)
);

CREATE INDEX example_owner_id_idx ON app.example (owner_id);

ALTER TABLE app.example ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.example FORCE ROW LEVEL SECURITY;

CREATE POLICY example_select ON app.example
    FOR SELECT TO __APP_ROLE__
    USING (owner_id = app.current_user_id());

CREATE POLICY example_insert ON app.example
    FOR INSERT TO __APP_ROLE__
    WITH CHECK (owner_id = app.current_user_id());

CREATE POLICY example_update ON app.example
    FOR UPDATE TO __APP_ROLE__
    USING (owner_id = app.current_user_id())
    WITH CHECK (owner_id = app.current_user_id());

CREATE POLICY example_delete ON app.example
    FOR DELETE TO __APP_ROLE__
    USING (owner_id = app.current_user_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON app.example TO __APP_ROLE__;
```

The integration tests query PostgreSQL's catalogs and fail when these
invariants are violated. Do not weaken those tests to make a migration pass.
