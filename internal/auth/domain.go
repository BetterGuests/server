package auth

import (
	"context"

	"github.com/google/uuid"
)

// T Y P E S --------------------------------------- //

// ClerkIdentity is the verified identity the API needs from a Clerk session
// token.
type ClerkIdentity struct {
	UserID string
}

// ClerkConfig holds the settings needed to verify Clerk session tokens.
type ClerkConfig struct {
	SecretKey string
}

// TokenVerifier verifies an identity-provider token. Handlers and tests depend
// on this interface so they never call the provider directly.
type TokenVerifier interface {
	VerifyIDToken(ctx context.Context, rawIDToken string) (ClerkIdentity, error)
}

type AccountProvisioner interface {
	EnsureAccount(ctx context.Context, clerkUserID string) (uuid.UUID, error)
}

type BanChecker interface {
	IsBanned(ctx context.Context, accountID uuid.UUID) (bool, error)
}
