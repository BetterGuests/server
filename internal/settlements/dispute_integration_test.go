package settlements

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/payments"
	"github.com/google/uuid"
)

type reversals struct{ requests []ReversalRequest }

func (stub *reversals) Reverse(_ context.Context, request ReversalRequest) (string, error) {
	stub.requests = append(stub.requests, request)
	return "trr_" + uuid.NewString(), nil
}

func (stay *finishedStay) intent() string {
	stay.t.Helper()
	var intent string
	if err := stay.admin.QueryRow(stay.ctx, `SELECT stripe_payment_intent_id FROM app.payments WHERE booking_id = $1`, stay.bookingID).Scan(&intent); err != nil {
		stay.t.Fatal(err)
	}
	return intent
}

func (stay *finishedStay) dispute(id, status string, cents int, at time.Time) {
	stay.t.Helper()
	matched, err := payments.NewPostgresPaymentStore(stay.store.DB).RecordDispute(stay.ctx,
		payments.Dispute{ID: id, IntentID: stay.intent(), Status: status, Reason: "fraudulent", AmountCents: cents}, at)
	if err != nil || !matched {
		stay.t.Fatalf("record dispute: matched = %v, err = %v", matched, err)
	}
}

func (stay *finishedStay) listed(viewer uuid.UUID, id string) (PaymentDispute, bool) {
	stay.t.Helper()
	found, err := stay.review.Disputes(stay.ctx, viewer)
	if err != nil {
		stay.t.Fatal(err)
	}
	index := slices.IndexFunc(found, func(item PaymentDispute) bool { return item.ID == id })
	if index < 0 {
		return PaymentDispute{}, false
	}
	return found[index], true
}

func (stay *finishedStay) paidOut() {
	stay.t.Helper()
	err := stay.store.Save(stay.ctx, Settlement{
		ID: uuid.New(), BookingID: stay.bookingID, GuestID: stay.guest, HostID: stay.host,
		FeeCents: 4830, PayoutCents: 55170, TransferID: "tr_" + stay.bookingID.String(), Status: StatusSettled,
	})
	if err != nil {
		stay.t.Fatal(err)
	}
}

func (stay *finishedStay) alerts(account uuid.UUID) (count int) {
	stay.t.Helper()
	if err := stay.admin.QueryRow(stay.ctx,
		`SELECT count(*) FROM app.notifications_awaiting_email(200) WHERE account_id = $1 AND booking_id = $2 AND kind = 'payment_disputed' AND for_team`,
		account, stay.bookingID).Scan(&count); err != nil {
		stay.t.Fatal(err)
	}
	return count
}

func TestADisputeAlertsTheTeamOnceAndNeverGoesBackwards(t *testing.T) {
	stay := newFinishedStay(t)
	id, opened := "dp_"+uuid.NewString(), time.Now().UTC().Add(-time.Hour)
	stay.dispute(id, "needs_response", 60000, opened)
	stay.dispute(id, "under_review", 60000, opened.Add(time.Minute))
	if got := stay.alerts(stay.team); got != 1 {
		t.Fatalf("team alerts = %d", got)
	}
	if got := stay.alerts(stay.host); got != 0 {
		t.Fatalf("host alerts = %d", got)
	}
	stay.dispute(id, "won", 60000, opened.Add(2*time.Minute))
	stay.dispute(id, "needs_response", 60000, opened.Add(2*time.Minute))
	stay.dispute(id, "under_review", 60000, opened)
	item, found := stay.listed(stay.team, id)
	if !found || item.Status != "won" || item.ListingTitle != "Held House" || item.AmountCents != 60000 || item.PaidOutCents != 0 || item.CanReverse {
		t.Fatalf("dispute = %+v, found = %v", item, found)
	}
}

