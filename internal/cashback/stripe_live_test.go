package cashback

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

func paidIntent(t *testing.T, key, paymentMethod string, amount int64) string {
	t.Helper()
	client := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: key}
	intent, err := client.New(&stripe.PaymentIntentParams{
		Amount:        stripe.Int64(amount),
		Currency:      stripe.String("usd"),
		PaymentMethod: stripe.String(paymentMethod),
		Confirm:       stripe.Bool(true),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled:        stripe.Bool(true),
			AllowRedirects: stripe.String("never"),
		},
	})
	if err != nil {
		t.Fatalf("create paid intent: %v", err)
	}
	if intent.Status != stripe.PaymentIntentStatusSucceeded {
		t.Fatalf("intent status = %s", intent.Status)
	}
	return intent.ID
}

func testKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(key, "sk_test_") {
		t.Skip("STRIPE_SECRET_KEY must be a sk_test_ key to run live Stripe test-mode checks")
	}
	return key
}

func TestStripeRefundsFiftyDollarsAndIsIdempotent(t *testing.T) {
	key := testKey(t)
	intentID := paidIntent(t, key, "pm_card_visa", 36000)
	refunder := NewStripeRefunder(key)
	idempotencyKey := "live-cashback-" + uuid.NewString()

	first, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, idempotencyKey)
	if err != nil {
		t.Fatalf("RefundPayment() error = %v", err)
	}
	second, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, idempotencyKey)
	if err != nil || first != second {
		t.Fatalf("second refund = %q, err = %v, want %q", second, err, first)
	}
}

func TestStripeRefundsEachBundleSeparately(t *testing.T) {
	key := testKey(t)
	intentID := paidIntent(t, key, "pm_card_visa", 36000)
	refunder := NewStripeRefunder(key)
	run := uuid.NewString()
	first, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, "live-kitchen-"+run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, "live-lock-up-"+run)
	if err != nil || first == second {
		t.Fatalf("refunds = %q %q, err = %v", first, second, err)
	}
}

func TestStripeRefusesRefundBeyondPaidAmount(t *testing.T) {
	key := testKey(t)
	intentID := paidIntent(t, key, "pm_card_visa", 6000)
	refunder := NewStripeRefunder(key)
	run := uuid.NewString()
	if _, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, "live-a-"+run); err != nil {
		t.Fatal(err)
	}
	if _, err := refunder.RefundPayment(context.Background(), intentID, BundleRefundCents, "live-b-"+run); err == nil {
		t.Fatal("expected Stripe to refuse refunding more than was paid")
	}
}

func TestStripeRefusesRefundOnUnpaidIntent(t *testing.T) {
	key := testKey(t)
	client := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: key}
	intent, err := client.New(&stripe.PaymentIntentParams{
		Amount:   stripe.Int64(10000),
		Currency: stripe.String("usd"),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled:        stripe.Bool(true),
			AllowRedirects: stripe.String("never"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewStripeRefunder(key).RefundPayment(context.Background(), intent.ID, BundleRefundCents, "live-unpaid-"+uuid.NewString())
	if err == nil {
		t.Fatal("expected Stripe to refuse refunding an unpaid intent")
	}
}
