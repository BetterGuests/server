package automessages_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/automessages"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type silentPublisher struct{}

func (silentPublisher) Publish(uuid.UUID, string, any) {}

type world struct {
	t                         *testing.T
	ctx                       context.Context
	pool                      *pgxpool.Pool
	host, otherHost, stranger uuid.UUID
	listingID                 uuid.UUID
	automatic                 *automessages.AutoMessageService
	messages                  *messages.MessageService
	journey                   *notifications.JourneyService
	stays                     *stays.StayService
}

func newWorld(t *testing.T) *world {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := automessages.NewPostgresAutoMessageStore(pool)
	automatic := automessages.NewAutoMessageService(store, store, store)
	stayStore := stays.NewPostgresStayStore(pool)
	w := &world{
		t: t, ctx: ctx, pool: pool,
		host: uuid.New(), otherHost: uuid.New(), stranger: uuid.New(),
		automatic: automatic,
		messages:  messages.NewMessageService(messages.NewPostgresMessageStore(pool)).WithPrivateBodies(automatic),
		journey:   notifications.NewJourneyService(notifications.NewPostgresNotificationStore(pool), silentPublisher{}).WithAutomaticMessages(automatic),
		stays:     stays.NewStayService(stayStore, stayStore),
	}
	dbtest.SignUp(t, ctx, pool, w.host, w.otherHost, w.stranger)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, w.host, listings.CreateListingInput{
		Title: "Message House", Description: "A complete listing used by the automatic message test.",
		Address: listings.Address{Line1: "1 Auto Way " + uuid.NewString(), City: "Tucson", CountryCode: "US", Latitude: 32.2, Longitude: -110.9},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.listingID = listing.ID
	if _, err := w.as(w.host, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) as(userID uuid.UUID, statement string, args ...any) (int64, error) {
	var affected int64
	err := db.WithUserTx(w.ctx, w.pool, userID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(w.ctx, statement, args...)
		affected = tag.RowsAffected()
		return err
	})
	return affected, err
}

func (w *world) count(userID uuid.UUID, query string, args ...any) int {
	w.t.Helper()
	var found int
	if err := db.WithUserTx(w.ctx, w.pool, userID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, query, args...).Scan(&found)
	}); err != nil {
		w.t.Fatal(err)
	}
	return found
}

