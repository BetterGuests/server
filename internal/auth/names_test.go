package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	clerk "github.com/clerk/clerk-sdk-go/v2"
)

func TestPublicNameShowsFirstNameAndLastInitialOnly(t *testing.T) {
	tests := []struct {
		name        string
		first, last string
		want        string
	}{
		{"both names", "Maria", "Santos", "Maria S."},
		{"lowercase last initial is capitalised", "Maria", "santos", "Maria S."},
		{"first name only", "Maria", "", "Maria"},
		{"last name only", "", "Santos", "Santos"},
		{"no names", "", "", ""},
		{"surrounding whitespace", "  Maria ", " Santos  ", "Maria S."},
		{"multibyte initial", "Zoë", "Ægir", "Zoë Æ."},
		{"overlong first name is clipped", strings.Repeat("a", 90), "Jones", strings.Repeat("a", maxNamePartRunes) + " J."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PublicName(test.first, test.last); got != test.want {
				t.Fatalf("PublicName(%q, %q) = %q, want %q", test.first, test.last, got, test.want)
			}
		})
	}
}

type userGetterStub struct {
	user *clerk.User
	err  error
}

func (stub userGetterStub) Get(context.Context, string) (*clerk.User, error) {
	return stub.user, stub.err
}

func TestDisplayNameReadsTheClerkProfile(t *testing.T) {
	first, last := "Maria", "Santos"
	fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{FirstName: &first, LastName: &last}}}

	name, err := fetcher.DisplayName(context.Background(), "user_1")
	if err != nil || name != "Maria S." {
		t.Fatalf("DisplayName = %q, %v", name, err)
	}
}

func TestDisplayNameReportsAProfileWithoutNames(t *testing.T) {
	fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{}}}

	if _, err := fetcher.DisplayName(context.Background(), "user_1"); !errors.Is(err, ErrNoDisplayName) {
		t.Fatalf("expected ErrNoDisplayName, got %v", err)
	}
}

func TestDisplayNameWrapsClerkFailures(t *testing.T) {
	failure := errors.New("clerk down")
	fetcher := &ClerkNameFetcher{users: userGetterStub{err: failure}}

	if _, err := fetcher.DisplayName(context.Background(), "user_1"); !errors.Is(err, failure) {
		t.Fatalf("expected wrapped clerk error, got %v", err)
	}
}
