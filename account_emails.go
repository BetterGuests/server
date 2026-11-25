package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type clerkIDReader interface {
	ClerkUserID(ctx context.Context, accountID uuid.UUID) (string, error)
}

type emailFetcher interface {
	VerifiedEmail(ctx context.Context, clerkUserID string) (string, error)
}

type accountEmails struct {
	accounts clerkIDReader
	fetcher  emailFetcher
}

func (emails accountEmails) VerifiedEmail(ctx context.Context, accountID uuid.UUID) (string, error) {
	clerkUserID, err := emails.accounts.ClerkUserID(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("find sign in identity: %w", err)
	}
	return emails.fetcher.VerifiedEmail(ctx, clerkUserID)
}