func (w *world) administer(statement string, args ...any) {
	w.t.Helper()
	admin, err := pgx.Connect(w.ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = admin.Close(w.ctx) }()
	if _, err := admin.Exec(w.ctx, statement, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) request(startsInDays int) (uuid.UUID, uuid.UUID) {
	w.t.Helper()
	guest, bookingID := uuid.New(), uuid.New()
	dbtest.SignUp(w.t, w.ctx, w.pool, guest)
	if _, err := w.as(guest, `INSERT INTO app.account_names (account_id, display_name) VALUES ($1, 'Maria S.')`, guest); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.as(guest,
		`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on)
		 VALUES ($1, $2, $3, 'requested', CURRENT_DATE + $4::int, CURRENT_DATE + $4::int + 2)`,
		bookingID, w.listingID, guest, startsInDays); err != nil {
		w.t.Fatal(err)
	}
	return guest, bookingID
}

func (w *world) confirm(bookingID uuid.UUID) {
	w.t.Helper()
	w.administer(`UPDATE app.bookings SET status = 'confirmed' WHERE id = $1`, bookingID)
}

func (w *world) pay(bookingID uuid.UUID, status string) {
	w.t.Helper()
	w.administer(
		`INSERT INTO app.payments (id, booking_id, guest_id, host_id, stripe_payment_intent_id, amount_cents, currency, status, last_event_at)
		 SELECT gen_random_uuid(), b.id, b.guest_id, l.host_id, 'pi_' || b.id, 10000, 'usd', $2, $3
		 FROM app.bookings b JOIN app.listings l ON l.id = b.listing_id WHERE b.id = $1`,
		bookingID, status, time.Now().UTC())
}

func (w *world) create(input automessages.Input) automessages.AutoMessage {
	w.t.Helper()
	message, err := w.automatic.Create(w.ctx, w.host, w.listingID, input)
	if err != nil {
		w.t.Fatal(err)
	}
	return message
}

func (w *world) saveGuide(code string) {
	w.t.Helper()
	_, err := w.stays.SaveGuide(w.ctx, w.host, w.listingID, stays.GuideInput{
		Schedule: stays.Schedule{CheckInTime: stays.DefaultCheckInTime, CheckOutTime: stays.DefaultCheckOutTime, TimeZone: stays.DefaultTimeZone},
		ArrivalDetails: stays.ArrivalDetails{
			EntryInstructions: "Keys are in the blue box", EntryCode: code,
		},
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) send() int {
	w.t.Helper()
	sent, err := w.automatic.SendDue(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return sent
}

func (w *world) thread(viewer uuid.UUID) []messages.Message {
	w.t.Helper()
	threads, err := w.messages.List(w.ctx, viewer)
	if err != nil {
		w.t.Fatal(err)
	}
	if len(threads) == 0 {
		return nil
	}
	page, err := w.messages.Messages(w.ctx, viewer, threads[0].ID, nil, 50)
	if err != nil {
		w.t.Fatal(err)
	}
	return page.Messages
}

func welcome() automessages.Input {
	return automessages.Input{Name: "Welcome", Trigger: automessages.TriggerBookingConfirmed, Body: "Hi {guest_name}, welcome.", Enabled: true}
}

func steps() automessages.Input {
	return automessages.Input{
		Name: "How to get in", Trigger: automessages.TriggerBeforeCheckIn, Enabled: true,
		Body: "{entry_instructions}\nCode: {entry_code}\nWi-Fi: {wifi_name}",
	}
}

func TestOnlyTheListingHostCanManageItsMessages(t *testing.T) {
	w := newWorld(t)
	mine := w.create(welcome())
	for name, intruder := range map[string]uuid.UUID{"another host": w.otherHost, "a stranger": w.stranger} {
		t.Run(name, func(t *testing.T) {
			if _, err := w.automatic.Overview(w.ctx, intruder, w.listingID); !errors.Is(err, automessages.ErrListingNotFound) {
				t.Fatalf("read: %v", err)
			}
			if _, err := w.automatic.Create(w.ctx, intruder, w.listingID, welcome()); !errors.Is(err, automessages.ErrListingNotFound) {
				t.Fatalf("create: %v", err)
			}
			if _, err := w.automatic.Update(w.ctx, intruder, w.listingID, mine.ID, steps()); !errors.Is(err, automessages.ErrMessageNotFound) {
				t.Fatalf("update: %v", err)
			}
			if err := w.automatic.Delete(w.ctx, intruder, w.listingID, mine.ID); !errors.Is(err, automessages.ErrMessageNotFound) {
				t.Fatalf("delete: %v", err)
			}
			if found := w.count(intruder, `SELECT count(*) FROM app.listing_auto_messages WHERE id = $1`, mine.ID); found != 0 {
				t.Fatal("read the row directly")
			}
			if _, err := w.as(intruder,
				`INSERT INTO app.listing_auto_messages (id, listing_id, host_id, name, trigger, body, private, enabled, active_since, created_at, updated_at)
				 VALUES (gen_random_uuid(), $1, $2, 'x', 'check_in_day', 'x', false, true, now(), now(), now())`,
				w.listingID, intruder); err == nil {
				t.Fatal("inserted a message on someone else's listing")
			}
		})
	}
	overview, err := w.automatic.Overview(w.ctx, w.host, w.listingID)
	if err != nil || len(overview.Messages) != 1 || overview.Messages[0].Body != mine.Body {
		t.Fatalf("host overview: %+v %v", overview, err)
	}
	if err := w.automatic.Delete(w.ctx, w.host, w.listingID, mine.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAListingHoldsAtMostTenMessages(t *testing.T) {
	w := newWorld(t)
	for range automessages.MaxPerListing {
		w.create(welcome())
	}
	if _, err := w.automatic.Create(w.ctx, w.host, w.listingID, welcome()); !errors.Is(err, automessages.ErrTooMany) {
		t.Fatalf("eleventh message: %v", err)
	}
}

func TestUnpaidCancelledAndRequestedBookingsGetNothing(t *testing.T) {
	w := newWorld(t)
	w.saveGuide("4821")
	w.create(welcome())
	w.create(steps())

	w.request(1)
	_, unpaid := w.request(4)
	w.confirm(unpaid)
	_, failed := w.request(7)
	w.confirm(failed)
	w.pay(failed, "failed")
	cancelledGuest, cancelled := w.request(10)
	w.confirm(cancelled)
	w.pay(cancelled, "succeeded")
	w.administer(`UPDATE app.bookings SET status = 'cancelled' WHERE id = $1`, cancelled)

	if sent := w.send(); sent != 0 {
		t.Fatalf("sent %d messages", sent)
	}
	if thread := w.thread(cancelledGuest); len(thread) != 0 {
		t.Fatalf("cancelled guest thread: %+v", thread)
	}
}

func TestEachMessageIsPostedOncePerBooking(t *testing.T) {
	w := newWorld(t)
	first := w.create(welcome())
	guest, bookingID := w.request(1)
	w.confirm(bookingID)
	w.pay(bookingID, "succeeded")

	var racers sync.WaitGroup
	total := make(chan int, 6)
	for range cap(total) {
		racers.Add(1)
		go func() {
			defer racers.Done()
			sent, err := w.automatic.SendDue(w.ctx)
			if err != nil {
				t.Error(err)
			}
			total <- sent
		}()
	}
	racers.Wait()
	close(total)
	sent := 0
	for count := range total {
		sent += count
	}
	if sent != 1 {
		t.Fatalf("concurrent runs sent %d", sent)
	}

	thread := w.thread(guest)
	if len(thread) != 1 || thread[0].Body != "Hi Maria S., welcome." || thread[0].SenderRole != messages.RoleHost || !thread[0].Automatic || thread[0].Private {
		t.Fatalf("thread: %+v", thread)
	}
	threads, err := w.messages.List(w.ctx, guest)
	if err != nil || threads[0].UnreadCount != 1 {
		t.Fatalf("the guest must see it as unread: %+v %v", threads, err)
	}

	changed := welcome()
	changed.Body = "Edited"
	if _, err := w.automatic.Update(w.ctx, w.host, w.listingID, first.ID, changed); err != nil {
		t.Fatal(err)
	}
	if err := w.messages.Delete(w.ctx, w.host, threads[0].ID, thread[0].ID); err != nil {
		t.Fatal(err)
	}
	if again := w.send(); again != 0 {
		t.Fatalf("editing and deleting the sent message resent %d", again)
	}

	secondGuest, secondBooking := w.request(4)
	w.confirm(secondBooking)
	w.pay(secondBooking, "succeeded")
	if sent := w.send(); sent != 1 {
		t.Fatalf("the next booking got %d", sent)
	}
	if thread := w.thread(secondGuest); len(thread) != 1 || thread[0].Body != "Edited" {
		t.Fatalf("the edit did not reach the next booking: %+v", thread)
	}
}

func TestSwitchedOffMessagesStayQuiet(t *testing.T) {
	w := newWorld(t)
	off := welcome()
	off.Enabled = false
	w.create(off)
	_, bookingID := w.request(1)
	w.confirm(bookingID)
	w.pay(bookingID, "succeeded")
	if sent := w.send(); sent != 0 {
		t.Fatalf("sent %d", sent)
	}
}

func TestCheckInDetailsReachOnlyThePartiesAndCloseAtCheckout(t *testing.T) {
	w := newWorld(t)
	w.create(steps())
	guest, bookingID := w.request(1)
	w.confirm(bookingID)
	w.pay(bookingID, "succeeded")
	if err := w.journey.Sweep(w.ctx); err != nil {
		t.Fatal(err)
	}
	for _, message := range w.thread(guest) {
		if message.Private {
			t.Fatal("sent before the host saved any entry instructions")
		}
	}
	w.saveGuide("4821")
	if err := w.journey.Sweep(w.ctx); err != nil {
		t.Fatal(err)
	}

	private := func(viewer uuid.UUID) messages.Message {
		t.Helper()
		for _, message := range w.thread(viewer) {
			if message.Private {
				return message
			}
		}
		t.Fatal("no private message in the thread")
		return messages.Message{}
	}
	want := "Keys are in the blue box\nCode: 4821"
	for _, viewer := range []uuid.UUID{guest, w.host} {
		if got := private(viewer); got.Details != want || got.Body != automessages.PrivateNotice {
			t.Fatalf("details: %+v", got)
		}
	}
	w.saveGuide("9900")
	if got := private(guest); !strings.Contains(got.Details, "9900") {
		t.Fatalf("a changed code did not reach the guest: %q", got.Details)
	}

	for _, leak := range []string{
		`SELECT count(*) FROM app.messages WHERE body LIKE '%blue box%' OR body LIKE '%4821%'`,
		`SELECT count(*) FROM app.conversations WHERE last_message_preview LIKE '%blue box%' OR last_message_preview LIKE '%4821%'`,
		`SELECT count(*) FROM app.notifications WHERE body LIKE '%blue box%' OR body LIKE '%4821%'`,
	} {
		if found := w.count(guest, leak); found != 0 {
			t.Fatalf("secrets leaked: %s", leak)
		}
	}
	for _, viewer := range []uuid.UUID{w.stranger, w.otherHost} {
		if found := w.count(viewer, `SELECT count(*) FROM app.message_private_bodies`); found != 0 {
			t.Fatal("an unrelated account read private bodies")
		}
	}

	w.administer(
		`INSERT INTO app.booking_stays (booking_id, guest_id, checked_in_at, checked_out_at) VALUES ($1, $2, now(), now())`,
		bookingID, guest)
	if got := private(guest); got.Details != "" || got.Body != automessages.PrivateNotice {
		t.Fatalf("details stayed open after checkout: %+v", got)
	}
	if got := private(w.host); got.Details == "" {
		t.Fatal("the host lost their own message")
	}

	w.administer(`DELETE FROM app.booking_stays WHERE booking_id = $1`, bookingID)
	w.administer(`UPDATE app.bookings SET status = 'cancelled' WHERE id = $1`, bookingID)
	if got := private(guest); got.Details != "" {
		t.Fatalf("a cancelled guest can still read the details: %+v", got)
	}
	if found := w.count(guest, `SELECT count(*) FROM app.message_private_bodies`); found != 0 {
		t.Fatal("the database still shows a cancelled guest the private body")
	}
}

func TestATypedEntryCodeIsTreatedAsPrivate(t *testing.T) {
	w := newWorld(t)
	w.saveGuide("4821")
	typed := welcome()
	typed.Body = "Your code is 4821."
	w.create(typed)
	guest, bookingID := w.request(1)
	w.confirm(bookingID)
	w.pay(bookingID, "succeeded")
	w.send()
	thread := w.thread(guest)
	if len(thread) != 1 || !thread[0].Private || thread[0].Body != automessages.PrivateNotice || thread[0].Details != typed.Body {
		t.Fatalf("thread: %+v", thread)
	}
}

func TestAFarFutureBookingDoesNotReceiveCheckInDetailsEarly(t *testing.T) {
	w := newWorld(t)
	w.saveGuide("4821")
	early := steps()
	early.Trigger = automessages.TriggerBookingConfirmed
	w.create(early)
	_, bookingID := w.request(30)
	w.confirm(bookingID)
	w.pay(bookingID, "succeeded")
	if sent := w.send(); sent != 0 {
		t.Fatalf("sent %d check-in messages a month early", sent)
	}
}

func TestNobodyCanForgeAnAutomaticMessage(t *testing.T) {
	w := newWorld(t)
	guest, bookingID := w.request(1)
	conversation, err := w.messages.Open(w.ctx, guest, messages.OpenTarget{BookingID: bookingID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.as(guest,
		`INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at, automatic)
		 VALUES (gen_random_uuid(), $1, $2, 'fake', now(), true)`, conversation.ID, guest); err == nil {
		t.Fatal("a guest marked their own message automatic")
	}
	if _, err := w.as(guest,
		`INSERT INTO app.booking_auto_messages (booking_id, auto_message_id) VALUES ($1, gen_random_uuid())`, bookingID); err == nil {
		t.Fatal("a guest wrote a delivery record")
	}
}
