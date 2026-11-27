package settlements

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v82"
)

func TestHostAccountIsATransfersOnlyExpressRecipient(t *testing.T) {
	encoded, err := json.Marshal(recipientAccountBody(uuid.New(), "host@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"business_url":"https://better-guests.com"`, `"product_description":`, `"contact_email":"host@example.com"`, `"dashboard":"express"`, `"stripe_transfers":{"requested":true}`, `"fees_collector":"application"`, `"losses_collector":"application"`} {
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

func TestOnlyAClosedInOurFavourDisputeReleasesTheCharge(t *testing.T) {
	cases := map[string]struct {
		statuses []stripe.DisputeStatus
		stands   bool
	}{
		"won":                     {[]stripe.DisputeStatus{stripe.DisputeStatusWon}, false},
		"inquiry closed":          {[]stripe.DisputeStatus{stripe.DisputeStatusWarningClosed}, false},
		"lost":                    {[]stripe.DisputeStatus{stripe.DisputeStatusLost}, true},
		"needs a response":        {[]stripe.DisputeStatus{stripe.DisputeStatusNeedsResponse}, true},
		"under review":            {[]stripe.DisputeStatus{stripe.DisputeStatusUnderReview}, true},
		"one won and one open":    {[]stripe.DisputeStatus{stripe.DisputeStatusWon, stripe.DisputeStatusNeedsResponse}, true},
		"flagged but none listed": {nil, true},
	}
	for name, test := range cases {
		if got := disputeStands(test.statuses); got != test.stands {
			t.Errorf("%s: stands = %v", name, got)
		}
	}
}
