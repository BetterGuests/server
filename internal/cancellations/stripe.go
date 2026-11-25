package cancellations

import (
	"context"
	"fmt"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/refund"
)

type StripeGateway struct{ secretKey string }

func NewStripeGateway(secretKey string) *StripeGateway {
	return &StripeGateway{secretKey: secretKey}
}

func refundParams(pending PendingRefund) *stripe.RefundParams {
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(pending.PaymentIntentID),
		Amount:        stripe.Int64(int64(pending.AmountCents)),
		Reason:        stripe.String(string(stripe.RefundReasonRequestedByCustomer)),
	}
	params.AddMetadata(metadataKey(pending.Source), pending.BookingID.String())
	params.SetIdempotencyKey(refundIdempotencyPrefix + string(pending.Source) + "-" + pending.BookingID.String())
	return params
}

func metadataKey(source RefundSource) string {
	return string(source) + refundMetadataSuffix
}

func (gateway *StripeGateway) Refund(ctx context.Context, pending PendingRefund) (string, error) {
	client := refund.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	list := &stripe.RefundListParams{PaymentIntent: stripe.String(pending.PaymentIntentID)}
	list.Context = ctx
	found := client.List(list)
	for found.Next() {
		item := found.Refund()
		dead := item.Status == stripe.RefundStatusFailed || item.Status == stripe.RefundStatusCanceled
		if !dead && item.Metadata[metadataKey(pending.Source)] == pending.BookingID.String() {
			return item.ID, nil
		}
	}
	if err := found.Err(); err != nil {
		return "", fmt.Errorf("stripe list refunds: %w", err)
	}
	params := refundParams(pending)
	params.Context = ctx
	created, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe refund: %w", err)
	}
	if created.Status == stripe.RefundStatusFailed || created.Status == stripe.RefundStatusCanceled {
		return "", fmt.Errorf("stripe refund %s is %s", created.ID, created.Status)
	}
	return created.ID, nil
}
