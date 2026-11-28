package settlements

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/dispute"
	"github.com/stripe/stripe-go/v82/paymentintent"
	"github.com/stripe/stripe-go/v82/rawrequest"
	"github.com/stripe/stripe-go/v82/refund"
	"github.com/stripe/stripe-go/v82/transfer"
	"github.com/stripe/stripe-go/v82/transferreversal"
)

const (
	accountsAPIVersion = "2026-09-30.endive"
	accountsPath       = "/v2/core/accounts"
	accountLinksPath   = "/v2/core/account_links"
	metadataDisputeID  = "dispute_id"
	capabilityActive   = "active"
	hostCountry        = "us"
	platformWebsite    = "https://better-guests.com"
	hostDescription    = "Hosts a short term rental booked and paid through Better Guests."
)

type StripeGateway struct{ secretKey string }

func NewStripeGateway(secretKey string) *StripeGateway {
	return &StripeGateway{secretKey: secretKey}
}

func transferGroup(bookingID uuid.UUID) string {
	return "booking_" + bookingID.String()
}

func (gateway *StripeGateway) Charge(ctx context.Context, paymentIntentID string, bookingID uuid.UUID) (Charge, error) {
	params := &stripe.PaymentIntentParams{}
	params.Context = ctx
	params.AddExpand("latest_charge")
	intents := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	intent, err := intents.Get(paymentIntentID, params)
	if err != nil {
		return Charge{}, fmt.Errorf("stripe get payment intent: %w", err)
	}
	if intent.LatestCharge == nil {
		return Charge{}, fmt.Errorf("stripe payment intent %s has no charge", paymentIntentID)
	}
	charge := Charge{
		ID:            intent.LatestCharge.ID,
		CapturedCents: int(intent.LatestCharge.AmountCaptured),
		RefundedCents: int(intent.LatestCharge.AmountRefunded),
	}
	if intent.LatestCharge.Disputed {
		statuses, err := gateway.disputeStatuses(ctx, paymentIntentID)
		if err != nil {
			return Charge{}, err
		}
		charge.Disputed = disputeStands(statuses)
	}

	list := &stripe.RefundListParams{PaymentIntent: stripe.String(paymentIntentID)}
	list.Context = ctx
	refunds := refund.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	found := refunds.List(list)
	for found.Next() {
		item := found.Refund()
		if item.Metadata[MetadataBookingID] != bookingID.String() {
			continue
		}
		if item.Status == stripe.RefundStatusFailed || item.Status == stripe.RefundStatusCanceled {
			charge.SettlementRefundDead = true
			continue
		}
		charge.SettlementRefundID = item.ID
		charge.SettlementRefundCents = int(item.Amount)
	}
	if err := found.Err(); err != nil {
		return Charge{}, fmt.Errorf("stripe list refunds: %w", err)
	}
	return charge, nil
}

func (gateway *StripeGateway) disputeStatuses(ctx context.Context, paymentIntentID string) ([]stripe.DisputeStatus, error) {
	params := &stripe.DisputeListParams{PaymentIntent: stripe.String(paymentIntentID)}
	params.Context = ctx
	client := dispute.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	found := client.List(params)
	var statuses []stripe.DisputeStatus
	for found.Next() {
		statuses = append(statuses, found.Dispute().Status)
	}
	if err := found.Err(); err != nil {
		return nil, fmt.Errorf("stripe list disputes: %w", err)
	}
	return statuses, nil
}

func disputeStands(statuses []stripe.DisputeStatus) bool {
	for _, status := range statuses {
		if status != stripe.DisputeStatusWon && status != stripe.DisputeStatusWarningClosed {
			return true
		}
	}
	return len(statuses) == 0
}

func (gateway *StripeGateway) Refund(ctx context.Context, request RefundRequest) (string, error) {
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(request.PaymentIntentID),
		Amount:        stripe.Int64(int64(request.AmountCents)),
		Reason:        stripe.String(string(stripe.RefundReasonRequestedByCustomer)),
	}
	params.Context = ctx
	params.AddMetadata(MetadataBookingID, request.BookingID.String())
	params.SetIdempotencyKey("settlement-refund-" + request.BookingID.String())
	client := refund.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	created, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe refund: %w", err)
	}
	if created.Status == stripe.RefundStatusFailed || created.Status == stripe.RefundStatusCanceled {
		return "", fmt.Errorf("stripe refund %s is %s", created.ID, created.Status)
	}
	return created.ID, nil
}

func (gateway *StripeGateway) transfers() transfer.Client {
	return transfer.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
}

func (gateway *StripeGateway) FindTransfer(ctx context.Context, bookingID uuid.UUID) (string, bool, error) {
	params := &stripe.TransferListParams{TransferGroup: stripe.String(transferGroup(bookingID))}
	params.Context = ctx
	found := gateway.transfers().List(params)
	for found.Next() {
		item := found.Transfer()
		if item.Metadata[MetadataBookingID] == bookingID.String() && !item.Reversed {
			return item.ID, true, nil
		}
	}
	if err := found.Err(); err != nil {
		return "", false, fmt.Errorf("stripe list transfers: %w", err)
	}
	return "", false, nil
}

