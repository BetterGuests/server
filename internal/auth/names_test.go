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

func TestPublicProfileReadsTheNameAndUploadedPhoto(t *testing.T) {
	first, last, photo := "Maria", "Santos", "https://img.clerk.com/maria"
	fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{FirstName: &first, LastName: &last, HasImage: true, ImageURL: &photo}}}

	profile, err := fetcher.PublicProfile(context.Background(), "user_1")
	if err != nil || profile != (PublicProfile{DisplayName: "Maria S.", AvatarURL: photo}) {
		t.Fatalf("PublicProfile = %+v, %v", profile, err)
	}
}

func TestPublicProfileLeavesOutUnusablePhotos(t *testing.T) {
	first := "Maria"
	cases := map[string]struct {
		hasImage bool
		url      string
	}{
		"clerk placeholder": {false, "https://img.clerk.com/default"},
		"plain http":        {true, "http://img.clerk.com/maria"},
		"overlong":          {true, "https://img.clerk.com/" + strings.Repeat("a", maxPhotoURLBytes)},
		"missing":           {true, ""},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{FirstName: &first, HasImage: test.hasImage, ImageURL: &test.url}}}
			profile, err := fetcher.PublicProfile(context.Background(), "user_1")
			if err != nil || profile.AvatarURL != "" {
				t.Fatalf("PublicProfile = %+v, %v", profile, err)
			}
		})
	}
}

func TestPublicProfileReportsAProfileWithoutNames(t *testing.T) {
	fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{}}}

	if _, err := fetcher.PublicProfile(context.Background(), "user_1"); !errors.Is(err, ErrNoDisplayName) {
		t.Fatalf("expected ErrNoDisplayName, got %v", err)
	}
}

func TestPublicProfileWrapsClerkFailures(t *testing.T) {
	failure := errors.New("clerk down")
	fetcher := &ClerkNameFetcher{users: userGetterStub{err: failure}}

	if _, err := fetcher.PublicProfile(context.Background(), "user_1"); !errors.Is(err, failure) {
		t.Fatalf("expected wrapped clerk error, got %v", err)
	}
}

func TestVerifiedEmailReturnsOnlyAVerifiedPrimaryAddress(t *testing.T) {
	primary := "idn_1"
	address := func(id, status string) *clerk.EmailAddress {
		return &clerk.EmailAddress{ID: id, EmailAddress: id + "@example.com", Verification: &clerk.Verification{Status: status}}
	}
	cases := map[string]struct {
		addresses []*clerk.EmailAddress
		want      string
	}{
		"verified primary":            {[]*clerk.EmailAddress{address("idn_2", "verified"), address("idn_1", "verified")}, "idn_1@example.com"},
		"unverified primary":          {[]*clerk.EmailAddress{address("idn_1", "unverified"), address("idn_2", "verified")}, ""},
		"primary without a check":     {[]*clerk.EmailAddress{{ID: "idn_1", EmailAddress: "a@example.com"}}, ""},
		"no addresses on the profile": {nil, ""},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			fetcher := &ClerkNameFetcher{users: userGetterStub{user: &clerk.User{PrimaryEmailAddressID: &primary, EmailAddresses: test.addresses}}}
			email, err := fetcher.VerifiedEmail(context.Background(), "user_1")
			if email != test.want || (test.want == "") != errors.Is(err, ErrNoVerifiedEmail) {
				t.Fatalf("email = %q, err = %v", email, err)
			}
		})
	}
}
