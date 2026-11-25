package settlements

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestHostAccountIsATransfersOnlyExpressRecipient(t *testing.T) {
	encoded, err := json.Marshal(recipientAccountBody(uuid.New(), "host@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"business_url":"https://betterguests.com"`, `"product_description":`, `"contact_email":"host@example.com"`, `"dashboard":"express"`, `"stripe_transfers":{"requested":true}`, `"fees_collector":"application"`, `"losses_collector":"application"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
	if strings.Contains(string(encoded), "card_payments") {
		t.Fatalf("a recipient must not request card payments: %s", encoded)
	}
}

func TestTransferIsTiedToTheBookingsCharge(t *testing.T) {
	booking := uuid.New()
	params := transferParams(TransferRequest{BookingID: booking, ChargeID: "ch_1", Destination: "acct_1", AmountCents: 4200, Currency: "usd"})
	if *params.SourceTransaction != "ch_1" || *params.TransferGroup != "booking_"+booking.String() || *params.Amount != 4200 {
		t.Fatalf("params = %+v", params)
	}
	if *params.IdempotencyKey != "settlement-transfer-"+booking.String() || params.Metadata[MetadataBookingID] != booking.String() {
		t.Fatalf("params = %+v", params)
	}
}
