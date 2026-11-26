package payments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func styleCard(wallet *walletStub, cardID string, style CardStyle) error {
	return newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).
		WithSavedCards(wallet, &customerStoreStub{existing: "cus_old"}).
		StyleCard(context.Background(), uuid.New(), cardID, style)
}

func TestGuestCanStyleTheirOwnCardWithATidiedBankName(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{{ID: "pm_mine"}}}

	err := styleCard(wallet, "pm_mine", CardStyle{BankName: "  First   Mountain  ", Color: "forest"})

	want := CardStyle{BankName: "First Mountain", Color: "forest"}
	if err != nil || wallet.styled["pm_mine"] != want {
		t.Fatalf("err = %v, styled = %v", err, wallet.styled)
	}
}

func TestAStyleThatCouldHoldACardNumberOrBreakTheCardIsRefused(t *testing.T) {
	tests := map[string]CardStyle{
		"full number":    {BankName: "4242424242424242"},
		"spaced number":  {BankName: "4242 4242 4242"},
		"dashed number":  {BankName: "4242-42"},
		"too long":       {BankName: strings.Repeat("a", maxBankNameRunes+1)},
		"hidden control": {BankName: "Bank\u0000"},
		"unlisted color": {Color: "url(evil)"},
	}
	for name, style := range tests {
		t.Run(name, func(t *testing.T) {
			wallet := &walletStub{cards: []SavedCard{{ID: "pm_mine"}}}

			err := styleCard(wallet, "pm_mine", style)

			if !errors.Is(err, ErrInvalidCardStyle) || len(wallet.styled) != 0 {
				t.Fatalf("err = %v, styled = %v", err, wallet.styled)
			}
		})
	}
}

func TestGuestCannotStyleSomeoneElsesCard(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{{ID: "pm_mine"}}}

	err := styleCard(wallet, "pm_theirs", CardStyle{Color: "ocean"})

	if !errors.Is(err, ErrCardNotFound) || len(wallet.styled) != 0 {
		t.Fatalf("err = %v, styled = %v", err, wallet.styled)
	}
}
