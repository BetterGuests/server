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
	namePlaceholder  = ""
)

var ErrNoDisplayName = errors.New("clerk user has no name")

type userGetter interface {
	Get(ctx context.Context, id string) (*clerk.User, error)
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

func (fetcher *ClerkNameFetcher) DisplayName(ctx context.Context, clerkUserID string) (string, error) {
	profile, err := fetcher.users.Get(ctx, clerkUserID)
	if err != nil {
		return namePlaceholder, fmt.Errorf("fetch clerk user: %w", err)
	}
	name := PublicName(deref(profile.FirstName), deref(profile.LastName))
	if name == namePlaceholder {
		return namePlaceholder, ErrNoDisplayName
	}
	return name, nil
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
