#!/usr/bin/env bash
set -euo pipefail

# Takes a full logical backup of the database at DATABASE_URL (or $1) using
# pg_dump's custom format, which pg_restore can selectively restore from and
# which is already compressed. See SECURITY.md: "Configure automated
# database backups" / "Test database restoration procedures".
#
# IMPORTANT: run this with the Postgres admin/superuser connection string
# (the one db/bootstrap/001_roles_and_schema.sql uses), not DATABASE_URL's
# betterguests_app role or MIGRATION_DATABASE_URL's betterguests_migrator
# role. Both app roles have NOBYPASSRLS (see db/bootstrap), so a dump taken
# as either one would silently contain zero rows from every
# row-level-security protected table — current_user_id() is unset outside a
# request, so every RLS policy evaluates to "no rows visible". Only a role
# that bypasses RLS sees the real data to back up.
#
# Usage:
#   ./scripts/backup.sh "postgresql://postgres:...@host/db"
#
# This script takes one backup when you run it; it does not schedule
# anything. Run it on a schedule from whatever already runs scheduled jobs
# for this project (a Railway cron service, a CI scheduled workflow, etc.),
# and point its output at storage that outlives the database (e.g. object
# storage), not the same disk Postgres runs on.

database_url="${1:-${DATABASE_URL:-}}"
if [ -z "$database_url" ]; then
	echo "usage: $0 <database-url>  (or set DATABASE_URL)" >&2
	exit 1
fi

backup_dir="${BACKUP_DIR:-./backups}"
mkdir -p "$backup_dir"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
out_file="$backup_dir/betterguests-$timestamp.dump"

echo "Backing up to $out_file ..."
# Ownership and grants are kept (no --no-owner/--no-privileges): restoring
# into a database already bootstrapped with the same betterguests_app/
# betterguests_migrator role names (the normal recovery path — see
# scripts/restore.sh) should recreate the exact same grants and RLS
# policies, not a version stripped of them.
pg_dump --format=custom --file="$out_file" "$database_url"

echo "Done: $out_file ($(du -h "$out_file" | cut -f1))"
