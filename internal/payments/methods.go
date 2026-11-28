package payments

import (
	"errors"

	"github.com/stripe/stripe-go/v82"
)

const (
	afterpayCurrency = "usd"
	afterpayMaxCents = 400_000
)

func paymentMethodTypes(amountCents int, currency string) []string {
	types := []string{
		string(stripe.PaymentMethodTypeCard),
		string(stripe.PaymentMethodTypeLink),
		string(stripe.PaymentMethodTypeKlarna),
	}
	if currency == afterpayCurrency && amountCents <= afterpayMaxCents {
		types = append(types, string(stripe.PaymentMethodTypeAfterpayClearpay))
	}
	return types
}

func payLaterWasRefused(err error, types []string) bool {
	var refused *stripe.Error
	return len(types) > 1 && errors.As(err, &refused) && refused.Type == stripe.ErrorTypeInvalidRequest
}
