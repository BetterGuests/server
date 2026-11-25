package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/auth"
	"github.com/google/uuid"
)

const nameLookupTimeout = 10 * time.Second

type nameFetcher interface {
	DisplayName(ctx context.Context, clerkUserID string) (string, error)
}

type namingProvisioner struct {
	inner      auth.AccountProvisioner
	fetcher    nameFetcher
	names      accounts.NameStore
	remembered sync.Map
}

func newNamingProvisioner(inner auth.AccountProvisioner, fetcher nameFetcher, names accounts.NameStore) *namingProvisioner {
	return &namingProvisioner{inner: inner, fetcher: fetcher, names: names}
}

func (provisioner *namingProvisioner) EnsureAccount(ctx context.Context, clerkUserID string) (uuid.UUID, error) {
	accountID, err := provisioner.inner.EnsureAccount(ctx, clerkUserID)
	if err != nil {
		return uuid.Nil, err
	}
	if _, already := provisioner.remembered.LoadOrStore(accountID, struct{}{}); !already {
		go provisioner.remember(accountID, clerkUserID)
	}
	return accountID, nil
}

func (provisioner *namingProvisioner) remember(accountID uuid.UUID, clerkUserID string) {
	ctx, cancel := context.WithTimeout(context.Background(), nameLookupTimeout)
	defer cancel()

	name, err := provisioner.fetcher.DisplayName(ctx, clerkUserID)
	if errors.Is(err, auth.ErrNoDisplayName) {
		return
	}
	if err != nil {
		slog.Warn("Look up account name", "error", err)
		provisioner.remembered.Delete(accountID)
		return
	}
	if err := provisioner.names.Remember(ctx, accountID, name); err != nil {
		slog.Warn("Store account name", "error", err)
		provisioner.remembered.Delete(accountID)
	}
}
