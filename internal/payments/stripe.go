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
	"github.com/stripe/stripe-go/v82/customer"
	"github.com/stripe/stripe-go/v82/customersession"
	"github.com/stripe/stripe-go/v82/paymentintent"
	"github.com/stripe/stripe-go/v82/paymentmethod"
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
	if params.CustomerID != "" {
		request.Customer = stripe.String(params.CustomerID)
	}
	request.Context = ctx
	request.AddMetadata(MetadataBookingID, params.BookingID.String())
	request.AddMetadata(MetadataGuestID, params.GuestID.String())
	request.SetIdempotencyKey(params.IdempotencyKey + "-" + strings.Join(types, "+"))
	return gateway.client().New(request)
}

func (gateway *StripeGateway) CreateCustomer(ctx context.Context, accountID uuid.UUID) (string, error) {
	params := &stripe.CustomerParams{}
	params.Context = ctx
	params.AddMetadata(MetadataGuestID, accountID.String())
	params.SetIdempotencyKey("customer-" + accountID.String())
	client := customer.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	created, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe create customer: %w", err)
	}
	return created.ID, nil
}

func (gateway *StripeGateway) CreateCustomerSession(ctx context.Context, customerID string) (string, error) {
	params := customerSessionParams(customerID)
	params.Context = ctx
	client := customersession.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	session, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe create customer session: %w", err)
	}
	return session.ClientSecret, nil
}

func customerSessionParams(customerID string) *stripe.CustomerSessionParams {
	return &stripe.CustomerSessionParams{
		Customer: stripe.String(customerID),
		Components: &stripe.CustomerSessionComponentsParams{
			PaymentElement: &stripe.CustomerSessionComponentsPaymentElementParams{
				Enabled: stripe.Bool(true),
				Features: &stripe.CustomerSessionComponentsPaymentElementFeaturesParams{
					PaymentMethodRedisplay: stripe.String(string(stripe.CustomerSessionComponentsPaymentElementFeaturesPaymentMethodRedisplayEnabled)),
					PaymentMethodRemove:    stripe.String(string(stripe.CustomerSessionComponentsPaymentElementFeaturesPaymentMethodRemoveEnabled)),
					PaymentMethodSave:      stripe.String(string(stripe.CustomerSessionComponentsPaymentElementFeaturesPaymentMethodSaveEnabled)),
					PaymentMethodSaveUsage: stripe.String(string(stripe.CustomerSessionComponentsPaymentElementFeaturesPaymentMethodSaveUsageOnSession)),
				},
			},
		},
	}
}

func (gateway *StripeGateway) ListCards(ctx context.Context, customerID string) ([]SavedCard, error) {
	params := &stripe.PaymentMethodListParams{
		Customer: stripe.String(customerID),
		Type:     stripe.String(string(stripe.PaymentMethodTypeCard)),
	}
	params.Context = ctx
	client := paymentmethod.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	found := client.List(params)
	cards := []SavedCard{}
	for found.Next() {
		if card, ok := savedCardFrom(found.PaymentMethod()); ok {
			cards = append(cards, card)
		}
	}
	if err := found.Err(); err != nil {
		return nil, fmt.Errorf("stripe list cards: %w", err)
	}
	return cards, nil
}

func (gateway *StripeGateway) DetachCard(ctx context.Context, cardID string) error {
	params := &stripe.PaymentMethodDetachParams{}
	params.Context = ctx
	client := paymentmethod.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	if _, err := client.Detach(cardID, params); err != nil {
		return fmt.Errorf("stripe detach card: %w", err)
	}
	return nil
}

const (
	metadataArtCardID = "vgs_card_id"
	metadataBankName  = "card_bank"
	metadataColor     = "card_color"
)

func (gateway *StripeGateway) StyleCard(ctx context.Context, cardID string, style CardStyle) error {
	params := &stripe.PaymentMethodParams{}
	params.Context = ctx
	params.AddMetadata(metadataBankName, style.BankName)
	params.AddMetadata(metadataColor, style.Color)
	client := paymentmethod.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	if _, err := client.Update(cardID, params); err != nil {
		return fmt.Errorf("stripe style card: %w", err)
	}
	return nil
}

func (gateway *StripeGateway) LinkArtCard(ctx context.Context, cardID, artCardID string) error {
	params := &stripe.PaymentMethodParams{}
	params.Context = ctx
	params.AddMetadata(metadataArtCardID, artCardID)
	client := paymentmethod.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	if _, err := client.Update(cardID, params); err != nil {
		return fmt.Errorf("stripe link card art: %w", err)
	}
	return nil
}

func savedCardFrom(method *stripe.PaymentMethod) (SavedCard, bool) {
	if method == nil || method.Card == nil {
		return SavedCard{}, false
	}
	card := SavedCard{
		ID:       method.ID,
		Brand:    string(method.Card.Brand),
		Last4:    method.Card.Last4,
		ExpMonth: int(method.Card.ExpMonth),
		ExpYear:  int(method.Card.ExpYear),
		Funding:  string(method.Card.Funding),

		CardStyle: CardStyle{BankName: method.Metadata[metadataBankName], Color: method.Metadata[metadataColor]},
		ArtCardID: method.Metadata[metadataArtCardID],
	}
	if method.Card.Wallet != nil {
		card.Wallet = string(method.Card.Wallet.Type)
	}
	if details := method.BillingDetails; details != nil {
		card.Billing.Name = details.Name
		if address := details.Address; address != nil {
			card.Billing.Line1 = address.Line1
			card.Billing.Line2 = address.Line2
			card.Billing.City = address.City
			card.Billing.State = address.State
			card.Billing.PostalCode = address.PostalCode
			card.Billing.Country = address.Country
		}
	}
	return card, true
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
