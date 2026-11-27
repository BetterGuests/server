package messages

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type conversationFixture struct {
	ctx          context.Context
	store        *PostgresMessageStore
	host         uuid.UUID
	guest        uuid.UUID
	listing      uuid.UUID
	pool         *pgxpool.Pool
	conversation uuid.UUID
}

func newConversationFixture(t *testing.T) conversationFixture {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	people := accounts.NewPostgresAccountStore(pool)
	join := func(name, photo string) uuid.UUID {
		clerkUserID := "clerk-" + uuid.NewString()
		id := accounts.DeterministicID(clerkUserID)
		if err := people.Ensure(ctx, accounts.Account{ID: id, ClerkUserID: clerkUserID}); err != nil {
			t.Fatal(err)
		}
		if err := people.Remember(ctx, id, name, photo); err != nil {
			t.Fatal(err)
		}
		return id
	}
	host := join("Luis H.", "https://img.clerk.com/luis")
	guest := join("Maria S.", "https://img.clerk.com/maria")

	listing, err := listings.NewListingService(listings.NewPostgresListingStore(pool)).CreateListing(ctx, host, listings.CreateListingInput{
		Title: "Photo House", Description: "A complete listing used by the conversation photo test.",
		Address: listings.Address{Line1: "1 Photo Way " + uuid.NewString(), City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.Cabin, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
		Amenities: []listings.Amenity{listings.WiFi},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithUserTx(ctx, pool, host.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'published', nightly_price_cents = 25000 WHERE id = $1`, listing.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	store := NewPostgresMessageStore(pool)
	conversation, err := store.Open(ctx, guest, uuid.New(), OpenTarget{ListingID: listing.ID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	return conversationFixture{
		ctx: ctx, store: store, host: host, guest: guest,
		listing: listing.ID, pool: pool, conversation: conversation,
	}
}

func (fixture conversationFixture) requestStay(t *testing.T, startsInDays int) uuid.UUID {
	t.Helper()
	bookingID := uuid.New()
	err := db.WithUserTx(fixture.ctx, fixture.pool, fixture.guest.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(fixture.ctx,
			`INSERT INTO app.bookings (id, listing_id, guest_id, status, starts_on, ends_on)
			 VALUES ($1, $2, $3, 'requested', CURRENT_DATE + $4::integer, CURRENT_DATE + $4::integer + 2)`,
			bookingID, fixture.listing, fixture.guest, startsInDays)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return bookingID
}

func TestBothSidesSeeTheSoonestOpenStayOnTheConversation(t *testing.T) {
	fixture := newConversationFixture(t)
	fixture.requestStay(t, 40)
	soonest := fixture.requestStay(t, 10)
	for _, person := range []uuid.UUID{fixture.guest, fixture.host} {
		summaries, err := fixture.store.List(fixture.ctx, person)
		if err != nil {
			t.Fatal(err)
		}
		if len(summaries) != 1 || summaries[0].Stay == nil {
			t.Fatalf("summaries = %+v, want one conversation with a stay", summaries)
		}
		stay := *summaries[0].Stay
		if stay.BookingID != soonest || stay.Status != "requested" || stay.StartsOn >= stay.EndsOn {
			t.Fatalf("stay = %+v, want the soonest requested booking %s", stay, soonest)
		}
	}
}

func TestEachSideOfAConversationSeesTheOtherPersonsPhoto(t *testing.T) {
	fixture := newConversationFixture(t)
	ctx, store, host, guest, conversation := fixture.ctx, fixture.store, fixture.host, fixture.guest, fixture.conversation

	for viewer, want := range map[uuid.UUID]string{host: "https://img.clerk.com/maria", guest: "https://img.clerk.com/luis"} {
		summary, err := store.Get(ctx, viewer, conversation)
		if err != nil {
			t.Fatal(err)
		}
		if summary.CounterpartyAvatarURL != want {
			t.Fatalf("photo = %q, want %q", summary.CounterpartyAvatarURL, want)
		}
	}
}

func TestARetriedSendWithTheSameIDStoresOneMessage(t *testing.T) {
	fixture := newConversationFixture(t)
	draft := Draft{ID: uuid.New(), Body: "Is early check-in possible?"}

	first, _, created, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, draft, time.Now())
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}
	again, _, created, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, draft, time.Now())
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("retry: created=%v id=%s err=%v", created, again.ID, err)
	}
	if _, _, _, err := fixture.store.Insert(fixture.ctx, fixture.host, fixture.conversation, draft, time.Now()); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("someone else reusing the id: err = %v, want ErrInvalidMessage", err)
	}
	page, err := fixture.store.Messages(fixture.ctx, fixture.host, fixture.conversation, nil, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("messages = %d, err = %v", len(page), err)
	}
}

func TestRepliesCarryAPreviewOfTheOriginal(t *testing.T) {
	fixture := newConversationFixture(t)
	original, _, _, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, Draft{ID: uuid.New(), Body: "Do you have parking?"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	reply, _, _, err := fixture.store.Insert(fixture.ctx, fixture.host, fixture.conversation, Draft{ID: uuid.New(), Body: "Yes, two spots.", ReplyToID: original.ID}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if reply.ReplyTo == nil || reply.ReplyTo.ID != original.ID || reply.ReplyTo.Body != original.Body || reply.ReplyTo.SenderRole != RoleGuest {
		t.Fatalf("reply preview = %+v", reply.ReplyTo)
	}
	if _, _, _, err := fixture.store.Insert(fixture.ctx, fixture.host, fixture.conversation, Draft{ID: uuid.New(), Body: "x", ReplyToID: uuid.New()}, time.Now()); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("reply to an unknown message: err = %v", err)
	}
}

func TestOnlyTheSenderCanDeleteAndThePreviewMovesBack(t *testing.T) {
	fixture := newConversationFixture(t)
	start := time.Now()
	if _, _, _, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, Draft{ID: uuid.New(), Body: "first"}, start); err != nil {
		t.Fatal(err)
	}
	second, _, _, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, Draft{ID: uuid.New(), Body: "oops"}, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.Delete(fixture.ctx, fixture.host, fixture.conversation, second.ID); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("host deleting the guest's message: err = %v", err)
	}
	if _, err := fixture.store.Delete(fixture.ctx, fixture.guest, fixture.conversation, second.ID); err != nil {
		t.Fatal(err)
	}
	summary, err := fixture.store.Get(fixture.ctx, fixture.host, fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	if summary.LastMessagePreview != "first" {
		t.Fatalf("preview = %q, want the message before the deleted one", summary.LastMessagePreview)
	}
}

func TestReactionsToggleAndAreSeenByBothSides(t *testing.T) {
	fixture := newConversationFixture(t)
	message, _, _, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, Draft{ID: uuid.New(), Body: "See you Friday"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	reactions, _, err := fixture.store.ToggleReaction(fixture.ctx, fixture.host, fixture.conversation, message.ID, uuid.New(), "❤️", time.Now())
	if err != nil || len(reactions) != 1 || reactions[0].Role != RoleHost {
		t.Fatalf("after adding: %+v, err = %v", reactions, err)
	}
	page, err := fixture.store.Messages(fixture.ctx, fixture.guest, fixture.conversation, nil, 10)
	if err != nil || len(page) != 1 || len(page[0].Reactions) != 1 || page[0].Reactions[0].Emoji != "❤️" {
		t.Fatalf("guest sees %+v, err = %v", page, err)
	}
	reactions, _, err = fixture.store.ToggleReaction(fixture.ctx, fixture.host, fixture.conversation, message.ID, uuid.New(), "❤️", time.Now())
	if err != nil || len(reactions) != 0 {
		t.Fatalf("after removing: %+v, err = %v", reactions, err)
	}
}

func TestReplyMinutesNeedsThreeRepliesAndIsAveraged(t *testing.T) {
	fixture := newConversationFixture(t)
	start := time.Now().Add(-time.Hour)
	at := start
	exchange := func(gap time.Duration) {
		t.Helper()
		if _, _, _, err := fixture.store.Insert(fixture.ctx, fixture.guest, fixture.conversation, Draft{ID: uuid.New(), Body: "question"}, at); err != nil {
			t.Fatal(err)
		}
		at = at.Add(gap)
		if _, _, _, err := fixture.store.Insert(fixture.ctx, fixture.host, fixture.conversation, Draft{ID: uuid.New(), Body: "answer"}, at); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Minute)
	}

	exchange(4 * time.Minute)
	exchange(6 * time.Minute)
	summary, err := fixture.store.Get(fixture.ctx, fixture.guest, fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CounterpartyReplyMins != nil {
		t.Fatalf("two replies is too few to show, got %d", *summary.CounterpartyReplyMins)
	}

	exchange(8 * time.Minute)
	summary, err = fixture.store.Get(fixture.ctx, fixture.guest, fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CounterpartyReplyMins == nil || *summary.CounterpartyReplyMins != 6 {
		t.Fatalf("reply minutes = %v, want 6", summary.CounterpartyReplyMins)
	}
}

func TestDeletingAConversationHidesItOnlyForThatPersonUntilSomeoneWritesAgain(t *testing.T) {
	fixture := newConversationFixture(t)
	ctx, store, host, guest, conversation := fixture.ctx, fixture.store, fixture.host, fixture.guest, fixture.conversation
	start := time.Now()
	if _, _, _, err := store.Insert(ctx, guest, conversation, Draft{ID: uuid.New(), Body: "Thanks for the stay"}, start); err != nil {
		t.Fatal(err)
	}

	summary, err := store.Get(ctx, guest, conversation)
	if err != nil || !summary.CanDelete {
		t.Fatalf("can delete = %v, err = %v", summary.CanDelete, err)
	}
	if err := store.Hide(ctx, guest, conversation, start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	listsConversation := func(viewer uuid.UUID) bool {
		t.Helper()
		summaries, err := store.List(ctx, viewer)
		if err != nil {
			t.Fatal(err)
		}
		for _, summary := range summaries {
			if summary.ID == conversation {
				return true
			}
		}
		return false
	}
	countMessages := func(viewer uuid.UUID) int {
		t.Helper()
		page, err := store.Messages(ctx, viewer, conversation, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(page)
	}

	if listsConversation(guest) || countMessages(guest) != 0 {
		t.Fatal("the guest still sees the conversation they deleted")
	}
	if !listsConversation(host) || countMessages(host) != 1 {
		t.Fatal("the host lost their copy of the conversation")
	}

	if _, _, _, err := store.Insert(ctx, host, conversation, Draft{ID: uuid.New(), Body: "Come back any time"}, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !listsConversation(guest) || countMessages(guest) != 1 {
		t.Fatal("a new message should bring the conversation back with only the new message")
	}
}

func TestPinningAConversationIsOnlyForThePersonWhoPinnedIt(t *testing.T) {
	fixture := newConversationFixture(t)
	ctx, store, host, guest, conversation := fixture.ctx, fixture.store, fixture.host, fixture.guest, fixture.conversation
	pinnedFor := func(viewer uuid.UUID) bool {
		t.Helper()
		summary, err := store.Get(ctx, viewer, conversation)
		if err != nil {
			t.Fatal(err)
		}
		return summary.Pinned
	}

	if err := store.SetPinned(ctx, guest, conversation, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !pinnedFor(guest) || pinnedFor(host) {
		t.Fatalf("guest pinned = %v, host pinned = %v", pinnedFor(guest), pinnedFor(host))
	}
	if err := store.SetPinned(ctx, guest, conversation, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pinnedFor(guest) {
		t.Fatal("the conversation is still pinned after unpinning")
	}
}

func TestAConversationWithoutAnyBookingIsNotAnEndedStay(t *testing.T) {
	fixture := newConversationFixture(t)
	summary, err := fixture.store.Get(fixture.ctx, fixture.guest, fixture.conversation)
	if err != nil {
		t.Fatal(err)
	}
	if summary.StayEnded {
		t.Fatal("an inquiry with no booking was marked as an ended stay")
	}
	if summary.Stay != nil {
		t.Fatalf("an inquiry with no booking carries a stay: %+v", summary.Stay)
	}
}
