package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/jwks"
	"github.com/clerk/clerk-sdk-go/v2/jwt"
)

// ClerkTokenVerifier verifies Clerk session tokens against Clerk's JSON Web
// Key Set. Verification is networkless after the first fetch: the SDK caches
// the key set and only re-fetches it when a token names an unknown key ID.
type ClerkTokenVerifier struct {
	jwksClient *jwks.Client
}

// ConfigFromEnv builds a ClerkConfig from environment variables.
func ConfigFromEnv() (ClerkConfig, error) {
	return configFromLookup(os.Getenv)
}

// configFromLookup does the real work of building a ClerkConfig, but takes a
// lookup function so tests can supply values without touching the environment.
func configFromLookup(lookup func(string) string) (ClerkConfig, error) {
	secretKey := strings.TrimSpace(lookup("CLERK_SECRET_KEY"))
	if secretKey == "" {
		return ClerkConfig{}, errors.New("CLERK_SECRET_KEY is required")
	}
	return ClerkConfig{SecretKey: secretKey}, nil
}

// NewClerkTokenVerifier builds a verifier from cfg. The secret key is scoped
// to a dedicated backend client instead of the SDK's package-level
// clerk.SetKey, so verification never depends on mutable global state.
func NewClerkTokenVerifier(cfg ClerkConfig) (*ClerkTokenVerifier, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("clerk secret key is required")
	}

	client := jwks.NewClient(&clerk.ClientConfig{
		BackendConfig: clerk.BackendConfig{Key: &cfg.SecretKey},
	})

	return &ClerkTokenVerifier{jwksClient: client}, nil
}

// VerifyIDToken verifies rawIDToken and returns the Clerk user ID it carries.
func (verifier *ClerkTokenVerifier) VerifyIDToken(ctx context.Context, rawIDToken string) (ClerkIdentity, error) {
	claims, err := jwt.Verify(ctx, &jwt.VerifyParams{
		Token:      rawIDToken,
		JWKSClient: verifier.jwksClient,
	})
	if err != nil {
		return ClerkIdentity{}, fmt.Errorf("verify clerk session token: %w", err)
	}
	return ClerkIdentity{UserID: claims.Subject}, nil
}
