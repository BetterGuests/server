package settlements

import "testing"

func TestTheHostIsPaidOnlyOnWhatTheGuestDidNotGetBack(t *testing.T) {
	cases := map[string]struct{ adjustment, payout int }{
		"half refunded on cancellation": {30000, 27570},
		"team refunded part of a stay":  {10000, 45970},
		"everything refunded":           {60000, 0},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(60000, nil)
			h.store.due[0].AdjustmentCents = test.adjustment
			h.gateway.charge.RefundedCents = test.adjustment
			if report := h.run(t); report.Settled != 1 {
				t.Fatalf("report = %+v", report)
			}
			paid := 0
			for _, request := range h.gateway.transfers {
				paid += request.AmountCents
			}
			if paid != test.payout || len(h.gateway.refunds) != 0 {
				t.Fatalf("paid %d with %d refunds, want %d and none", paid, len(h.gateway.refunds), test.payout)
			}
		})
	}
}

func TestARefundNobodyRecordedStillBlocksThePayout(t *testing.T) {
	h := newHarness(60000, nil)
	h.store.due[0].AdjustmentCents = 10000
	h.gateway.charge.RefundedCents = 10001
	if report := h.run(t); report.Blocked != 1 {
		t.Fatalf("report = %+v", report)
	}
}
