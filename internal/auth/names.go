package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

const (
	maxNamePartRunes = 40
	maxPhotoURLBytes = 2000
	namePlaceholder  = ""
)

var ErrNoDisplayName = errors.New("clerk user has no name")

var ErrNoVerifiedEmail = errors.New("clerk user has no verified primary email")

const verifiedStatus = "verified"

type userGetter interface {
	Get(ctx context.Context, id string) (*clerk.User, error)
}

type PublicProfile struct {
	DisplayName string
	AvatarURL   string
}

type ClerkNameFetcher struct{ users userGetter }

func NewClerkNameFetcher(cfg ClerkConfig) (*ClerkNameFetcher, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("clerk secret key is required")
	}
	client := user.NewClient(&clerk.ClientConfig{
		BackendConfig: clerk.BackendConfig{Key: &cfg.SecretKey},
	})
	return &ClerkNameFetcher{users: client}, nil
}

func (fetcher *ClerkNameFetcher) PublicProfile(ctx context.Context, clerkUserID string) (PublicProfile, error) {
	profile, err := fetcher.users.Get(ctx, clerkUserID)
	if err != nil {
		return PublicProfile{}, fmt.Errorf("fetch clerk user: %w", err)
	}
	name := PublicName(deref(profile.FirstName), deref(profile.LastName))
	if name == namePlaceholder {
		return PublicProfile{}, ErrNoDisplayName
	}
	return PublicProfile{DisplayName: name, AvatarURL: uploadedPhoto(profile)}, nil
}

func uploadedPhoto(profile *clerk.User) string {
	url := deref(profile.ImageURL)
	if !profile.HasImage || !strings.HasPrefix(url, "https://") || len(url) > maxPhotoURLBytes {
		return ""
	}
	return url
}

func (fetcher *ClerkNameFetcher) VerifiedEmail(ctx context.Context, clerkUserID string) (string, error) {
	profile, err := fetcher.users.Get(ctx, clerkUserID)
	if err != nil {
		return "", fmt.Errorf("fetch clerk user: %w", err)
	}
	primary := deref(profile.PrimaryEmailAddressID)
	for _, address := range profile.EmailAddresses {
		if address.ID == primary && address.Verification != nil && address.Verification.Status == verifiedStatus {
			return address.EmailAddress, nil
		}
	}
	return "", ErrNoVerifiedEmail
}

func PublicName(first, last string) string {
	first = clip(strings.TrimSpace(first))
	last = strings.TrimSpace(last)
	switch {
	case first == "" && last == "":
		return namePlaceholder
	case last == "":
		return first
	case first == "":
		return clip(last)
	}
	initial, _ := utf8.DecodeRuneInString(last)
	return first + " " + strings.ToUpper(string(initial)) + "."
}

func clip(part string) string {
	if utf8.RuneCountInString(part) <= maxNamePartRunes {
		return part
	}
	return string([]rune(part)[:maxNamePartRunes])
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
