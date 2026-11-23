package cashback

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/refund"
)

type StripeRefunder struct{ secretKey string }

func NewStripeRefunder(secretKey string) *StripeRefunder {
	return &StripeRefunder{secretKey: secretKey}
}

func (refunder *StripeRefunder) RefundPayment(ctx context.Context, paymentIntentID string, amountCents int, idempotencyKey string) (string, error) {
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
		Amount:        stripe.Int64(int64(amountCents)),
		Reason:        stripe.String(string(stripe.RefundReasonRequestedByCustomer)),
	}
	params.Context = ctx
	params.SetIdempotencyKey(idempotencyKey)

	client := refund.Client{B: stripe.GetBackend(stripe.APIBackend), Key: refunder.secretKey}
	created, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe refund: %w", err)
	}
	return created.ID, nil
}
