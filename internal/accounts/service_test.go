package accounts

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type storeStub struct {
	account Account
	err     error
}

func (stub *storeStub) Ensure(_ context.Context, account Account) error {
	stub.account = account
	return stub.err
}

func TestAccountServiceEnsureAccountCreatesDeterministicAccount(t *testing.T) {
	store := &storeStub{}
	service := NewAccountService(store)

	accountID, err := service.EnsureAccount(context.Background(), "clerk-user")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != DeterministicID("clerk-user") {
		t.Fatal("service did not return the deterministic account ID")
	}
	if store.account.ID != accountID || store.account.ClerkUserID != "clerk-user" {
		t.Fatalf("store received unexpected account: %+v", store.account)
	}
}

type countingStore struct {
	calls int
	err   error
}

func (store *countingStore) Ensure(_ context.Context, _ Account) error {
	store.calls++
	return store.err
}

func TestAccountServiceEnsureAccountHitsStoreOncePerAccount(t *testing.T) {
	store := &countingStore{}
	service := NewAccountService(store)

	for range 3 {
		if _, err := service.EnsureAccount(context.Background(), "clerk-user"); err != nil {
			t.Fatal(err)
		}
	}
	if store.calls != 1 {
		t.Fatalf("expected one store call for a repeat account, got %d", store.calls)
	}

	if _, err := service.EnsureAccount(context.Background(), "another-user"); err != nil {
		t.Fatal(err)
	}
	if store.calls != 2 {
		t.Fatalf("expected a new account to reach the store, got %d calls", store.calls)
	}
}

func TestAccountServiceEnsureAccountRetriesAfterStoreError(t *testing.T) {
	store := &countingStore{err: errors.New("database unavailable")}
	service := NewAccountService(store)

	if _, err := service.EnsureAccount(context.Background(), "clerk-user"); err == nil {
		t.Fatal("expected the store error")
	}
	store.err = nil
	if _, err := service.EnsureAccount(context.Background(), "clerk-user"); err != nil {
		t.Fatal(err)
	}
	if store.calls != 2 {
		t.Fatalf("a failed ensure must not be remembered, got %d calls", store.calls)
	}
}

func TestAccountServiceEnsureAccountRejectsEmptyUID(t *testing.T) {
	service := NewAccountService(&storeStub{})

	if _, err := service.EnsureAccount(context.Background(), ""); !errors.Is(err, ErrInvalidClerkUserID) {
		t.Fatalf("expected ErrInvalidClerkUserID, got %v", err)
	}
}

func TestAccountServiceEnsureAccountReturnsStoreError(t *testing.T) {
	want := errors.New("database unavailable")
	service := NewAccountService(&storeStub{err: want})

	if _, err := service.EnsureAccount(context.Background(), "clerk-user"); !errors.Is(err, want) {
		t.Fatalf("expected store error, got %v", err)
	}
}

func TestDeterministicID(t *testing.T) {
	first := DeterministicID("clerk-user")

	if first == uuid.Nil {
		t.Fatal("deterministic ID must not be nil")
	}
	if first != DeterministicID("clerk-user") {
		t.Fatal("the same Clerk user ID produced different account IDs")
	}
	if first == DeterministicID("another-user") {
		t.Fatal("different Clerk user IDs produced the same account ID")
	}
	if DeterministicID("") != uuid.Nil {
		t.Fatal("an empty Clerk user ID must produce a nil account ID")
	}
}

func TestUserIDRoundTrip(t *testing.T) {
	want := uuid.New()
	got, ok := UserID(WithUserID(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("unexpected identity: %v %v", got, ok)
	}

	if _, ok := UserID(context.Background()); ok {
		t.Fatal("context without an identity returned an account ID")
	}
	if _, ok := UserID(WithUserID(context.Background(), uuid.Nil)); ok {
		t.Fatal("a nil account ID must not be treated as authenticated")
	}
}
