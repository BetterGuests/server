package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/auth"
	"github.com/google/uuid"
)

type provisionerStub struct {
	id  uuid.UUID
	err error
}

func (stub provisionerStub) EnsureAccount(context.Context, string) (uuid.UUID, error) {
	return stub.id, stub.err
}

type fetcherStub struct {
	name   string
	avatar string
	err    error
	calls  atomic.Int32
}

func (stub *fetcherStub) PublicProfile(context.Context, string) (auth.PublicProfile, error) {
	stub.calls.Add(1)
	return auth.PublicProfile{DisplayName: stub.name, AvatarURL: stub.avatar}, stub.err
}

type namesStub struct {
	stored chan string
	err    error
}

func (stub *namesStub) Remember(_ context.Context, _ uuid.UUID, name, avatar string) error {
	if stub.err != nil {
		return stub.err
	}
	stub.stored <- name + "|" + avatar
	return nil
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFirstSightOfAnAccountStoresItsClerkNameAndPhoto(t *testing.T) {
	names := &namesStub{stored: make(chan string, 1)}
	fetcher := &fetcherStub{name: "Maria S.", avatar: "https://img.clerk.com/maria"}
	provisioner := newNamingProvisioner(provisionerStub{id: uuid.New()}, fetcher, names)

	if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-names.stored:
		if got != "Maria S.|https://img.clerk.com/maria" {
			t.Fatalf("stored profile %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("name was never stored")
	}
}

func TestNameIsLookedUpOncePerAccount(t *testing.T) {
	names := &namesStub{stored: make(chan string, 4)}
	fetcher := &fetcherStub{name: "Maria S."}
	provisioner := newNamingProvisioner(provisionerStub{id: uuid.New()}, fetcher, names)

	for range 3 {
		if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); err != nil {
			t.Fatal(err)
		}
	}
	<-names.stored

	time.Sleep(50 * time.Millisecond)
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("clerk was asked %d times, want once", calls)
	}
}

func TestAccountWithoutANameIsNotRetriedEveryRequest(t *testing.T) {
	fetcher := &fetcherStub{err: auth.ErrNoDisplayName}
	provisioner := newNamingProvisioner(provisionerStub{id: uuid.New()}, fetcher, &namesStub{stored: make(chan string, 1)})

	for range 3 {
		if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return fetcher.calls.Load() >= 1 })
	}

	time.Sleep(50 * time.Millisecond)
	if calls := fetcher.calls.Load(); calls != 1 {
		t.Fatalf("clerk was asked %d times, want once", calls)
	}
}

func TestTransientClerkFailureIsRetriedOnTheNextRequest(t *testing.T) {
	fetcher := &fetcherStub{err: errors.New("clerk down")}
	provisioner := newNamingProvisioner(provisionerStub{id: uuid.New()}, fetcher, &namesStub{stored: make(chan string, 1)})

	if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fetcher.calls.Load() == 1 })
	time.Sleep(20 * time.Millisecond)

	if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return fetcher.calls.Load() == 2 })
}

func TestProvisioningFailureSkipsNameLookup(t *testing.T) {
	fetcher := &fetcherStub{name: "Maria S."}
	failure := errors.New("database down")
	provisioner := newNamingProvisioner(provisionerStub{err: failure}, fetcher, &namesStub{stored: make(chan string, 1)})

	if _, err := provisioner.EnsureAccount(context.Background(), "user_1"); !errors.Is(err, failure) {
		t.Fatalf("expected provisioning error, got %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if fetcher.calls.Load() != 0 {
		t.Fatal("no name lookup may happen for a failed provision")
	}
}
