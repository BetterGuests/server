package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Files contains every *.sql file in this folder, baked directly into the
// compiled program at build time.
//
// The //go:embed comment right above the variable isn't a regular comment -
// it's a special instruction the Go compiler reads. It means we don't have
// to ship .sql files alongside the binary or worry about the right folder
// being present on a server; the migration SQL travels inside the program
// itself.
//
//go:embed *.sql
var Files embed.FS

// Rendered copies every embedded migration into a new temporary directory
// with the __APP_ROLE__ and __MIGRATOR_ROLE__ placeholders replaced by
// appRole and migratorRole, and returns that directory's path. The caller
// is responsible for removing it.
//
// Goose sends migration SQL to the database as literal text with no
// parameter binding - CREATE ROLE, GRANT ... TO, and CREATE POLICY ... TO
// can't bind a role name as a query parameter - so this is the one point
// where the two configurable role names (DB_EXPECTED_ROLE and
// DB_EXPECTED_MIGRATOR_ROLE) reach the migration SQL. Renaming the roles
// later is one environment variable, not a grep across every migration
// file.
func Rendered(appRole, migratorRole string) (string, error) {
	dir, err := os.MkdirTemp("", "betterguests-migrations-")
	if err != nil {
		return "", fmt.Errorf("create rendered migrations directory: %w", err)
	}

	replacer := strings.NewReplacer("__APP_ROLE__", appRole, "__MIGRATOR_ROLE__", migratorRole)
	err = fs.WalkDir(Files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, readErr := Files.ReadFile(name)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", name, readErr)
		}
		rendered := replacer.Replace(string(raw))
		return os.WriteFile(filepath.Join(dir, name), []byte(rendered), 0o600)
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
