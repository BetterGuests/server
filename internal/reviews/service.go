package reviews

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

var (
	ErrInvalidReview        = errors.New("invalid review")
	ErrBookingNotReviewable = errors.New("booking is not reviewable")
	ErrReviewAlreadyExists  = errors.New("booking already has a review")
	ErrReviewWindowClosed   = errors.New("the 14 days to review this stay have passed")
	ErrBookingNotFound      = errors.New("booking not found")
	errUnusableSummary      = errors.New("summary is empty or too long")
)

type ReviewService struct {
	store      Store
	nudger     Nudger
	summaries  SummaryStore
	summarizer Summarizer
	generating singleflight.Group
	now        func() time.Time
	newID      func() uuid.UUID
}

func NewReviewService(store Store) *ReviewService {
	return &ReviewService{store: store, now: time.Now, newID: uuid.New}
}

func (service *ReviewService) WithNudger(nudger Nudger) *ReviewService {
	service.nudger = nudger
	return service
}

func (service *ReviewService) WithSummaries(store SummaryStore, summarizer Summarizer) *ReviewService {
	service.summaries = store
	service.summarizer = summarizer
	return service
}

func (service *ReviewService) CreateReview(ctx context.Context, actorID uuid.UUID, input CreateReviewInput) (Review, error) {
	input, err := cleanInput(input)
	if err != nil {
		return Review{}, err
	}
	booking, err := service.store.Booking(ctx, actorID, input.BookingID)
	if errors.Is(err, ErrBookingNotFound) {
		return Review{}, ErrBookingNotReviewable
	}
	if err != nil {
		return Review{}, err
	}
	if booking.Status != statusCompleted || booking.CompletedAt == nil {
		return Review{}, ErrBookingNotReviewable
	}
	now := service.now().UTC()
	if !now.Before(booking.CompletedAt.Add(ReviewWindow)) {
		return Review{}, ErrReviewWindowClosed
	}

	review := Review{
		ID:           service.newID(),
		BookingID:    booking.BookingID,
		ListingID:    booking.ListingID,
		ListingTitle: booking.ListingTitle,
		AuthorRole:   RoleGuest,
		AuthorName:   nameOr(booking.GuestName, missingGuestName),
		Rating:       input.Rating,
		Comment:      input.Comment,
		Categories:   input.Categories,
		CreatedAt:    now,
	}
	if actorID == booking.HostID {
		if input.Categories != nil {
			return Review{}, fmt.Errorf("%w: category scores are only for stays", ErrInvalidReview)
		}
		review.AuthorRole = RoleHost
		review.AuthorName = nameOr(booking.HostName, missingHostName)
	}
	if err := service.store.Insert(ctx, actorID, NewReview{Review: review, GuestID: booking.GuestID, HostID: booking.HostID}); err != nil {
		return Review{}, err
	}
	if _, err := service.PublishDue(ctx); err != nil {
		slog.Error("Publish reviews", "error", err)
	}
	if service.nudger != nil {
		service.nudger.Nudge()
	}
	return review, nil
}

func (service *ReviewService) PublishDue(ctx context.Context) (int, error) {
	return service.store.PublishDue(ctx)
}

func (service *ReviewService) ForBooking(ctx context.Context, actorID, bookingID uuid.UUID) (BookingReviews, error) {
	booking, err := service.store.Booking(ctx, actorID, bookingID)
	if err != nil {
		return BookingReviews{}, err
	}
	found, err := service.store.ForBooking(ctx, actorID, bookingID)
	if err != nil {
		return BookingReviews{}, err
	}
	result := BookingReviews{
		BookingID:    booking.BookingID,
		ListingTitle: booking.ListingTitle,
		Role:         RoleGuest,
		SubjectName:  nameOr(booking.HostName, missingHostName),
	}
	if actorID == booking.HostID {
		result.Role = RoleHost
		result.SubjectName = nameOr(booking.GuestName, missingGuestName)
	}
	for _, review := range found {
		switch {
		case review.AuthorRole == result.Role:
			result.Mine = &review
		case review.PublishedAt != nil:
			result.Theirs = &review
		}
	}
	if booking.Status == statusCompleted && booking.CompletedAt != nil {
		closes := booking.CompletedAt.Add(ReviewWindow)
		result.WindowClosesAt = &closes
		result.CanReview = result.Mine == nil && service.now().Before(closes)
	}
	return result, nil
}

func (service *ReviewService) ForListing(ctx context.Context, listingID uuid.UUID) (ListingReviews, error) {
	return service.store.ForListing(ctx, listingID, maxListedReviews)
}

func (service *ReviewService) AboutMe(ctx context.Context, actorID uuid.UUID) (GuestReviews, error) {
	return service.store.AboutGuest(ctx, actorID, actorID, maxListedReviews)
}

func (service *ReviewService) AboutBookingGuest(ctx context.Context, hostID, bookingID uuid.UUID) (GuestReviews, error) {
	booking, err := service.store.Booking(ctx, hostID, bookingID)
	if err != nil {
		return GuestReviews{}, err
	}
	if booking.HostID != hostID {
		return GuestReviews{}, ErrBookingNotFound
	}
	result, err := service.store.AboutGuest(ctx, hostID, booking.GuestID, maxListedReviews)
	if err != nil {
		return GuestReviews{}, err
	}
	result.GuestName = nameOr(booking.GuestName, missingGuestName)
	return result, nil
}

func (service *ReviewService) Pending(ctx context.Context, actorID uuid.UUID) ([]PendingReview, error) {
	return service.store.Pending(ctx, actorID, service.now().UTC().Add(-ReviewWindow), maxPendingReviews)
}

