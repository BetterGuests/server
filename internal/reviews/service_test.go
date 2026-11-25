package reviews

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type reviewStoreStub struct {
	Store
	booking   Booking
	found     []Review
	bookErr   error
	insertErr error
	inserted  []NewReview
	published int
}

func (stub *reviewStoreStub) Booking(context.Context, uuid.UUID, uuid.UUID) (Booking, error) {
	return stub.booking, stub.bookErr
}

func (stub *reviewStoreStub) Insert(_ context.Context, _ uuid.UUID, review NewReview) error {
	stub.inserted = append(stub.inserted, review)
	return stub.insertErr
}

func (stub *reviewStoreStub) ForBooking(context.Context, uuid.UUID, uuid.UUID) ([]Review, error) {
	return stub.found, nil
}

func (stub *reviewStoreStub) PublishDue(context.Context) (int, error) {
	stub.published++
	return 0, nil
}

func completedStay(finished time.Time) *reviewStoreStub {
	return &reviewStoreStub{booking: Booking{
		BookingID: uuid.New(), ListingID: uuid.New(), GuestID: uuid.New(), HostID: uuid.New(),
		GuestName: "Maria S.", HostName: "Dana", Status: statusCompleted, CompletedAt: &finished,
	}}
}

func input(store *reviewStoreStub) CreateReviewInput {
	return CreateReviewInput{BookingID: store.booking.BookingID, Rating: 5, Comment: "  Great stay  "}
}

func TestGuestReviewGoesToTheListingUnderTheGuestsName(t *testing.T) {
	store := completedStay(time.Now().Add(-time.Hour))
	review, err := NewReviewService(store).CreateReview(context.Background(), store.booking.GuestID, input(store))
	if err != nil {
		t.Fatal(err)
	}
	if review.AuthorRole != RoleGuest || review.AuthorName != "Maria S." || review.Comment != "Great stay" {
		t.Fatalf("unexpected review: %+v", review)
	}
	if len(store.inserted) != 1 || store.published != 1 {
		t.Fatalf("expected one insert followed by a publish check, got %d and %d", len(store.inserted), store.published)
	}
}

func TestHostReviewIsAboutTheGuest(t *testing.T) {
	store := completedStay(time.Now().Add(-time.Hour))
	review, err := NewReviewService(store).CreateReview(context.Background(), store.booking.HostID, input(store))
	if err != nil {
		t.Fatal(err)
	}
	if review.AuthorRole != RoleHost || review.AuthorName != "Dana" || store.inserted[0].GuestID != store.booking.GuestID {
		t.Fatalf("unexpected host review: %+v", store.inserted[0])
	}
}

func TestCreateReviewRejections(t *testing.T) {
	score, tooHigh := 4, 6
	cases := map[string]struct {
		change func(*reviewStoreStub, *CreateReviewInput) uuid.UUID
		want   error
	}{
		"rating too high": {func(s *reviewStoreStub, in *CreateReviewInput) uuid.UUID { in.Rating = 6; return s.booking.GuestID }, ErrInvalidReview},
		"blank comment": {func(s *reviewStoreStub, in *CreateReviewInput) uuid.UUID {
			in.Comment = "   "
			return s.booking.GuestID
		}, ErrInvalidReview},
		"category score too high": {func(s *reviewStoreStub, in *CreateReviewInput) uuid.UUID {
			in.Categories = &Categories{Value: &tooHigh}
			return s.booking.GuestID
		}, ErrInvalidReview},
		"host sends category scores": {func(s *reviewStoreStub, in *CreateReviewInput) uuid.UUID {
			in.Categories = &Categories{Value: &score}
			return s.booking.HostID
		}, ErrInvalidReview},
		"stay still going": {func(s *reviewStoreStub, _ *CreateReviewInput) uuid.UUID {
			s.booking.Status = "confirmed"
			s.booking.CompletedAt = nil
			return s.booking.GuestID
		}, ErrBookingNotReviewable},
		"stranger": {func(s *reviewStoreStub, _ *CreateReviewInput) uuid.UUID {
			s.bookErr = ErrBookingNotFound
			return uuid.New()
		}, ErrBookingNotReviewable},
		"fourteen days have passed": {func(s *reviewStoreStub, _ *CreateReviewInput) uuid.UUID {
			finished := time.Now().Add(-ReviewWindow - time.Minute)
			s.booking.CompletedAt = &finished
			return s.booking.GuestID
		}, ErrReviewWindowClosed},
		"already reviewed": {func(s *reviewStoreStub, _ *CreateReviewInput) uuid.UUID {
			s.insertErr = ErrReviewAlreadyExists
			return s.booking.GuestID
		}, ErrReviewAlreadyExists},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store := completedStay(time.Now().Add(-time.Hour))
			request := input(store)
			actor := test.change(store, &request)
			if _, err := NewReviewService(store).CreateReview(context.Background(), actor, request); !errors.Is(err, test.want) {
				t.Fatalf("want %v, got %v", test.want, err)
			}
		})
	}
}

func TestTheOtherSidesReviewStaysHiddenUntilPublished(t *testing.T) {
	store := completedStay(time.Now().Add(-time.Hour))
	store.found = []Review{{AuthorRole: RoleHost, Comment: "secret"}}
	result, err := NewReviewService(store).ForBooking(context.Background(), store.booking.GuestID, store.booking.BookingID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Theirs != nil || !result.CanReview || result.SubjectName != "Dana" {
		t.Fatalf("unpublished review leaked or review blocked: %+v", result)
	}
}
