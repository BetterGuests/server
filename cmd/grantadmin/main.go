package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/logging"
	"github.com/jackc/pgx/v5"
)

const (
	clerkUsersURL   = "https://api.clerk.com/v1/users"
	clerkTimeout    = 15 * time.Second
	maxClerkPayload = 1 << 20
	verifiedStatus  = "verified"
)

var (
	errUsage               = errors.New("usage: grantadmin <email> [<email>...]")
	errMissingDatabaseURL  = errors.New("MIGRATION_DATABASE_URL is required")
	errMissingClerkKey     = errors.New("CLERK_SECRET_KEY is required")
	errNoVerifiedUser      = errors.New("no Clerk user has this email as a verified address")
	errAmbiguousClerkUsers = errors.New("more than one Clerk user has this verified email")
)

type clerkUser struct {
	ID             string `json:"id"`
	EmailAddresses []struct {
		EmailAddress string `json:"email_address"`
		Verification struct {
			Status string `json:"status"`
		} `json:"verification"`
	} `json:"email_addresses"`
}

func main() {
	logging.Configure()
	if err := run(os.Args[1:]); err != nil {
		slog.Error("Grant admin failed", "error", err)
		os.Exit(1)
	}
}

func run(emails []string) error {
	if len(emails) == 0 {
		return errUsage
	}
	databaseURL := os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" {
		return errMissingDatabaseURL
	}
	clerkKey := os.Getenv("CLERK_SECRET_KEY")
	if clerkKey == "" {
		return errMissingClerkKey
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to migration database: %w", err)
	}
	defer conn.Close(ctx)

	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		clerkID, err := verifiedClerkUserID(ctx, http.DefaultClient, clerkKey, email)
		if err != nil {
			return fmt.Errorf("%s: %w", email, err)
		}
		accountID := accounts.DeterministicID(clerkID)
		_, err = conn.Exec(ctx, `
			INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)
			ON CONFLICT (account_id) DO NOTHING`, accountID, email)
		if err != nil {
			return fmt.Errorf("%s: insert admin: %w", email, err)
		}
		slog.Info("Granted admin", "email", email, "account_id", accountID)
	}
	return nil
}

func verifiedClerkUserID(ctx context.Context, client *http.Client, secretKey, email string) (string, error) {
	query := url.Values{"email_address": {email}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clerkUsersURL+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+secretKey)
	request.Header.Set("Accept", "application/json")

	ctx, cancel := context.WithTimeout(ctx, clerkTimeout)
	defer cancel()
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("query Clerk: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("query Clerk: unexpected status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxClerkPayload))
	if err != nil {
		return "", fmt.Errorf("read Clerk response: %w", err)
	}
	return pickVerifiedUser(body, email)
}

func pickVerifiedUser(body []byte, email string) (string, error) {
	var users []clerkUser
	if err := json.Unmarshal(body, &users); err != nil {
		return "", fmt.Errorf("decode Clerk response: %w", err)
	}
	matches := make([]string, 0, 1)
	for _, user := range users {
		for _, address := range user.EmailAddresses {
			if strings.EqualFold(address.EmailAddress, email) && address.Verification.Status == verifiedStatus {
				matches = append(matches, user.ID)
				break
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", errNoVerifiedUser
	case 1:
		return matches[0], nil
	default:
		return "", errAmbiguousClerkUsers
	}
}
