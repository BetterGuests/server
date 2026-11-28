package payments

import (
	"errors"
	"slices"
	"testing"

	"github.com/stripe/stripe-go/v82"
)

func TestOnlyCardsLinkAndPayLaterAreOffered(t *testing.T) {
	tests := map[string]struct {
		amount   int
		currency string
		want     []string
	}{
		"at the Afterpay limit":   {afterpayMaxCents, "usd", []string{"card", "link", "klarna", "afterpay_clearpay"}},
		"over the Afterpay limit": {afterpayMaxCents + 1, "usd", []string{"card", "link", "klarna"}},
		"another currency":        {5000, "eur", []string{"card", "link", "klarna"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := paymentMethodTypes(test.amount, test.currency); !slices.Equal(got, test.want) {
				t.Fatalf("types = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOnlyARefusedPayLaterRequestFallsBackToCardsAlone(t *testing.T) {
	refused := &stripe.Error{Type: stripe.ErrorTypeInvalidRequest}
	tests := map[string]struct {
		err   error
		types []string
		want  bool
	}{
		"method not activated": {refused, []string{"card", "klarna"}, true},
		"already cards alone":  {refused, []string{"card"}, false},
		"stripe is down":       {&stripe.Error{Type: stripe.ErrorTypeAPI}, []string{"card", "klarna"}, false},
		"network failure":      {errors.New("timeout"), []string{"card", "klarna"}, false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := payLaterWasRefused(test.err, test.types); got != test.want {
				t.Fatalf("fallback = %v, want %v", got, test.want)
			}
		})
	}
}
