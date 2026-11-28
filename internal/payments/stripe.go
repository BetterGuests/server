package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	types := paymentMethodTypes(params.AmountCents, params.Currency)
	intent, err := gateway.newIntent(ctx, params, types)
	if payLaterWasRefused(err, types) {
		slog.Warn("Pay later was refused, offering cards alone", "error", err)
		intent, err = gateway.newIntent(ctx, params, []string{string(stripe.PaymentMethodTypeCard)})
	}
	if err != nil {
		return Intent{}, fmt.Errorf("stripe create payment intent: %w", err)
	}
	return Intent{ID: intent.ID, ClientSecret: intent.ClientSecret}, nil
}

func (gateway *StripeGateway) newIntent(ctx context.Context, params IntentParams, types []string) (*stripe.PaymentIntent, error) {
	request := &stripe.PaymentIntentParams{
		Amount:             stripe.Int64(int64(params.AmountCents)),
		Currency:           stripe.String(params.Currency),
		PaymentMethodTypes: stripe.StringSlice(types),
	}
	if params.ManualCapture {
		request.CaptureMethod = stripe.String(string(stripe.PaymentIntentCaptureMethodManual))
	}
	request.Context = ctx
	request.AddMetadata(MetadataBookingID, params.BookingID.String())
	request.AddMetadata(MetadataGuestID, params.GuestID.String())
	request.SetIdempotencyKey(params.IdempotencyKey + "-" + strings.Join(types, "+"))
	return gateway.client().New(request)
}

func (gateway *StripeGateway) client() paymentintent.Client {
	return paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
}

func (gateway *StripeGateway) Lookup(ctx context.Context, intentID string) (IntentState, bool, error) {
	params := &stripe.PaymentIntentParams{}
	params.Context = ctx
	intent, err := gateway.client().Get(intentID, params)
	if missingIntent(err) {
		return IntentState{}, false, nil
	}
	if err != nil {
		return IntentState{}, false, fmt.Errorf("stripe get payment intent: %w", err)
	}
	return intentState(intent), true, nil
}

func intentState(intent *stripe.PaymentIntent) IntentState {
	return IntentState{
		Intent:        Intent{ID: intent.ID, ClientSecret: intent.ClientSecret},
		Status:        lookedUpStatus(intent.Status),
		AmountCents:   int(intent.Amount),
		ManualCapture: intent.CaptureMethod == stripe.PaymentIntentCaptureMethodManual,
	}
}

func lookedUpStatus(status stripe.PaymentIntentStatus) Status {
	switch status {
	case stripe.PaymentIntentStatusRequiresCapture:
		return StatusAuthorized
	case stripe.PaymentIntentStatusSucceeded:
		return StatusSucceeded
	case stripe.PaymentIntentStatusProcessing:
		return StatusProcessing
	case stripe.PaymentIntentStatusCanceled:
		return StatusCanceled
	}
	return StatusPending
}

func missingIntent(err error) bool {
	var failure *stripe.Error
	return errors.As(err, &failure) && failure.Code == stripe.ErrorCodeResourceMissing
}

func (gateway *StripeGateway) Capture(ctx context.Context, intentID string) (Status, error) {
	params := &stripe.PaymentIntentCaptureParams{}
	params.Context = ctx
	params.SetIdempotencyKey("capture-" + intentID)
	intent, err := gateway.client().Capture(intentID, params)
	if err != nil {
		return "", fmt.Errorf("stripe capture payment intent: %w", err)
	}
	if intent.Status != stripe.PaymentIntentStatusSucceeded {
		return StatusPending, nil
	}
	return StatusSucceeded, nil
}

func (gateway *StripeGateway) Cancel(ctx context.Context, intentID string) error {
	params := &stripe.PaymentIntentCancelParams{}
	params.Context = ctx
	_, err := gateway.client().Cancel(intentID, params)
	if err != nil && !alreadyCanceled(err) {
		return fmt.Errorf("stripe cancel payment intent: %w", err)
	}
	return nil
}

func alreadyCanceled(err error) bool {
	var failure *stripe.Error
	return errors.As(err, &failure) &&
		failure.Code == stripe.ErrorCodePaymentIntentUnexpectedState &&
		failure.PaymentIntent != nil &&
		failure.PaymentIntent.Status == stripe.PaymentIntentStatusCanceled
}

func (gateway *StripeGateway) ParseEvent(body []byte, signature string) (Event, error) {
	event, err := webhook.ConstructEventWithOptions(body, signature, gateway.webhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	parsed := Event{ID: event.ID, Type: string(event.Type), OccurredAt: time.Unix(event.Created, 0).UTC()}
	if isDisputeEvent(parsed.Type) {
		var dispute stripe.Dispute
		if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
			return Event{}, errors.Join(ErrInvalidPayload, err)
		}
		parsed.Dispute = &Dispute{ID: dispute.ID, Status: string(dispute.Status), Reason: string(dispute.Reason), AmountCents: int(dispute.Amount)}
		if dispute.PaymentIntent != nil {
			parsed.Dispute.IntentID = dispute.PaymentIntent.ID
		}
		return parsed, nil
	}
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
