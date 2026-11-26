package notifications_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type silentPublisher struct{}

func (silentPublisher) Publish(uuid.UUID, string, any) {}

type trip struct {
	t                     *testing.T
	ctx                   context.Context
	pool                  *pgxpool.Pool
	host, guest, stranger uuid.UUID
	listingID, bookingID  uuid.UUID
	stays                 *stays.StayService
	reviews               *reviews.ReviewService
	inbox                 *notifications.NotificationService
	journey               *notifications.JourneyService
	messages              *messages.MessageService
}

func (trip *trip) as(userID uuid.UUID, statement string, args ...any) (int64, error) {
	var affected int64
	err := db.WithUserTx(trip.ctx, trip.pool, userID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(trip.ctx, statement, args...)
		affected = tag.RowsAffected()
		return err
	})
	return affected, err
}

func (trip *trip) must(userID uuid.UUID, statement string, args ...any) {
	trip.t.Helper()
	if _, err := trip.as(userID, statement, args...); err != nil {
		trip.t.Fatal(err)
	}
}

func (trip *trip) administer(statement string, args ...any) {
	trip.t.Helper()
	admin, err := pgx.Connect(trip.ctx, os.Getenv("TEST_SUPERUSER_DATABASE_URL"))
	if err != nil {
		trip.t.Fatal(err)
	}
	defer func() { _ = admin.Close(trip.ctx) }()
	if _, err := admin.Exec(trip.ctx, statement, args...); err != nil {
		trip.t.Fatal(err)
	}
}

func (trip *trip) sweep() {
	trip.t.Helper()
	if err := trip.journey.Sweep(trip.ctx); err != nil {
		trip.t.Fatal(err)
	}
}

func (trip *trip) kinds(userID uuid.UUID) []notifications.Kind {
	trip.t.Helper()
	inbox, err := trip.inbox.List(trip.ctx, userID)
	if err != nil {
		trip.t.Fatal(err)
	}
	found := []notifications.Kind{}
	for _, item := range inbox.Notifications {
		if item.BookingID == trip.bookingID {
			found = append(found, item.Kind)
		}
	}
	return found
}

func (trip *trip) expectKinds(userID uuid.UUID, wanted ...notifications.Kind) {
	trip.t.Helper()
	found := trip.kinds(userID)
	for _, kind := range wanted {
		if !slices.Contains(found, kind) {
			trip.t.Fatalf("missing %s in %v", kind, found)
		}
	}
}

