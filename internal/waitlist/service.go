package waitlist

import (
	"context"
	"net/mail"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var countryPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]$`)

type WaitlistService struct{ store Store }

func NewWaitlistService(store Store) *WaitlistService {
	return &WaitlistService{store: store}
}

func (service *WaitlistService) Join(ctx context.Context, email, country string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if country != "" && !countryPattern.MatchString(country) {
		return ErrInvalidCountry
	}
	return service.store.Add(ctx, Signup{ID: uuid.New(), Email: email, Country: country})
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) > maxEmailBytes {
		return "", ErrInvalidEmail
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", ErrInvalidEmail
	}
	_, domain, _ := strings.Cut(email, "@")
	if !strings.Contains(strings.Trim(domain, "."), ".") {
		return "", ErrInvalidEmail
	}
	return email, nil
}
