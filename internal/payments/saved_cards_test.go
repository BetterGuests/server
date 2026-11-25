package payments

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type walletStub struct {
	created     int
	createErr   error
	sessionErr  error
	sessionsFor []string
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