func newTrip(t *testing.T) *trip {
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

	stayStore := stays.NewPostgresStayStore(pool)
	notificationStore := notifications.NewPostgresNotificationStore(pool)
	trip := &trip{
		t: t, ctx: ctx, pool: pool,
		host: uuid.New(), guest: uuid.New(), stranger: uuid.New(), bookingID: uuid.New(),
		stays:    stays.NewStayService(stayStore, stayStore),
		reviews:  reviews.NewReviewService(reviews.NewPostgresReviewStore(pool)),
		inbox:    notifications.NewNotificationService(notificationStore),
		journey:  notifications.NewJourneyService(notificationStore, silentPublisher{}),
		messages: messages.NewMessageService(messages.NewPostgresMessageStore(pool)),
	}
	trip.journey.WithLifecycle(trip.stays, trip.reviews)

	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, trip.host, listings.CreateListingInput{
		Title: "Trip Flow House", Description: "A complete listing used by the trip flow integration test.",
		Address: listings.Address{Line1: "1 Trip Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	trip.listingID = listing.ID
	trip.must(trip.host, `UPDATE app.listings SET status = 'published' WHERE id = $1`, listing.ID)
	for _, account := range []uuid.UUID{trip.guest, trip.stranger} {
		trip.must(account, `INSERT INTO app.accounts (id) VALUES ($1)`, account)
	}
	trip.must(trip.guest, `INSERT INTO app.account_names (account_id, display_name) VALUES ($1, 'Maria S.')`, trip.guest)
	trip.must(trip.guest,
		`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on)
		 VALUES ($1, $2, $3, 'requested', CURRENT_DATE - 1, CURRENT_DATE + 2)`,
		trip.bookingID, listing.ID, trip.guest)
	trip.administer(`UPDATE app.bookings SET status = 'confirmed' WHERE id = $1`, trip.bookingID)
	return trip
}

func TestAWholeTripFromConfirmationToPublishedReviews(t *testing.T) {
	trip := newTrip(t)
	ctx := trip.ctx

	trip.sweep()
	trip.expectKinds(trip.guest, notifications.KindBookingConfirmed, notifications.KindCheckInOpen)
	trip.expectKinds(trip.host, notifications.KindBookingConfirmed, notifications.KindArrivalGuideNeeded)

	guide := stays.GuideInput{
		Schedule:       stays.Schedule{CheckInTime: stays.DefaultCheckInTime, CheckOutTime: stays.DefaultCheckOutTime, TimeZone: stays.DefaultTimeZone},
		ArrivalDetails: stays.ArrivalDetails{EntryInstructions: "Side gate", EntryCode: "4821"},
	}
	if _, err := trip.stays.SaveGuide(ctx, trip.guest, trip.listingID, guide); !errors.Is(err, stays.ErrListingNotFound) {
		t.Fatalf("a guest saved the host's arrival guide: %v", err)
	}
	if _, err := trip.stays.SaveGuide(ctx, trip.host, trip.listingID, guide); err != nil {
		t.Fatal(err)
	}
	trip.sweep()
	trip.expectKinds(trip.guest, notifications.KindArrivalReady)

	if _, err := trip.stays.Stay(ctx, trip.stranger, trip.bookingID); !errors.Is(err, stays.ErrStayNotFound) {
		t.Fatalf("a stranger read the stay: %v", err)
	}
	var visibleCodes int
	if err := db.WithUserTx(ctx, trip.pool, trip.stranger.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM app.listing_arrival_guides WHERE listing_id = $1`, trip.listingID).Scan(&visibleCodes)
	}); err != nil {
		t.Fatal(err)
	}
	if visibleCodes != 0 {
		t.Fatal("a stranger can see the entry code")
	}

	view, err := trip.stays.CheckIn(ctx, trip.guest, trip.bookingID)
	if err != nil || view.Arrival == nil || view.Arrival.EntryCode != "4821" {
		t.Fatalf("check in: %+v %v", view, err)
	}
	if view, err = trip.stays.CheckOut(ctx, trip.guest, trip.bookingID); err != nil || view.Phase != stays.PhaseCheckedOut {
		t.Fatalf("early check out must not complete the stay: %+v %v", view, err)
	}
	trip.sweep()
	trip.expectKinds(trip.host, notifications.KindGuestCheckedIn, notifications.KindGuestCheckedOut)

	early := reviews.CreateReviewInput{BookingID: trip.bookingID, Rating: 5, Comment: "Too soon"}
	if _, err := trip.reviews.CreateReview(ctx, trip.guest, early); !errors.Is(err, reviews.ErrBookingNotReviewable) {
		t.Fatalf("reviewed an unfinished stay: %v", err)
	}

	trip.administer(`UPDATE app.bookings SET ends_on = CURRENT_DATE - 1, starts_on = CURRENT_DATE - 3 WHERE id = $1`, trip.bookingID)
	trip.sweep()
	trip.expectKinds(trip.guest, notifications.KindReviewInvite)
	trip.expectKinds(trip.host, notifications.KindReviewInvite)

	four := 4
	if _, err := trip.reviews.CreateReview(ctx, trip.guest, reviews.CreateReviewInput{
		BookingID: trip.bookingID, Rating: 4, Comment: "Lovely house", Categories: &reviews.Categories{Cleanliness: &four},
	}); err != nil {
		t.Fatal(err)
	}
	listing, err := trip.reviews.ForListing(ctx, trip.listingID)
	if err != nil || listing.Count != 0 {
		t.Fatalf("one-sided review is already public: %+v %v", listing, err)
	}
	hostSide, err := trip.reviews.ForBooking(ctx, trip.host, trip.bookingID)
	if err != nil || hostSide.Theirs != nil {
		t.Fatalf("host can read the guest review before writing: %+v %v", hostSide, err)
	}
	if _, err := trip.as(trip.guest, `UPDATE app.listing_reviews SET published_at = now() WHERE booking_id = $1`, trip.bookingID); err == nil {
		t.Fatal("a guest published their own review early")
	}

	if _, err := trip.reviews.CreateReview(ctx, trip.host, reviews.CreateReviewInput{BookingID: trip.bookingID, Rating: 5, Comment: "Great guest"}); err != nil {
		t.Fatal(err)
	}
	listing, err = trip.reviews.ForListing(ctx, trip.listingID)
	if err != nil || listing.Count != 1 || listing.Average != 4 || listing.Reviews[0].AuthorName != "Maria S." ||
		listing.Categories.Cleanliness == nil || *listing.Categories.Cleanliness != 4 {
		t.Fatalf("published listing reviews: %+v %v", listing, err)
	}
	public, err := listings.NewPostgresListingStore(trip.pool).GetPublished(ctx, trip.listingID)
	if err != nil || public.Rating == nil || public.Rating.Count != 1 || public.Rating.Average != 4 || public.CheckInTime != stays.DefaultCheckInTime {
		t.Fatalf("listing rating and times: %+v %v", public, err)
	}
	about, err := trip.reviews.AboutMe(ctx, trip.guest)
	if err != nil || about.Count != 1 || about.Average != 5 {
		t.Fatalf("reviews about the guest: %+v %v", about, err)
	}
	unrelated, err := trip.reviews.AboutMe(ctx, trip.stranger)
	if err != nil || unrelated.Count != 0 {
		t.Fatalf("stranger summary: %+v %v", unrelated, err)
	}
	var leaked int
	if err := db.WithUserTx(ctx, trip.pool, trip.stranger.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM app.guest_reviews WHERE guest_id = $1`, trip.guest).Scan(&leaked)
	}); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("an unrelated account can read a guest's reviews")
	}

	trip.sweep()
	trip.expectKinds(trip.guest, notifications.KindReviewPublished)
	before := len(trip.kinds(trip.guest))
	trip.sweep()
	if after := len(trip.kinds(trip.guest)); after != before {
		t.Fatalf("a second sweep sent more: %d then %d", before, after)
	}

	threads, err := trip.messages.List(ctx, trip.guest)
	if err != nil || len(threads) != 1 || threads[0].UnreadCount != 0 {
		t.Fatalf("trip thread: %+v %v", threads, err)
	}
	page, err := trip.messages.Messages(ctx, trip.host, threads[0].ID, nil, 50)
	if err != nil || len(page.Messages) != 4 {
		t.Fatalf("expected confirmed, ready, checked in and checked out notes: %+v %v", page.Messages, err)
	}
	for _, message := range page.Messages {
		if message.SenderRole != messages.RoleSystem || message.BookingID == nil || *message.BookingID != trip.bookingID {
			t.Fatalf("not a trip note: %+v", message)
		}
	}
	if _, err := trip.messages.Send(ctx, trip.guest, threads[0].ID, messages.Draft{Body: "Thanks again"}); err != nil {
		t.Fatal(err)
	}
	if _, err := trip.as(trip.guest,
		`INSERT INTO app.messages (id, conversation_id, sender_id, body, created_at, booking_id, event_kind)
		 VALUES ($1, $2, NULL, 'fake', now(), $3, 'fake_event')`, uuid.New(), threads[0].ID, trip.bookingID); err == nil {
		t.Fatal("a guest forged a system message")
	}

	inbox, err := trip.inbox.List(ctx, trip.guest)
	if err != nil || inbox.UnreadCount == 0 {
		t.Fatalf("inbox: %+v %v", inbox, err)
	}
	if err := trip.inbox.MarkRead(ctx, trip.stranger, inbox.Notifications[0].ID); !errors.Is(err, notifications.ErrNotificationNotFound) {
		t.Fatalf("a stranger marked someone else's notification: %v", err)
	}
	if err := trip.inbox.MarkAllRead(ctx, trip.guest); err != nil {
		t.Fatal(err)
	}
	if inbox, err = trip.inbox.List(ctx, trip.guest); err != nil || inbox.UnreadCount != 0 {
		t.Fatalf("unread after marking all: %+v %v", inbox, err)
	}
}