func transferParams(request TransferRequest) *stripe.TransferParams {
	params := &stripe.TransferParams{
		Amount:            stripe.Int64(int64(request.AmountCents)),
		Currency:          stripe.String(request.Currency),
		Destination:       stripe.String(request.Destination),
		SourceTransaction: stripe.String(request.ChargeID),
		TransferGroup:     stripe.String(transferGroup(request.BookingID)),
	}
	params.AddMetadata(MetadataBookingID, request.BookingID.String())
	params.SetIdempotencyKey("settlement-transfer-" + request.BookingID.String())
	return params
}

func (gateway *StripeGateway) Transfer(ctx context.Context, request TransferRequest) (string, error) {
	params := transferParams(request)
	params.Context = ctx
	created, err := gateway.transfers().New(params)
	if err != nil {
		return "", fmt.Errorf("stripe transfer: %w", err)
	}
	return created.ID, nil
}

func (gateway *StripeGateway) Reverse(ctx context.Context, request ReversalRequest) (string, error) {
	client := transferreversal.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	list := &stripe.TransferReversalListParams{ID: stripe.String(request.TransferID)}
	list.Context = ctx
	found := client.List(list)
	for found.Next() {
		if item := found.TransferReversal(); item.Metadata[metadataDisputeID] == request.DisputeID {
			return item.ID, nil
		}
	}
	if err := found.Err(); err != nil {
		return "", fmt.Errorf("stripe list reversals: %w", err)
	}
	params := reversalParams(request)
	params.Context = ctx
	created, err := client.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe reverse transfer: %w", err)
	}
	return created.ID, nil
}

func reversalParams(request ReversalRequest) *stripe.TransferReversalParams {
	params := &stripe.TransferReversalParams{
		ID:     stripe.String(request.TransferID),
		Amount: stripe.Int64(int64(request.AmountCents)),
	}
	params.AddMetadata(metadataDisputeID, request.DisputeID)
	params.SetIdempotencyKey("settlement-reversal-" + request.DisputeID)
	return params
}

func recipientAccountBody(hostID uuid.UUID, contactEmail string) map[string]any {
	return map[string]any{
		"contact_email": contactEmail,
		"dashboard":     "express",
		"identity":      map[string]any{"country": hostCountry},
		"configuration": map[string]any{"recipient": map[string]any{"capabilities": map[string]any{
			"stripe_balance": map[string]any{"stripe_transfers": map[string]any{"requested": true}},
		}}},
		"defaults": map[string]any{
			"profile": map[string]any{
				"business_url":        platformWebsite,
				"product_description": hostDescription,
			},
			"responsibilities": map[string]any{
				"fees_collector":   "application",
				"losses_collector": "application",
			},
		},
		"metadata": map[string]any{"host_id": hostID.String()},
	}
}

func onboardingLinkBody(accountID, returnURL, refreshURL string) map[string]any {
	return map[string]any{
		"account": accountID,
		"use_case": map[string]any{
			"type": "account_onboarding",
			"account_onboarding": map[string]any{
				"return_url":  returnURL,
				"refresh_url": refreshURL,
			},
		},
	}
}

func (gateway *StripeGateway) accountsCall(ctx context.Context, method, path string, body any, idempotencyKey string, into any) error {
	content := ""
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		content = string(encoded)
	}
	params := &stripe.RawParams{}
	params.Context = ctx
	params.Headers = http.Header{"Stripe-Version": []string{accountsAPIVersion}}
	if idempotencyKey != "" {
		params.SetIdempotencyKey(idempotencyKey)
	}
	backend, ok := stripe.GetBackend(stripe.APIBackend).(stripe.RawRequestBackend)
	if !ok {
		return fmt.Errorf("stripe backend does not support raw requests")
	}
	response, err := rawrequest.Client{B: backend, Key: gateway.secretKey}.RawRequest(method, path, content, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(response.RawJSON, into); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (gateway *StripeGateway) CreateAccount(ctx context.Context, hostID uuid.UUID, contactEmail string) (string, error) {
	var account struct {
		ID string `json:"id"`
	}
	err := gateway.accountsCall(ctx, http.MethodPost, accountsPath, recipientAccountBody(hostID, contactEmail), "payout-account-"+hostID.String(), &account)
	if err != nil {
		return "", fmt.Errorf("stripe create account: %w", err)
	}
	return account.ID, nil
}

func (gateway *StripeGateway) OnboardingLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error) {
	var link struct {
		URL string `json:"url"`
	}
	err := gateway.accountsCall(ctx, http.MethodPost, accountLinksPath, onboardingLinkBody(accountID, returnURL, refreshURL), "", &link)
	if err != nil {
		return "", fmt.Errorf("stripe create account link: %w", err)
	}
	return link.URL, nil
}

type recipientAccount struct {
	Configuration struct {
		Recipient struct {
			Capabilities struct {
				StripeBalance struct {
					StripeTransfers struct {
						Status string `json:"status"`
					} `json:"stripe_transfers"`
				} `json:"stripe_balance"`
			} `json:"capabilities"`
		} `json:"recipient"`
	} `json:"configuration"`
}

func (gateway *StripeGateway) TransfersActive(ctx context.Context, accountID string) (bool, error) {
	var account recipientAccount
	path := accountsPath + "/" + url.PathEscape(accountID) + "?include[0]=configuration.recipient"
	if err := gateway.accountsCall(ctx, http.MethodGet, path, nil, "", &account); err != nil {
		return false, fmt.Errorf("stripe get account: %w", err)
	}
	return account.Configuration.Recipient.Capabilities.StripeBalance.StripeTransfers.Status == capabilityActive, nil
}
