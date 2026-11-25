package settlements

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type memoryAccounts struct{ saved string }

func (accounts *memoryAccounts) HostAccount(context.Context, uuid.UUID) (string, bool, error) {
	return accounts.saved, accounts.saved != "", nil
}

func (accounts *memoryAccounts) SaveHostAccount(_ context.Context, _ uuid.UUID, accountID string) (string, error) {
	accounts.saved = accountID
	return accountID, nil
}

type fakeOnboarder struct{ createdWith []string }

func (onboarder *fakeOnboarder) CreateAccount(_ context.Context, _ uuid.UUID, email string) (string, error) {
	onboarder.createdWith = append(onboarder.createdWith, email)
	return "acct_new", nil
}

func (onboarder *fakeOnboarder) OnboardingLink(_ context.Context, accountID, returnURL, _ string) (string, error) {
	return "https://connect.example.com/" + accountID + "?back=" + returnURL, nil
}

func (onboarder *fakeOnboarder) TransfersActive(context.Context, string) (bool, error) {
	return false, nil
}

type fakeEmails struct{ err error }

func (emails fakeEmails) VerifiedEmail(context.Context, uuid.UUID) (string, error) {
	if emails.err != nil {
		return "", emails.err
	}
	return "host@example.com", nil
}

func TestOnboardingCreatesOneAccountWithTheHostsVerifiedEmail(t *testing.T) {
	accounts, onboarder := &memoryAccounts{}, &fakeOnboarder{}
	service := NewPayoutService(accounts, onboarder, fakeEmails{}, Config{WebURL: "https://app.example.com"})
	for range 2 {
		link, err := service.StartOnboarding(context.Background(), uuid.New())
		if err != nil || link != "https://connect.example.com/acct_new?back=https://app.example.com/hosting/payouts" {
			t.Fatalf("link = %q, err = %v", link, err)
		}
	}
	if len(onboarder.createdWith) != 1 || onboarder.createdWith[0] != "host@example.com" {
		t.Fatalf("created = %v", onboarder.createdWith)
	}
}

func TestNoAccountIsCreatedWithoutAVerifiedEmail(t *testing.T) {
	onboarder := &fakeOnboarder{}
	service := NewPayoutService(&memoryAccounts{}, onboarder, fakeEmails{err: errors.New("unverified")}, Config{WebURL: "https://app.example.com"})
	if _, err := service.StartOnboarding(context.Background(), uuid.New()); err == nil || len(onboarder.createdWith) != 0 {
		t.Fatalf("err = %v, created = %v", err, onboarder.createdWith)
	}
}
