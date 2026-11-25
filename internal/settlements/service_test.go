package settlements

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

var everyTask = []string{"run_dishwasher", "empty_fridge", "wipe_counters", "strip_beds", "towels_in_hamper", "take_out_trash", "lights_thermostat"}

type memoryStore struct {
	due      []Due
	saved    map[uuid.UUID]Settlement
	history  []Settlement
	failWhen func(Settlement) bool
	earliest time.Time
	latest   time.Time
}

func (store *memoryStore) ListDue(_ context.Context, earliest, latest time.Time, _ int) ([]Due, error) {
	store.earliest, store.latest = earliest, latest
	var open []Due
	for _, booking := range store.due {
		saved, found := store.saved[booking.BookingID]
		if found && (saved.Status == StatusSettled || saved.Status == StatusBlocked) {
			continue
		}
		if found {
			booking.Saved = &saved
		}
		open = append(open, booking)
	}
	return open, nil
}

func (store *memoryStore) Save(_ context.Context, settlement Settlement) error {
	if store.failWhen != nil && store.failWhen(settlement) {
		return errors.New("database is down")
	}
	store.saved[settlement.BookingID] = settlement
	store.history = append(store.history, settlement)
	return nil
}

type fakeGateway struct {
	charge      Charge
	chargeErr   error
	refunds     []RefundRequest
	transfers   []TransferRequest
	refundErr   error
	transferErr error
	hostReady   bool
}

func (gateway *fakeGateway) Charge(context.Context, string, uuid.UUID) (Charge, error) {
	return gateway.charge, gateway.chargeErr
}

func (gateway *fakeGateway) Refund(_ context.Context, request RefundRequest) (string, error) {
	if gateway.refundErr != nil {
		return "", gateway.refundErr
	}
	gateway.refunds = append(gateway.refunds, request)
	gateway.charge.RefundedCents += request.AmountCents
	gateway.charge.SettlementRefundCents = request.AmountCents
	gateway.charge.SettlementRefundID = "re_1"
	return "re_1", nil
}

func (gateway *fakeGateway) FindTransfer(context.Context, uuid.UUID) (string, bool, error) {
	if len(gateway.transfers) > 0 {
		return "tr_1", true, nil
	}
	return "", false, nil
}

func (gateway *fakeGateway) Transfer(_ context.Context, request TransferRequest) (string, error) {
	if gateway.transferErr != nil {
		return "", gateway.transferErr
	}
	gateway.transfers = append(gateway.transfers, request)
	return "tr_1", nil
}

func (gateway *fakeGateway) TransfersActive(context.Context, string) (bool, error) {
	return gateway.hostReady, nil
}

type harness struct {
	service *SettlementService
	store   *memoryStore
	gateway *fakeGateway
	booking uuid.UUID
}

func newHarness(amount int, accepted []string) harness {
	booking := uuid.New()
	store := &memoryStore{saved: map[uuid.UUID]Settlement{}, due: []Due{{
		BookingID: booking, GuestID: uuid.New(), HostID: uuid.New(), PaymentIntentID: "pi_1",
		AmountCents: amount, Currency: "usd", AcceptedTaskKeys: accepted, HostAccountID: "acct_1",
	}}}
	gateway := &fakeGateway{charge: Charge{ID: "ch_1", CapturedCents: amount}, hostReady: true}
	service := NewSettlementService(store, gateway, Config{FeePercent: 12, BatchSize: 10})
	service.now = func() time.Time { return time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC) }
	return harness{service: service, store: store, gateway: gateway, booking: booking}
}

func (h harness) run(t *testing.T) Report {
	t.Helper()
	report, err := h.service.SettleDue(context.Background())
	if err != nil {
		t.Fatalf("SettleDue() error = %v", err)
	}
	return report
}

