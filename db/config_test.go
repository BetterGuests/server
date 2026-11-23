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

func configWith(extra map[string]string) (Config, error) {
	return configFromLookup(func(key string) string {
		switch key {
		case "DATABASE_URL":
			return "postgres://example.invalid/database"
		case "DB_EXPECTED_ROLE":
			return "betterguests_app"
		default:
			return extra[key]
		}
	})
}

func TestConfigKeepsOneIdleConnectionByDefault(t *testing.T) {
	config, err := configWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinIdleConns != 1 {
		t.Fatalf("expected one idle connection by default, got %d", config.MinIdleConns)
	}
}

func TestConfigReadsMinIdleConns(t *testing.T) {
	config, err := configWith(map[string]string{"DB_MAX_CONNS": "5", "DB_MIN_IDLE_CONNS": "5"})
	if err != nil {
		t.Fatal(err)
	}
	if config.MinIdleConns != 5 {
		t.Fatalf("expected five idle connections, got %d", config.MinIdleConns)
	}
}

func TestConfigAllowsZeroMinIdleConns(t *testing.T) {
	config, err := configWith(map[string]string{"DB_MIN_IDLE_CONNS": "0"})
	if err != nil {
		t.Fatal(err)
	}
	if config.MinIdleConns != 0 {
		t.Fatalf("expected zero idle connections, got %d", config.MinIdleConns)
	}
}

func TestConfigRejectsInvalidMinIdleConns(t *testing.T) {
	for _, value := range []string{"-1", "abc", "6", "2.5"} {
		_, err := configWith(map[string]string{"DB_MAX_CONNS": "5", "DB_MIN_IDLE_CONNS": value})
		if err == nil {
			t.Fatalf("expected DB_MIN_IDLE_CONNS=%q to fail", value)
		}
	}
}

func TestConfigResetsConnectionsOnReleaseByDefault(t *testing.T) {
	config, err := configWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !config.ResetOnRelease {
		t.Fatal("expected connections to be reset on release by default")
	}
}

func TestConfigCanDisableResetOnRelease(t *testing.T) {
	for _, value := range []string{"false", "0"} {
		config, err := configWith(map[string]string{"DB_RESET_ON_RELEASE": value})
		if err != nil {
			t.Fatal(err)
		}
		if config.ResetOnRelease {
			t.Fatalf("expected DB_RESET_ON_RELEASE=%q to disable the reset", value)
		}
	}
}

func TestConfigRejectsInvalidResetOnRelease(t *testing.T) {
	if _, err := configWith(map[string]string{"DB_RESET_ON_RELEASE": "maybe"}); err == nil {
		t.Fatal("expected an unparseable DB_RESET_ON_RELEASE to fail")
	}
}
