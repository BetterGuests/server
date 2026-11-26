package payments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
)

type walletStub struct {
	created     int
	createErr   error
	sessionErr  error
	sessionsFor []string
	cards       []SavedCard
	listedFor   []string
	detached    []string
	linked      map[string]string
	styled      map[string]CardStyle
}

func (stub *walletStub) StyleCard(_ context.Context, cardID string, style CardStyle) error {
	if stub.styled == nil {
		stub.styled = map[string]CardStyle{}
	}
	stub.styled[cardID] = style
	return nil
}

func (stub *walletStub) LinkArtCard(_ context.Context, cardID, artCardID string) error {
	if stub.linked == nil {
		stub.linked = map[string]string{}
	}
	stub.linked[cardID] = artCardID
	return nil
}

func (stub *walletStub) DetachCard(_ context.Context, cardID string) error {
	stub.detached = append(stub.detached, cardID)
	return nil
}

func (stub *walletStub) ListCards(_ context.Context, customerID string) ([]SavedCard, error) {
	stub.listedFor = append(stub.listedFor, customerID)
	return stub.cards, nil
}

func (stub *walletStub) CreateCustomer(context.Context, uuid.UUID) (string, error) {
	stub.created++
	return "cus_new", stub.createErr
}

func (stub *walletStub) CreateCustomerSession(_ context.Context, customerID string) (string, error) {
	stub.sessionsFor = append(stub.sessionsFor, customerID)
	return "cuss_secret", stub.sessionErr
}

type customerStoreStub struct {
	existing string
	saved    []string
}

func (stub *customerStoreStub) CustomerID(context.Context, uuid.UUID) (string, bool, error) {
	return stub.existing, stub.existing != "", nil
}

func (stub *customerStoreStub) SaveCustomerID(_ context.Context, _ uuid.UUID, customerID string) (string, error) {
	stub.saved = append(stub.saved, customerID)
	return customerID, nil
}

func checkoutWith(t *testing.T, wallet *walletStub, customers *customerStoreStub) (CheckoutResult, IntentParams) {
	t.Helper()
	quote := pricedQuote()
	quote.BookingStatus = "requested"
	intents := &fakeIntents{}
	service := newService(&fakeStore{quote: quote}, intents, fakeParser{}).WithSavedCards(wallet, customers)
	result, err := service.StartCheckout(context.Background(), uuid.New(), quote.BookingID)
	if err != nil {
		t.Fatalf("StartCheckout() error = %v", err)
	}
	return result, intents.params[0]
}

func TestFirstCheckoutCreatesAStripeCustomerAndOffersSavedCards(t *testing.T) {
	wallet, customers := &walletStub{}, &customerStoreStub{}

	result, params := checkoutWith(t, wallet, customers)

	if wallet.created != 1 || len(customers.saved) != 1 || customers.saved[0] != "cus_new" {
		t.Fatalf("created = %d, saved = %v", wallet.created, customers.saved)
	}
	if params.CustomerID != "cus_new" || result.CustomerSession != "cuss_secret" {
		t.Fatalf("params = %+v, result = %+v", params, result)
	}
}

func TestReturningGuestReusesTheirCustomer(t *testing.T) {
	wallet, customers := &walletStub{}, &customerStoreStub{existing: "cus_old"}

	_, params := checkoutWith(t, wallet, customers)

	if wallet.created != 0 || params.CustomerID != "cus_old" || wallet.sessionsFor[0] != "cus_old" {
		t.Fatalf("created = %d, customer = %q", wallet.created, params.CustomerID)
	}
}

func TestCheckoutStillWorksWhenSavedCardsFail(t *testing.T) {
	tests := map[string]*walletStub{
		"customer creation fails": {createErr: errors.New("stripe down")},
		"session creation fails":  {sessionErr: errors.New("stripe down")},
	}
	for name, wallet := range tests {
		t.Run(name, func(t *testing.T) {
			result, _ := checkoutWith(t, wallet, &customerStoreStub{})
			if result.ClientSecret == "" || result.CustomerSession != "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestGuestWithoutACustomerHasNoCardsAndNoneIsCreated(t *testing.T) {
	wallet := &walletStub{}
	service := newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).WithSavedCards(wallet, &customerStoreStub{})

	cards, err := service.SavedCards(context.Background(), uuid.New())

	if err != nil || cards == nil || len(cards) != 0 || wallet.created != 0 || len(wallet.listedFor) != 0 {
		t.Fatalf("cards = %#v, err = %v, created = %d", cards, err, wallet.created)
	}
}

func TestSavedCardsAreListedForTheGuestsOwnCustomer(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{{ID: "pm_1", Last4: "4242"}}}
	service := newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).WithSavedCards(wallet, &customerStoreStub{existing: "cus_old"})

	cards, err := service.SavedCards(context.Background(), uuid.New())

	if err != nil || len(cards) != 1 || cards[0].Last4 != "4242" || wallet.listedFor[0] != "cus_old" {
		t.Fatalf("cards = %+v, err = %v, listed = %v", cards, err, wallet.listedFor)
	}
}

func TestSavedCardKeepsBillingDetailsAndNeverTheFingerprint(t *testing.T) {
	card, ok := savedCardFrom(&stripe.PaymentMethod{
		ID: "pm_1",
		Card: &stripe.PaymentMethodCard{
			Brand: "visa", Last4: "4242", ExpMonth: 4, ExpYear: 2030, Funding: "credit", Fingerprint: "secret-fingerprint",
		},
		BillingDetails: &stripe.PaymentMethodBillingDetails{
			Name:    "Sam Guest",
			Address: &stripe.Address{PostalCode: "84101", Country: "US"},
		},
	})
	encoded, err := json.Marshal(card)

	if !ok || err != nil || card.Billing.PostalCode != "84101" || card.Billing.Name != "Sam Guest" {
		t.Fatalf("card = %+v, err = %v", card, err)
	}
	if strings.Contains(string(encoded), "secret-fingerprint") {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestNonCardPaymentMethodsAreSkipped(t *testing.T) {
	if _, ok := savedCardFrom(&stripe.PaymentMethod{ID: "pm_bank"}); ok {
		t.Fatal("a method without card details was listed")
	}
}

func TestGuestCanRemoveTheirOwnCard(t *testing.T) {
	wallet := &walletStub{cards: []SavedCard{{ID: "pm_mine"}}}
	service := newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).WithSavedCards(wallet, &customerStoreStub{existing: "cus_old"})

	err := service.RemoveCard(context.Background(), uuid.New(), "pm_mine")

	if err != nil || len(wallet.detached) != 1 || wallet.detached[0] != "pm_mine" {
		t.Fatalf("err = %v, detached = %v", err, wallet.detached)
	}
}

func TestGuestCannotRemoveACardThatIsNotTheirs(t *testing.T) {
	tests := map[string]*customerStoreStub{
		"another customer's card": {existing: "cus_old"},
		"no customer yet":         {},
	}
	for name, customers := range tests {
		t.Run(name, func(t *testing.T) {
			wallet := &walletStub{cards: []SavedCard{{ID: "pm_mine"}}}
			service := newService(&fakeStore{}, &fakeIntents{}, fakeParser{}).WithSavedCards(wallet, customers)

			err := service.RemoveCard(context.Background(), uuid.New(), "pm_someone_else")

			if !errors.Is(err, ErrCardNotFound) || len(wallet.detached) != 0 {
				t.Fatalf("err = %v, detached = %v", err, wallet.detached)
			}
		})
	}
}
