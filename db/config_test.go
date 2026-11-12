package db

import "testing"

func TestConfigRequiresDatabaseURL(t *testing.T) {
	_, err := configFromLookup(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected missing DATABASE_URL to fail")
	}
}

func TestConfigRequiresExpectedRole(t *testing.T) {
	_, err := configFromLookup(func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://example.invalid/database"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected missing DB_EXPECTED_ROLE to fail")
	}
}

func TestConfigAppliesSafeDefaults(t *testing.T) {
	config, err := configFromLookup(func(key string) string {
		switch key {
		case "DATABASE_URL":
			return "postgres://example.invalid/database"
		case "DB_EXPECTED_ROLE":
			return "betterguests_app"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("ConfigFromEnv returned an error: %v", err)
	}
	if config.ApplicationName != "api" {
		t.Fatalf("expected default application name, got %q", config.ApplicationName)
	}
	if config.MaxConns != 5 {
		t.Fatalf("expected five connections, got %d", config.MaxConns)
	}
}

func TestConfigRejectsPoolLargerThanRoleLimit(t *testing.T) {
	_, err := configFromLookup(func(key string) string {
		switch key {
		case "DATABASE_URL":
			return "postgres://example.invalid/database"
		case "DB_EXPECTED_ROLE":
			return "betterguests_app"
		case "DB_MAX_CONNS":
			return "21"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("expected a pool larger than the role connection limit to fail")
	}
}
