package auth

import "testing"

func TestConfigFromLookupRequiresClerkSecretKey(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantErr bool
	}{
		{
			name:   "valid secret key",
			values: map[string]string{"CLERK_SECRET_KEY": "sk_test_abc123"},
		},
		{
			name:   "secret key with surrounding whitespace",
			values: map[string]string{"CLERK_SECRET_KEY": "  sk_test_abc123  "},
		},
		{
			name:    "missing secret key",
			values:  map[string]string{},
			wantErr: true,
		},
		{
			name:    "blank secret key",
			values:  map[string]string{"CLERK_SECRET_KEY": "   "},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := configFromLookup(func(key string) string {
				return test.values[key]
			})
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SecretKey != "sk_test_abc123" {
				t.Fatalf("unexpected secret key: %q", cfg.SecretKey)
			}
		})
	}
}

func TestNewClerkTokenVerifierRequiresSecretKey(t *testing.T) {
	if _, err := NewClerkTokenVerifier(ClerkConfig{}); err == nil {
		t.Fatal("expected an error for an empty secret key")
	}
}
