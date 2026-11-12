package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderedSubstitutesRolePlaceholders(t *testing.T) {
	dir, err := Rendered("my_app", "my_migrator")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one rendered migration file")
	}

	foundPlaceholder := false
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), "__APP_ROLE__") || strings.Contains(string(contents), "__MIGRATOR_ROLE__") {
			t.Fatalf("%s still contains an unrendered placeholder", entry.Name())
		}
		if strings.Contains(string(contents), "my_app") || strings.Contains(string(contents), "my_migrator") {
			foundPlaceholder = true
		}
	}
	if !foundPlaceholder {
		t.Fatal("no rendered file contained the substituted role names")
	}
}

func TestRenderedLeavesEmbeddedFilesUntouched(t *testing.T) {
	before, err := Files.ReadFile("00001_security_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}

	dir, err := Rendered("my_app", "my_migrator")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	after, err := Files.ReadFile("00001_security_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rendering mutated the embedded migration source")
	}
}
