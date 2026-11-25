package cancellations

import (
	"testing"

	"github.com/google/uuid"
)

func TestCancellationRefundsAreNotMistakenForSettlementRefunds(t *testing.T) {
	booking := uuid.New()
	params := refundParams(PendingRefund{BookingID: booking, Source: SourceCancellation, PaymentIntentID: "pi_1", AmountCents: 500})
	if _, settlement := params.Metadata["settlement_booking_id"]; settlement {
		t.Fatal("uses the settlement metadata key")
	}
	if params.Metadata["cancellation_booking_id"] != booking.String() || *params.Amount != 500 {
		t.Fatalf("params = %+v", params.Metadata)
	}
}