func (service *ReviewService) ListingSummary(ctx context.Context, listingID uuid.UUID) (ReviewSummary, error) {
	return service.summary(ctx, uuid.Nil, SummaryOfListing, listingID, func(ctx context.Context) ([]Review, error) {
		found, err := service.store.ForListing(ctx, listingID, maxListedReviews)
		return found.Reviews, err
	})
}

func (service *ReviewService) BookingGuestSummary(ctx context.Context, hostID, bookingID uuid.UUID) (ReviewSummary, error) {
	booking, err := service.store.Booking(ctx, hostID, bookingID)
	if err != nil {
		return ReviewSummary{}, err
	}
	if booking.HostID != hostID {
		return ReviewSummary{}, ErrBookingNotFound
	}
	return service.summary(ctx, hostID, SummaryOfGuest, booking.GuestID, func(ctx context.Context) ([]Review, error) {
		found, err := service.store.AboutGuest(ctx, hostID, booking.GuestID, maxListedReviews)
		return found.Reviews, err
	})
}

func (service *ReviewService) summary(
	ctx context.Context,
	actorID uuid.UUID,
	subject SummarySubject,
	subjectID uuid.UUID,
	load func(context.Context) ([]Review, error),
) (ReviewSummary, error) {
	if service.summaries == nil || service.summarizer == nil {
		return ReviewSummary{}, nil
	}
	state, err := service.summaries.SummaryState(ctx, actorID, subject, subjectID)
	if err != nil {
		return ReviewSummary{}, err
	}
	if state.ReviewCount < MinSummarizedReviews {
		return ReviewSummary{ReviewCount: state.ReviewCount}, nil
	}
	if state.Cached != nil && state.Cached.Fingerprint == state.Fingerprint {
		return shownSummary(*state.Cached), nil
	}
	key := string(subject) + ":" + subjectID.String() + ":" + state.Fingerprint
	fresh, err, _ := service.generating.Do(key, func() (any, error) {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), summaryDeadline)
		defer cancel()
		return service.generate(detached, actorID, subject, subjectID, state, load)
	})
	if err != nil {
		slog.Error("Summarize reviews", "subject", subject, "subject_id", subjectID, "error", err)
		if state.Cached != nil {
			return shownSummary(*state.Cached), nil
		}
		return ReviewSummary{ReviewCount: state.ReviewCount}, nil
	}
	return shownSummary(fresh.(CachedSummary)), nil
}

func (service *ReviewService) generate(
	ctx context.Context,
	actorID uuid.UUID,
	subject SummarySubject,
	subjectID uuid.UUID,
	state SummaryState,
	load func(context.Context) ([]Review, error),
) (CachedSummary, error) {
	found, err := load(ctx)
	if err != nil {
		return CachedSummary{}, fmt.Errorf("load reviews to summarize: %w", err)
	}
	request := SummaryRequest{Subject: subject, Reviews: found}
	if state.Cached != nil {
		request.Current = state.Cached.Summary
		request.CurrentSince = &state.Cached.CheckedAt
	}
	answer, err := service.summarizer.Summarize(ctx, request)
	if err != nil {
		return CachedSummary{}, err
	}
	now := service.now().UTC()
	checked := CachedSummary{Fingerprint: state.Fingerprint, ReviewCount: state.ReviewCount, CheckedAt: now}
	if state.Cached != nil && answer.StillAccurate {
		checked.Summary = state.Cached.Summary
		checked.UpdatedAt = state.Cached.UpdatedAt
	} else {
		text := strings.Join(strings.Fields(answer.Summary), " ")
		if text == "" || utf8.RuneCountInString(text) > MaxSummaryRunes {
			return CachedSummary{}, errUnusableSummary
		}
		checked.Summary = text
		checked.UpdatedAt = now
		if state.Cached != nil && state.Cached.Summary == text {
			checked.UpdatedAt = state.Cached.UpdatedAt
		}
	}
	if err := service.summaries.SaveSummary(ctx, actorID, subject, subjectID, checked); err != nil {
		slog.Error("Save review summary", "subject", subject, "subject_id", subjectID, "error", err)
	}
	return checked, nil
}

func shownSummary(cached CachedSummary) ReviewSummary {
	updatedAt := cached.UpdatedAt
	return ReviewSummary{Summary: cached.Summary, ReviewCount: cached.ReviewCount, UpdatedAt: &updatedAt}
}

func cleanInput(input CreateReviewInput) (CreateReviewInput, error) {
	input.Comment = strings.TrimSpace(input.Comment)
	if input.BookingID == uuid.Nil {
		return input, fmt.Errorf("%w: booking_id is required", ErrInvalidReview)
	}
	if !validRating(input.Rating) {
		return input, fmt.Errorf("%w: rating must be between %d and %d", ErrInvalidReview, minRating, maxRating)
	}
	length := utf8.RuneCountInString(input.Comment)
	if length == 0 || length > maxCommentRunes || !utf8.ValidString(input.Comment) {
		return input, fmt.Errorf("%w: comment must be 1-%d characters", ErrInvalidReview, maxCommentRunes)
	}
	if input.Categories != nil {
		scores := *input.Categories
		for _, score := range []*int{scores.Cleanliness, scores.Accuracy, scores.CheckIn, scores.Communication, scores.Location, scores.Value} {
			if score != nil && !validRating(*score) {
				return input, fmt.Errorf("%w: category scores must be between %d and %d", ErrInvalidReview, minRating, maxRating)
			}
		}
	}
	return input, nil
}

func validRating(rating int) bool {
	return rating >= minRating && rating <= maxRating
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}