func TestADisputeForAnUnknownPaymentIsNotRecorded(t *testing.T) {
	stay := newFinishedStay(t)
	matched, err := payments.NewPostgresPaymentStore(stay.store.DB).RecordDispute(stay.ctx,
		payments.Dispute{ID: "dp_" + uuid.NewString(), IntentID: "pi_unknown", Status: "needs_response", AmountCents: 100}, time.Now())
	if err != nil || matched {
		t.Fatalf("matched = %v, err = %v", matched, err)
	}
}

func TestTheTeamTakesADisputedPayoutBackOnce(t *testing.T) {
	stay := newFinishedStay(t)
	stay.paidOut()
	first, second := "dp_"+uuid.NewString(), "dp_"+uuid.NewString()
	stay.dispute(first, "needs_response", 40000, time.Now().UTC())
	stay.dispute(second, "needs_response", 40000, time.Now().UTC())
	item, _ := stay.listed(stay.team, first)
	if !item.CanReverse || item.ReversibleCents != 40000 || item.PaidOutCents != 55170 {
		t.Fatalf("dispute = %+v", item)
	}
	if err := stay.review.Reverse(stay.ctx, stay.team, first); err != nil {
		t.Fatal(err)
	}
	if err := stay.review.Reverse(stay.ctx, stay.team, first); !errors.Is(err, ErrCannotReverse) {
		t.Fatalf("reversed twice: %v", err)
	}
	if err := stay.review.Reverse(stay.ctx, stay.team, second); err != nil {
		t.Fatal(err)
	}
	sent := stay.reverser.requests
	if len(sent) != 2 || sent[0].AmountCents != 40000 || sent[1].AmountCents != 15170 || sent[0].TransferID != "tr_"+stay.bookingID.String() || sent[0].DisputeID != first {
		t.Fatalf("reversals = %+v", sent)
	}
	if item, _ = stay.listed(stay.team, first); item.ReversedCents != 40000 || item.CanReverse {
		t.Fatalf("after reversal = %+v", item)
	}
}

func TestAPayoutIsNotReversedWithoutCause(t *testing.T) {
	for name, scenario := range map[string]struct {
		status string
		paid   bool
	}{"won": {"won", true}, "inquiry closed": {"warning_closed", true}, "not paid out": {"needs_response", false}} {
		t.Run(name, func(t *testing.T) {
			stay := newFinishedStay(t)
			if scenario.paid {
				stay.paidOut()
			}
			id := "dp_" + uuid.NewString()
			stay.dispute(id, scenario.status, 60000, time.Now().UTC())
			if err := stay.review.Reverse(stay.ctx, stay.team, id); !errors.Is(err, ErrCannotReverse) {
				t.Fatalf("reverse: %v", err)
			}
			if len(stay.reverser.requests) != 0 {
				t.Fatalf("money moved: %+v", stay.reverser.requests)
			}
		})
	}
}

func TestOnlyAnUninvolvedAdminCanReverse(t *testing.T) {
	stay := newFinishedStay(t)
	stay.paidOut()
	id := "dp_" + uuid.NewString()
	stay.dispute(id, "lost", 60000, time.Now().UTC())
	for name, viewer := range map[string]uuid.UUID{"host": stay.host, "guest": stay.guest} {
		if _, err := stay.review.Disputes(stay.ctx, viewer); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("%s listed disputes: %v", name, err)
		}
		if err := stay.review.Reverse(stay.ctx, viewer, id); !errors.Is(err, ErrNotAdmin) {
			t.Fatalf("%s reversed: %v", name, err)
		}
		if recorded, err := stay.store.RecordReversal(stay.ctx, viewer, id, "trr_x", 100); err != nil || recorded {
			t.Fatalf("the database let the %s record a reversal: %v %v", name, recorded, err)
		}
	}
	stay.sql(`INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, stay.host, stay.host.String()+"@example.test")
	if err := stay.review.Reverse(stay.ctx, stay.host, id); !errors.Is(err, ErrCannotReverse) {
		t.Fatalf("an admin reversed their own payout: %v", err)
	}
	if len(stay.reverser.requests) != 0 {
		t.Fatalf("money moved: %+v", stay.reverser.requests)
	}
}
