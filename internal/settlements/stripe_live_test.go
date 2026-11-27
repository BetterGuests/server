package settlements

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
)

func sandboxGateway(t *testing.T) *StripeGateway {
	t.Helper()
	key := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(key, "sk_test_") && !strings.HasPrefix(key, "rk_test_") {
		t.Skip("STRIPE_SECRET_KEY must be a test mode key to run live Stripe sandbox checks")
	}
	return NewStripeGateway(key)
}

func TestSandboxCreatesAHostAccountAndOnboardingLink(t *testing.T) {
	gateway := sandboxGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	accountID, err := gateway.CreateAccount(ctx, uuid.New(), "sandbox-host@example.com")
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	t.Logf("account %s", accountID)

	link, err := gateway.OnboardingLink(ctx, accountID, "http://localhost:5173/hosting/payouts", "http://localhost:5173/hosting/payouts?refresh=1")
	if err != nil || !strings.HasPrefix(link, "https://") {
		t.Fatalf("OnboardingLink() = %q, error = %v", link, err)
	}

	ready, err := gateway.TransfersActive(ctx, accountID)
	if err != nil || ready {
		t.Fatalf("a new account must not be ready: ready = %v, error = %v", ready, err)
	}
}

func paidIntent(t *testing.T, gateway *StripeGateway, amount int64) string {
	t.Helper()
	client := paymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: gateway.secretKey}
	intent, err := client.New(&stripe.PaymentIntentParams{
		Amount:        stripe.Int64(amount),
		Currency:      stripe.String("usd"),
		PaymentMethod: stripe.String("pm_card_visa"),
		Confirm:       stripe.Bool(true),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled:        stripe.Bool(true),
			AllowRedirects: stripe.String("never"),
		},
	})
	if err != nil || intent.Status != stripe.PaymentIntentStatusSucceeded {
		t.Fatalf("create paid intent: %v", err)
	}
	return intent.ID
}

func TestSandboxRefundsCashbackOnceAndReadsItBack(t *testing.T) {
	gateway := sandboxGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	intentID := paidIntent(t, gateway, 60000)
	bookingID := uuid.New()

	before, err := gateway.Charge(ctx, intentID, bookingID)
	if err != nil || before.ID == "" || before.CapturedCents != 60000 || before.RefundedCents != 0 || before.Disputed || before.SettlementRefundID != "" {
		t.Fatalf("before = %+v, error = %v", before, err)
	}

	request := RefundRequest{BookingID: bookingID, PaymentIntentID: intentID, AmountCents: 15000}
	first, err := gateway.Refund(ctx, request)
	if err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	second, err := gateway.Refund(ctx, request)
	if err != nil || second != first {
		t.Fatalf("second refund = %q, error = %v, want %q", second, err, first)
	}

	after, err := gateway.Charge(ctx, intentID, bookingID)
	if err != nil || after.RefundedCents != 15000 || after.SettlementRefundID != first || after.SettlementRefundCents != 15000 || after.SettlementRefundDead {
		t.Fatalf("after = %+v, error = %v", after, err)
	}

	if _, found, err := gateway.FindTransfer(ctx, bookingID); err != nil || found {
		t.Fatalf("FindTransfer() found = %v, error = %v", found, err)
	}
}

func TestSandboxNoticesARefundMadeOutsideSettlement(t *testing.T) {
	gateway := sandboxGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	intentID := paidIntent(t, gateway, 20000)
	if _, err := gateway.Refund(ctx, RefundRequest{BookingID: uuid.New(), PaymentIntentID: intentID, AmountCents: 1000}); err != nil {
		t.Fatalf("Refund() error = %v", err)
	}
	charge, err := gateway.Charge(ctx, intentID, uuid.New())
	if err != nil || charge.RefundedCents != 1000 || charge.SettlementRefundID != "" {
		t.Fatalf("charge = %+v, error = %v", charge, err)
	}
}

func TestSandboxSettlesAStayAndPaysTheHostWhenReady(t *testing.T) {
	gateway := sandboxGateway(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	hostAccount := os.Getenv("STRIPE_TEST_HOST_ACCOUNT")
	booking := Due{
		BookingID: uuid.New(), GuestID: uuid.New(), HostID: uuid.New(),
		PaymentIntentID: paidIntent(t, gateway, 60000), AmountCents: 60000, Currency: "usd",
		AcceptedTaskKeys: everyTask, HostAccountID: hostAccount,
	}
	store := &memoryStore{saved: map[uuid.UUID]Settlement{}, due: []Due{booking}}
	service := NewSettlementService(store, gateway, Config{BatchSize: 1})

	want := StatusAwaitingHost
	if hostAccount != "" {
		want = StatusSettled
	}
	for run := 1; run <= 2; run++ {
		if _, err := service.SettleDue(ctx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		saved := store.saved[booking.BookingID]
		if saved.Status != want || saved.RefundID == "" || saved.PayoutCents != 40170 {
			t.Fatalf("run %d: saved = %+v", run, saved)
		}
		if want == StatusSettled && saved.TransferID == "" {
			t.Fatalf("run %d: no transfer recorded", run)
		}
	}
	charge, err := gateway.Charge(ctx, booking.PaymentIntentID, booking.BookingID)
	if err != nil || charge.RefundedCents != 15000 {
		t.Fatalf("charge = %+v, error = %v", charge, err)
	}
	t.Logf("booking %s ended %s", booking.BookingID, want)
}
