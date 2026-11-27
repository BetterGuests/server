package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type verifierStub struct {
	identity ClerkIdentity
	err      error
}

func (stub verifierStub) VerifyIDToken(context.Context, string) (ClerkIdentity, error) {
	return stub.identity, stub.err
}

type provisionerStub struct {
	accountID   uuid.UUID
	clerkUserID string
	err         error
	called      bool
}

func (stub *provisionerStub) EnsureAccount(_ context.Context, clerkUserID string) (uuid.UUID, error) {
	stub.called = true
	stub.clerkUserID = clerkUserID
	if stub.accountID == uuid.Nil {
		stub.accountID = uuid.New()
	}
	return stub.accountID, stub.err
}

func TestAuthServiceAuthenticateReturnsProvisionedAccount(t *testing.T) {
	provisioner := &provisionerStub{}
	service := NewAuthService(verifierStub{identity: ClerkIdentity{UserID: "clerk-user"}}, provisioner)

	accountID, err := service.Authenticate(context.Background(), "Bearer token")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != provisioner.accountID {
		t.Fatalf("unexpected account ID: %v", accountID)
	}
	if !provisioner.called || provisioner.clerkUserID != "clerk-user" {
		t.Fatalf("provisioner not called correctly: %+v", provisioner)
	}
}

func TestAuthServiceAuthenticateRejectsInvalidTokens(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
		verifier      verifierStub
	}{
		{name: "missing header"},
		{name: "wrong scheme", authorization: "Basic token"},
		{name: "empty bearer", authorization: "Bearer "},
		{name: "verifier rejects", authorization: "Bearer token", verifier: verifierStub{err: ErrUnauthenticated}},
		{name: "empty provider user ID", authorization: "Bearer token", verifier: verifierStub{identity: ClerkIdentity{}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewAuthService(test.verifier, &provisionerStub{})
			if _, err := service.Authenticate(context.Background(), test.authorization); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("expected ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestAuthServiceAuthenticateReturnsProvisioningError(t *testing.T) {
	want := errors.New("database unavailable")
	service := NewAuthService(
		verifierStub{identity: ClerkIdentity{UserID: "clerk-user"}},
		&provisionerStub{err: want},
	)

	_, err := service.Authenticate(context.Background(), "Bearer token")
	if !errors.Is(err, want) {
		t.Fatalf("expected provisioning error, got %v", err)
	}
	if errors.Is(err, ErrUnauthenticated) {
		t.Fatal("a provisioning failure must not look like an authentication failure")
	}
}

type banStub struct {
	banned bool
	err    error
}

func (stub banStub) IsBanned(context.Context, uuid.UUID) (bool, error) {
	return stub.banned, stub.err
}

func TestABannedAccountCannotAuthenticateAndAFailedCheckLetsNobodyIn(t *testing.T) {
	failure := errors.New("database down")
	cases := map[string]struct {
		bans banStub
		want error
	}{
		"banned":           {banStub{banned: true}, ErrBanned},
		"check failed":     {banStub{err: failure}, failure},
		"in good standing": {banStub{}, nil},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			service := NewAuthService(verifierStub{identity: ClerkIdentity{UserID: "clerk-user"}}, &provisionerStub{}).WithBans(test.bans)
			accountID, err := service.Authenticate(context.Background(), "Bearer token")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if (accountID == uuid.Nil) != (test.want != nil) {
				t.Fatalf("account = %v", accountID)
			}
		})
	}
}

func TestTheAPITellsABannedAccountWhyItWasRefused(t *testing.T) {
	service := NewAuthService(verifierStub{identity: ClerkIdentity{UserID: "clerk-user"}}, &provisionerStub{}).WithBans(banStub{banned: true})
	reached := false
	handler := NewAuthenticator(service).Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/bookings", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if reached || response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"account_banned"`) {
		t.Fatalf("reached=%v %d %s", reached, response.Code, response.Body)
	}
}
