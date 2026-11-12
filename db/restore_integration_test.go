package db

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestBackupAndRestoreRoundTrip is the test for the security checklist's
// "test database restoration procedures" / "test backup recovery and
// incident response" items. It runs the actual scripts/backup.sh and
// scripts/restore.sh — not a reimplementation of what they do — against the
// disposable test database: insert a marker row, back up, delete the
// marker, restore, and confirm the marker is back. That is the real
// disaster-recovery drill this project has, automated.
func TestBackupAndRestoreRoundTrip(t *testing.T) {
	superuserURL := os.Getenv("TEST_SUPERUSER_DATABASE_URL")
	if superuserURL == "" {
		t.Skip("set TEST_SUPERUSER_DATABASE_URL through go run ./cmd/dev test")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	backupScript := filepath.Join(repoRoot, "scripts", "backup.sh")
	restoreScript := filepath.Join(repoRoot, "scripts", "restore.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, superuserURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	markerID := "backup-test-" + time.Now().UTC().Format("20060102T150405.000000000")
	insertMarker(t, ctx, pool, markerID)

	backupDir := t.TempDir()
	runScript(t, backupScript, []string{"BACKUP_DIR=" + backupDir}, superuserURL)

	dumpFile := onlyDumpFile(t, backupDir)

	deleteMarker(t, ctx, pool, markerID)
	if markerExists(t, ctx, pool, markerID) {
		t.Fatal("marker row should be gone before restoring")
	}

	runScript(t, restoreScript, nil, dumpFile, superuserURL)

	if !markerExists(t, ctx, pool, markerID) {
		t.Fatal("marker row was not restored from the backup")
	}
}

func insertMarker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, markerID string) {
	t.Helper()
	// Inserted as the superuser, bypassing RLS entirely, the same way a
	// real backup/restore drill would need to: betterguests_app and
	// betterguests_migrator both have NOBYPASSRLS, so neither can see rows
	// without app.user_id set, and this check must hold regardless of that.
	if _, err := pool.Exec(ctx, `INSERT INTO app.accounts (id, clerk_user_id) VALUES (gen_random_uuid(), $1)`, markerID); err != nil {
		t.Fatalf("insert marker row: %v", err)
	}
}

func deleteMarker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, markerID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `DELETE FROM app.accounts WHERE clerk_user_id = $1`, markerID); err != nil {
		t.Fatalf("delete marker row: %v", err)
	}
}

func markerExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, markerID string) bool {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.accounts WHERE clerk_user_id = $1`, markerID).Scan(&count); err != nil {
		t.Fatalf("query marker row: %v", err)
	}
	return count > 0
}

func onlyDumpFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one backup file in %s, found %d", dir, len(entries))
	}
	return filepath.Join(dir, entries[0].Name())
}

func runScript(t *testing.T, script string, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command(script, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, output)
	}
}
