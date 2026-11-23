package accounts

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

// ErrInvalidClerkUserID is returned when an empty Clerk user ID reaches the
// account mapping.
var ErrInvalidClerkUserID = errors.New("clerk user ID is required")

// clerkNamespace fixes the UUID namespace used to derive account IDs from
// Clerk user IDs. It must never change: a different namespace would orphan
// every account created before the change.
var clerkNamespace = uuid.MustParse("4bf672a5-58a7-4f37-8903-731e3d495c74")

type AccountService struct {
	store   AccountStore
	ensured sync.Map
}

// NewAccountService returns an account service backed by store.
func NewAccountService(store AccountStore) *AccountService {
	return &AccountService{store: store}
}

// EnsureAccount returns the stable account ID for a verified Clerk user ID and
// creates the matching row if it does not exist yet.
func (service *AccountService) EnsureAccount(ctx context.Context, clerkUserID string) (uuid.UUID, error) {
	account := Account{ID: DeterministicID(clerkUserID), ClerkUserID: clerkUserID}
	if account.ID == uuid.Nil {
		return uuid.Nil, ErrInvalidClerkUserID
	}
	if _, known := service.ensured.Load(account.ID); known {
		return account.ID, nil
	}
	if err := service.store.Ensure(ctx, account); err != nil {
		return uuid.Nil, err
	}
	service.ensured.Store(account.ID, struct{}{})
	return account.ID, nil
}

// DeterministicID maps a verified Clerk user ID to a stable internal account
// ID. Deriving the ID instead of generating a random one lets a request
// resolve the account with the existing id-based row-level-security
// policies, without a second lookup path keyed on clerk_user_id.
func DeterministicID(clerkUserID string) uuid.UUID {
	if clerkUserID == "" {
		return uuid.Nil
	}
	return uuid.NewSHA1(clerkNamespace, []byte(clerkUserID))
}

type userIDKey struct{}

// WithUserID returns a copy of ctx carrying the authenticated account ID.
func WithUserID(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserID returns the authenticated account ID stored in ctx. The second result
// is false when the request has no authenticated identity.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	userID, ok := ctx.Value(userIDKey{}).(uuid.UUID)
	return userID, ok && userID != uuid.Nil
}
