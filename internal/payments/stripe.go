package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
	"github.com/stripe/stripe-go/v82/webhook"
)

type StripeGateway struct {
	secretKey     string
	webhookSecret string
}

func NewStripeGateway(config Config) *StripeGateway {
	return &StripeGateway{secretKey: config.SecretKey, webhookSecret: config.WebhookSecret}
}

func (gateway *StripeGateway) CreateIntent(ctx context.Context, params IntentParams) (Intent, error) {
	request := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(int64(params.AmountCents)),
		Currency: stripe.String(params.Currency),
	}
	if params.ManualCapture {
		request.CaptureMethod = stripe.String(string(stripe.PaymentIntentCaptureMethodManual))
		request.PaymentMethodTypes = stripe.StringSlice([]string{string(stripe.PaymentMethodTypeCard)})
	} else {
		request.AutomaticPaymentMethods = &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		}
	}
	request.Context = ctx
	request.AddMetadata(MetadataBookingID, params.BookingID.String())
	request.AddMetadata(MetadataGuestID, params.GuestID.String())
	request.SetIdempotencyKey(params.IdempotencyKey)

	client := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	intent, err := client.New(request)
	if err != nil {
		return Intent{}, fmt.Errorf("stripe create payment intent: %w", err)
	}
	return Intent{ID: intent.ID, ClientSecret: intent.ClientSecret}, nil
}

func (gateway *StripeGateway) client() paymentintent.Client {
	return paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
}

func (gateway *StripeGateway) Capture(ctx context.Context, intentID string) error {
	params := &stripe.PaymentIntentCaptureParams{}
	params.Context = ctx
	params.SetIdempotencyKey("capture-" + intentID)
	if _, err := gateway.client().Capture(intentID, params); err != nil {
		return fmt.Errorf("stripe capture payment intent: %w", err)
	}
	return nil
}

func (gateway *StripeGateway) Cancel(ctx context.Context, intentID string) error {
	params := &stripe.PaymentIntentCancelParams{}
	params.Context = ctx
	params.SetIdempotencyKey("cancel-" + intentID)
	if _, err := gateway.client().Cancel(intentID, params); err != nil {
		return fmt.Errorf("stripe cancel payment intent: %w", err)
	}
	return nil
}

func (gateway *StripeGateway) ParseEvent(body []byte, signature string) (Event, error) {
	event, err := webhook.ConstructEventWithOptions(body, signature, gateway.webhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	parsed := Event{ID: event.ID, Type: string(event.Type), OccurredAt: time.Unix(event.Created, 0).UTC()}
	if _, relevant := statusForEvent(parsed.Type); !relevant {
		return parsed, nil
	}

	var intent stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &intent); err != nil {
		return Event{}, errors.Join(ErrInvalidPayload, err)
	}
	parsed.IntentID = intent.ID
	if guestID, err := uuid.Parse(intent.Metadata[MetadataGuestID]); err == nil {
		parsed.GuestID = guestID
	}
	if bookingID, err := uuid.Parse(intent.Metadata[MetadataBookingID]); err == nil {
		parsed.BookingID = bookingID
	}
	return parsed, nil
}
