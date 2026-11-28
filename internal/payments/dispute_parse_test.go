package payments

import (
	"testing"

	"github.com/stripe/stripe-go/v82/webhook"
)

func TestParseEventReadsADisputedCharge(t *testing.T) {
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Secret:  "whsec_test",
		Payload: []byte(`{"id":"evt_1","object":"event","type":"charge.dispute.created","created":1700000000,"data":{"object":{"id":"dp_1","object":"dispute","payment_intent":"pi_1","status":"needs_response","reason":"fraudulent","amount":4000}}}`),
	})
	event, err := NewStripeGateway(Config{WebhookSecret: "whsec_test"}).ParseEvent(signed.Payload, signed.Header)
	if err != nil {
		t.Fatal(err)
	}
	want := Dispute{ID: "dp_1", IntentID: "pi_1", Status: "needs_response", Reason: "fraudulent", AmountCents: 4000}
	if event.Dispute == nil || *event.Dispute != want {
		t.Fatalf("dispute = %+v", event.Dispute)
	}
}
