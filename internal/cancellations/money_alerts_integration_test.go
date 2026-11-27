package cancellations_test

import (
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/google/uuid"
)

type moneyAlert struct {
	kind  notifications.Kind
	title string
}

func queuedMoney(t *testing.T, w *world, store *notifications.PostgresNotificationStore) map[uuid.UUID][]moneyAlert {
	t.Helper()
	alerts, err := store.QueueTeamAlerts(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	byRecipient := map[uuid.UUID][]moneyAlert{}
	for _, alert := range alerts {
		byRecipient[alert.Recipient] = append(byRecipient[alert.Recipient], moneyAlert{alert.Kind, alert.Title})
	}
	return byRecipient
}

func newWorldAwayFromThePhoenixRankings(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.sql(`UPDATE app.listings SET city = 'Flagstaff' WHERE id = $1`, w.listingID)
	return w
}

func (w *world) settle(booking, guest uuid.UUID, status string, refundDue, payout int) {
	w.t.Helper()
	w.sql(`INSERT INTO app.booking_settlements
	         (id, booking_id, guest_id, host_id, fee_cents, cashback_cents, refund_due_cents, payout_cents,
	          stripe_refund_id, stripe_transfer_id, status)
	       VALUES ($1, $2, $3, $4, 1000, $5, $5, $6,
	               CASE WHEN $7 = 'settled' AND $5 > 0 THEN 're_' || gen_random_uuid() END,
	               CASE WHEN $7 = 'settled' AND $6 > 0 THEN 'tr_' || gen_random_uuid() END,
	               $7)`,
		uuid.New(), booking, guest, w.host, refundDue, payout, status)
}

func TestASettledStayTellsTheHostAboutThePayoutAndTheGuestAboutCashback(t *testing.T) {
	w := newWorldAwayFromThePhoenixRankings(t)
	store := notifications.NewPostgresNotificationStore(w.pool)
	booking, guest := w.book(10, "requested")
	w.settle(booking, guest, "settled", 2500, 30000)

	queued := queuedMoney(t, w, store)
	if got := queued[w.host]; len(got) != 1 || got[0] != (moneyAlert{"payout_sent", "$300.00 is on its way"}) {
		t.Fatalf("host alerts = %+v", got)
	}
	if got := queued[guest]; len(got) != 1 || got[0] != (moneyAlert{"cashback_paid", "You got $25.00 back"}) {
		t.Fatalf("guest alerts = %+v", got)
	}
	again := queuedMoney(t, w, store)
	if len(again[w.host])+len(again[guest]) != 0 {
		t.Fatalf("money alerts were sent twice: %+v", again)
	}
}

func TestPayoutsStuckOnTheHostOrOnHoldTellTheHost(t *testing.T) {
	w := newWorldAwayFromThePhoenixRankings(t)
	store := notifications.NewPostgresNotificationStore(w.pool)
	waiting, waitingGuest := w.book(10, "requested")
	w.settle(waiting, waitingGuest, "awaiting_host", 0, 12000)
	held, heldGuest := w.book(20, "requested")
	w.settle(held, heldGuest, "blocked", 0, 12000)

	kinds := map[notifications.Kind]bool{}
	for _, alert := range queuedMoney(t, w, store)[w.host] {
		kinds[alert.kind] = true
	}
	if !kinds["payout_setup_needed"] || !kinds["payout_on_hold"] {
		t.Fatalf("host alert kinds = %v", kinds)
	}
}

func TestAnOldSettlementDoesNotAlertWhenTheQueueFirstRuns(t *testing.T) {
	w := newWorldAwayFromThePhoenixRankings(t)
	store := notifications.NewPostgresNotificationStore(w.pool)
	booking, guest := w.book(10, "requested")
	w.settle(booking, guest, "settled", 2500, 30000)
	w.sql(`UPDATE app.booking_settlements SET updated_at = now() - interval '10 days' WHERE booking_id = $1`, booking)

	queued := queuedMoney(t, w, store)
	if len(queued[w.host])+len(queued[guest]) != 0 {
		t.Fatalf("an old payout alerted: %+v", queued)
	}
}

func TestCashbackDuringTheStayAndPhotoRetakesTellTheGuest(t *testing.T) {
	w := newWorldAwayFromThePhoenixRankings(t)
	store := notifications.NewPostgresNotificationStore(w.pool)
	booking, guest := w.book(10, "requested")
	w.sql(`INSERT INTO app.booking_bundle_refunds (id, booking_id, guest_id, bundle_key, amount_cents, status, created_at, updated_at)
	       VALUES ($1, $2, $3, 'kitchen', 5000, 'succeeded', now(), now())`, uuid.New(), booking, guest)
	w.sql(`INSERT INTO app.booking_task_submissions (id, booking_id, guest_id, task_key, photo_url, status, created_at, updated_at)
	       VALUES ($1, $2, $3, 'strip_beds', 'https://example.test/photo.jpg', 'retake', now(), now())`, uuid.New(), booking, guest)

	got := map[notifications.Kind]string{}
	for _, alert := range queuedMoney(t, w, store)[guest] {
		got[alert.kind] = alert.title
	}
	if got["cashback_earned"] != "You earned $50.00 back" || got["photo_retake"] != "Retake a checkout photo" {
		t.Fatalf("guest alerts = %+v", got)
	}
}

func TestMoneyAlertsStayInTheAppAndAreNotEmailed(t *testing.T) {
	w := newWorldAwayFromThePhoenixRankings(t)
	store := notifications.NewPostgresNotificationStore(w.pool)
	booking, guest := w.book(10, "requested")
	w.settle(booking, guest, "settled", 2500, 30000)
	queuedMoney(t, w, store)

	waiting, err := store.AwaitingEmail(w.ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range waiting {
		if item.BookingID == booking {
			t.Fatalf("a money alert was queued for email: %+v", item)
		}
	}
}
