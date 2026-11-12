#!/usr/bin/env bash
set -euo pipefail

# Restores a pg_dump custom-format backup (see scripts/backup.sh) into the
# database at $2. This is the script db/restore_integration_test.go
# exercises end to end against the disposable test database, and the one to
# run by hand when practicing — or actually needing — a restore. See
# SECURITY.md: "Test database restoration procedures" / "Test backup
# recovery and incident response".
#
# Run with the admin/superuser connection string, for the same reason
# backup.sh needs it: recreating objects and re-granting betterguests_app/
# betterguests_migrator requires more privilege than either of those roles
# has on itself.
#
# Usage:
#   ./scripts/restore.sh ./backups/betterguests-20260101T000000Z.dump "postgresql://postgres:...@host/db"
#
# Deliberately requires an explicit, non-empty target database: this drops
# and recreates every object the backup contains, so pointing it at a
# database you still need would destroy its current data. Restore into the
# database you intend to end up with (a freshly bootstrapped Railway
# Postgres service for disaster recovery, or the disposable test database
# for a drill) — never at a database serving live traffic you want to keep.

if [ $# -ne 2 ]; then
	echo "usage: $0 <backup-file> <database-url>" >&2
	exit 1
fi

backup_file="$1"
database_url="$2"

if [ ! -f "$backup_file" ]; then
	echo "no such backup file: $backup_file" >&2
	exit 1
fi

echo "Restoring $backup_file into the target database ..."
pg_restore --clean --if-exists --dbname="$database_url" "$backup_file"

echo "Restore complete."