func TestNothingCanPullAListingOrABookingOutFromUnderAGuest(t *testing.T) {
	trip := newTrip(t)
	ctx := trip.ctx
	listingService := listings.NewListingService(listings.NewPostgresListingStore(trip.pool))

	if err := listingService.DeleteListing(ctx, trip.host, trip.listingID); !errors.Is(err, listings.ErrListingInUse) {
		t.Fatalf("deleted a listing mid trip: %v", err)
	}

	attacks := map[string]string{
		"delete the row directly":     `DELETE FROM app.listings WHERE id = $1`,
		"turn it back into a draft":   `UPDATE app.listings SET status = 'draft' WHERE id = $1`,
		"pause it":                    `UPDATE app.listings SET status = 'paused' WHERE id = $1`,
		"cancel the guest":            `UPDATE app.bookings SET status = 'cancelled' WHERE listing_id = $1`,
		"end the stay early":          `UPDATE app.bookings SET ends_on = CURRENT_DATE WHERE listing_id = $1`,
		"hand the stay to themselves": `UPDATE app.bookings SET guest_id = app.current_user_id() WHERE listing_id = $1`,
	}
	for name, attack := range attacks {
		t.Run(name, func(t *testing.T) {
			affected, err := trip.as(trip.host, attack, trip.listingID)
			if err == nil && affected != 0 {
				t.Fatalf("the host changed %d rows", affected)
			}
		})
	}

	view, err := trip.stays.Stay(ctx, trip.guest, trip.bookingID)
	if err != nil || view.Phase != stays.PhaseCheckInOpen {
		t.Fatalf("the guest's stay changed: %+v %v", view, err)
	}

	guide := stays.GuideInput{
		Schedule:       stays.Schedule{CheckInTime: stays.DefaultCheckInTime, CheckOutTime: stays.DefaultCheckOutTime, TimeZone: stays.DefaultTimeZone},
		ArrivalDetails: stays.ArrivalDetails{EntryInstructions: "Use the new keypad"},
	}
	if _, err := trip.stays.SaveGuide(ctx, trip.host, trip.listingID, guide); err != nil {
		t.Fatalf("entry details must stay editable mid trip: %v", err)
	}
	guide.CheckOutTime = "08:00"
	if _, err := trip.stays.SaveGuide(ctx, trip.host, trip.listingID, guide); !errors.Is(err, stays.ErrScheduleLocked) {
		t.Fatalf("checkout time moved on a staying guest: %v", err)
	}
}