func TestSettlementRefundsCashbackAndPaysTheHostTheRest(t *testing.T) {
	cases := map[string]struct {
		amount, refund, payout int
		accepted               []string
	}{
		"no chores":               {60000, 0, 52800, nil},
		"one bundle":              {60000, 5000, 47800, everyTask[:3]},
		"every bundle":            {60000, 15000, 37800, everyTask},
		"cheap stay hits the cap": {10000, 2500, 6300, everyTask},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(test.amount, test.accepted)
			if report := h.run(t); report.Settled != 1 {
				t.Fatalf("report = %+v", report)
			}
			refunded := 0
			for _, request := range h.gateway.refunds {
				refunded += request.AmountCents
			}
			if refunded != test.refund || len(h.gateway.refunds) > 1 {
				t.Fatalf("refunds = %+v", h.gateway.refunds)
			}
			if len(h.gateway.transfers) != 1 || h.gateway.transfers[0].AmountCents != test.payout || h.gateway.transfers[0].ChargeID != "ch_1" || h.gateway.transfers[0].Destination != "acct_1" {
				t.Fatalf("transfers = %+v", h.gateway.transfers)
			}
		})
	}
}

func TestRunningAgainMovesNoMoreMoney(t *testing.T) {
	h := newHarness(60000, everyTask)
	h.run(t)
	h.run(t)
	if len(h.gateway.refunds) != 1 || len(h.gateway.transfers) != 1 {
		t.Fatalf("refunds = %d, transfers = %d", len(h.gateway.refunds), len(h.gateway.transfers))
	}
}

func TestAFailedRefundHoldsThePayoutAndIsRetried(t *testing.T) {
	h := newHarness(60000, everyTask)
	h.gateway.refundErr = errors.New("stripe is down")
	if report := h.run(t); report.Failed != 1 || len(h.gateway.transfers) != 0 {
		t.Fatalf("report = %+v, transfers = %d", report, len(h.gateway.transfers))
	}
	h.gateway.refundErr = nil
	if report := h.run(t); report.Settled != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestAFailedPayoutIsRetriedWithoutASecondRefund(t *testing.T) {
	h := newHarness(60000, everyTask)
	h.gateway.transferErr = errors.New("insufficient balance")
	h.run(t)
	h.gateway.transferErr = nil
	if report := h.run(t); report.Settled != 1 || len(h.gateway.refunds) != 1 || len(h.gateway.transfers) != 1 {
		t.Fatalf("report = %+v, refunds = %d", report, len(h.gateway.refunds))
	}
}

func TestACrashAfterTheRefundDoesNotRefundTwice(t *testing.T) {
	h := newHarness(60000, everyTask)
	h.store.failWhen = func(settlement Settlement) bool { return settlement.RefundID != "" }
	h.run(t)
	h.store.failWhen = nil
	if report := h.run(t); report.Settled != 1 || len(h.gateway.refunds) != 1 {
		t.Fatalf("report = %+v, refunds = %d", report, len(h.gateway.refunds))
	}
}

func TestACrashAfterTheTransferDoesNotPayTwice(t *testing.T) {
	h := newHarness(60000, nil)
	h.store.failWhen = func(settlement Settlement) bool { return settlement.Status == StatusSettled }
	h.run(t)
	h.store.failWhen = nil
	if report := h.run(t); report.Settled != 1 || len(h.gateway.transfers) != 1 {
		t.Fatalf("report = %+v, transfers = %d", report, len(h.gateway.transfers))
	}
}

func TestNoMoneyMovesIfTheSettlementCannotBeRecordedFirst(t *testing.T) {
	h := newHarness(60000, everyTask)
	h.store.failWhen = func(Settlement) bool { return true }
	h.run(t)
	if len(h.gateway.refunds) != 0 || len(h.gateway.transfers) != 0 {
		t.Fatalf("refunds = %d, transfers = %d", len(h.gateway.refunds), len(h.gateway.transfers))
	}
}

func TestGuestIsRefundedWhileThePayoutWaitsForTheHostAccount(t *testing.T) {
	for name, prepare := range map[string]func(harness){
		"no account":         func(h harness) { h.store.due[0].HostAccountID = "" },
		"onboarding pending": func(h harness) { h.gateway.hostReady = false },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(60000, everyTask)
			prepare(h)
			if report := h.run(t); report.Waiting != 1 || len(h.gateway.refunds) != 1 || len(h.gateway.transfers) != 0 {
				t.Fatalf("report = %+v", report)
			}
			h.store.due[0].HostAccountID = "acct_1"
			h.gateway.hostReady = true
			h.service.config.FeePercent = 30
			if report := h.run(t); report.Settled != 1 || len(h.gateway.refunds) != 1 {
				t.Fatalf("report = %+v", report)
			}
			if got := h.gateway.transfers[0].AmountCents; got != 37800 {
				t.Fatalf("payout = %d, the fee must stay as first recorded", got)
			}
		})
	}
}

