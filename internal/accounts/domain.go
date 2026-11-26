package accounts

import (
	"context"

	"github.com/google/uuid"
)

// T Y P E S --------------------------------------- //

// Account is an internal BetterGuests account linked to a Clerk identity.
type Account struct {
	ID          uuid.UUID
	ClerkUserID string
}

// AccountStore persists accounts.
type AccountStore interface {
	Ensure(ctx context.Context, account Account) error
}

type NameStore interface {
	Remember(ctx context.Context, accountID uuid.UUID, displayName, avatarURL string) error
}
