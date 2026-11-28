package stays

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOnlyABookedGuestAndTheHostSeeTheStreetAddress(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		asHost  bool
		visible bool
	}{
		{name: "a request not yet answered", status: statusRequested},
		{name: "approved but unpaid", status: statusPending},
		{name: "confirmed weeks before arrival", status: statusConfirmed, visible: true},
		{name: "after the stay", status: statusCompleted, visible: true},
		{name: "cancelled", status: "cancelled"},
		{name: "declined", status: "declined"},
		{name: "the host at any time", status: statusRequested, asHost: true, visible: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := phoenixStay()
			store.record.Status = test.status
			store.record.Address = Address{Line1: "100 Ocean Ave", City: "Santa Monica"}
			actor := store.record.GuestID
			if test.asHost {
				actor = store.record.HostID
			}

			view, err := serviceAt(store, store.record.StartsOn.Add(-30*24*time.Hour)).Stay(context.Background(), actor, uuid.New())

			if err != nil {
				t.Fatal(err)
			}
			if (view.Address != nil) != test.visible {
				t.Fatalf("address = %+v", view.Address)
			}
		})
	}
}