func TestUnsafeChargesAreBlockedForAPerson(t *testing.T) {
	cases := map[string]struct {
		change func(*Charge)
		reason string
	}{
		"disputed":                  {func(c *Charge) { c.Disputed = true }, reasonDisputed},
		"partly captured":           {func(c *Charge) { c.CapturedCents = 100 }, reasonWrongCapture},
		"refunded by hand":          {func(c *Charge) { c.RefundedCents = 1000 }, reasonOutsideRefund},
		"cashback refund failed":    {func(c *Charge) { c.SettlementRefundDead = true }, reasonRefundFailed},
		"earlier refund wrong size": {func(c *Charge) { c.SettlementRefundID, c.SettlementRefundCents, c.RefundedCents = "re_9", 5000, 5000 }, reasonRefundMismatch},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(60000, everyTask)
			test.change(&h.gateway.charge)
			report := h.run(t)
			if report.Blocked != 1 || len(h.gateway.refunds) != 0 || len(h.gateway.transfers) != 0 {
				t.Fatalf("report = %+v", report)
			}
			if got := h.store.saved[h.booking].Reason; got != test.reason {
				t.Fatalf("reason = %q, want %q", got, test.reason)
			}
		})
	}
}

func TestCashbackAlreadyRefundedUnderTheOldFlowIsNotRefundedAgain(t *testing.T) {
	h := newHarness(60000, everyTask[:3])
	h.store.due[0].EarlierRefundCents = 5000
	h.gateway.charge.RefundedCents = 5000
	if report := h.run(t); report.Settled != 1 || len(h.gateway.refunds) != 0 {
		t.Fatalf("report = %+v, refunds = %+v", report, h.gateway.refunds)
	}
	if got := h.gateway.transfers[0].AmountCents; got != 47800 {
		t.Fatalf("payout = %d", got)
	}
}

func TestOneBrokenBookingDoesNotStopTheOthers(t *testing.T) {
	h := newHarness(60000, nil)
	broken := h.store.due[0]
	broken.BookingID = uuid.New()
	h.store.due = append([]Due{broken}, h.store.due...)
	h.store.failWhen = func(settlement Settlement) bool { return settlement.BookingID == broken.BookingID }
	if report := h.run(t); report.Failed != 1 || report.Settled != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestConfigFromLookup(t *testing.T) {
	cases := map[string]struct {
		values map[string]string
		fails  bool
	}{
		"defaults are off":           {map[string]string{}, false},
		"enabled needs a start date": {map[string]string{"SETTLEMENT_ENABLED": "true"}, true},
		"enabled with a start date":  {map[string]string{"SETTLEMENT_ENABLED": "true", "SETTLEMENT_STARTS_ON": "2026-12-01"}, false},
		"fee too high":               {map[string]string{"PLATFORM_FEE_PERCENT": "51"}, true},
		"negative fee":               {map[string]string{"PLATFORM_FEE_PERCENT": "-1"}, true},
		"interval too short":         {map[string]string{"SETTLEMENT_INTERVAL": "1s"}, true},
		"plain http web address":     {map[string]string{"PAYOUTS_WEB_URL": "http://example.com"}, true},
		"localhost web address":      {map[string]string{"PAYOUTS_WEB_URL": "http://localhost:5173"}, false},
		"bad enabled value":          {map[string]string{"SETTLEMENT_ENABLED": "yes please"}, true},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			config, err := configFromLookup(func(key string) string { return test.values[key] })
			if (err != nil) != test.fails {
				t.Fatalf("config = %+v, err = %v", config, err)
			}
			if name == "defaults are off" && (config.Enabled || config.FeePercent != 12) {
				t.Fatalf("config = %+v", config)
			}
		})
	}
}
