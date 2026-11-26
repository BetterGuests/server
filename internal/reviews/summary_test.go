package reviews

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type summaryStoreStub struct {
	state SummaryState
	saved []CachedSummary
}

func (stub *summaryStoreStub) SummaryState(context.Context, uuid.UUID, SummarySubject, uuid.UUID) (SummaryState, error) {
	return stub.state, nil
}

func (stub *summaryStoreStub) SaveSummary(_ context.Context, _ uuid.UUID, _ SummarySubject, _ uuid.UUID, summary CachedSummary) error {
	stub.saved = append(stub.saved, summary)
	stub.state.Cached = &summary
	return nil
}

func (stub *summaryStoreStub) addReview(fingerprint string) {
	stub.state.Fingerprint = fingerprint
	stub.state.ReviewCount++
}

type summarizerStub struct {
	answer        string
	stillAccurate bool
	err           error
	calls         int
	subjects      []SummarySubject
	requests      []SummaryRequest
}

func (stub *summarizerStub) Summarize(_ context.Context, request SummaryRequest) (SummaryAnswer, error) {
	stub.calls++
	stub.subjects = append(stub.subjects, request.Subject)
	stub.requests = append(stub.requests, request)
	return SummaryAnswer{Summary: stub.answer, StillAccurate: stub.stillAccurate && request.Current != ""}, stub.err
}

type listingReviewsStub struct {
	reviewStoreStub
}

func (stub *listingReviewsStub) ForListing(context.Context, uuid.UUID, int) (ListingReviews, error) {
	return ListingReviews{Reviews: []Review{{Rating: 4, Comment: "Nice"}}}, nil
}

func (stub *listingReviewsStub) AboutGuest(context.Context, uuid.UUID, uuid.UUID, int) (GuestReviews, error) {
	return GuestReviews{Reviews: []Review{{Rating: 4, Comment: "Tidy"}}}, nil
}

func summaryService(reviewCount int) (*ReviewService, *summaryStoreStub, *summarizerStub) {
	cache := &summaryStoreStub{state: SummaryState{Fingerprint: "first", ReviewCount: reviewCount}}
	summarizer := &summarizerStub{answer: "  The house was great,\n but the circuit breaker kept tripping. "}
	service := NewReviewService(&listingReviewsStub{}).WithSummaries(cache, summarizer)
	return service, cache, summarizer
}

func TestListingSummaryIsHiddenBelowTheMinimum(t *testing.T) {
	for _, count := range []int{0, 1, MinSummarizedReviews - 1} {
		service, cache, summarizer := summaryService(count)
		summary, err := service.ListingSummary(context.Background(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if summary.Summary != "" || summary.ReviewCount != count {
			t.Fatalf("%d reviews showed %+v", count, summary)
		}
		if summarizer.calls != 0 || len(cache.saved) != 0 {
			t.Fatalf("%d reviews called the model %d times", count, summarizer.calls)
		}
	}
}

func TestListingSummaryIsGeneratedOnceAndServedFromTheCache(t *testing.T) {
	service, cache, summarizer := summaryService(MinSummarizedReviews)
	for range 3 {
		summary, err := service.ListingSummary(context.Background(), uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if summary.Summary != "The house was great, but the circuit breaker kept tripping." || summary.UpdatedAt == nil {
			t.Fatalf("unexpected summary %+v", summary)
		}
	}
	if summarizer.calls != 1 || len(cache.saved) != 1 || cache.saved[0].Fingerprint != "first" {
		t.Fatalf("model called %d times, saved %+v", summarizer.calls, cache.saved)
	}
	if summarizer.subjects[0] != SummaryOfListing {
		t.Fatalf("summarized as %q", summarizer.subjects[0])
	}
}

func clockAt(service *ReviewService, moment time.Time) {
	service.now = func() time.Time { return moment }
}

func TestANewReviewThatChangesThePictureUpdatesTheSummary(t *testing.T) {
	service, cache, summarizer := summaryService(MinSummarizedReviews)
	firstWritten := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	clockAt(service, firstWritten)
	if _, err := service.ListingSummary(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	cache.addReview("second")
	clockAt(service, firstWritten.Add(24*time.Hour))
	summarizer.answer = "Clean and quiet, but the hot water ran out."
	summary, err := service.ListingSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if summarizer.calls != 2 || summary.Summary != "Clean and quiet, but the hot water ran out." {
		t.Fatalf("model called %d times, summary %+v", summarizer.calls, summary)
	}
	if summary.ReviewCount != MinSummarizedReviews+1 || cache.saved[1].Fingerprint != "second" {
		t.Fatalf("cache kept %+v", cache.saved)
	}
	if !summary.UpdatedAt.Equal(firstWritten.Add(24 * time.Hour)) {
		t.Fatalf("updated_at = %v", summary.UpdatedAt)
	}
	asked := summarizer.requests[1]
	if asked.Current != "The house was great, but the circuit breaker kept tripping." || asked.CurrentSince == nil || !asked.CurrentSince.Equal(firstWritten) {
		t.Fatalf("model was not shown the current summary: %+v", asked)
	}
}

func TestANewReviewThatFitsTheSummaryKeepsItAsItIs(t *testing.T) {
	service, cache, summarizer := summaryService(MinSummarizedReviews)
	firstWritten := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	clockAt(service, firstWritten)
	first, err := service.ListingSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	cache.addReview("second")
	clockAt(service, firstWritten.Add(24*time.Hour))
	summarizer.answer = "A rewrite the model was told to skip."
	summarizer.stillAccurate = true
	kept, err := service.ListingSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if summarizer.calls != 2 || kept.Summary != first.Summary || !kept.UpdatedAt.Equal(firstWritten) {
		t.Fatalf("summary changed to %+v", kept)
	}
	saved := cache.saved[1]
	if saved.Fingerprint != "second" || !saved.CheckedAt.Equal(firstWritten.Add(24*time.Hour)) || saved.ReviewCount != MinSummarizedReviews+1 {
		t.Fatalf("the check was not recorded: %+v", saved)
	}
	if _, err := service.ListingSummary(context.Background(), uuid.New()); err != nil || summarizer.calls != 2 {
		t.Fatalf("checked the same reviews again: %d calls, %v", summarizer.calls, err)
	}
}

func TestTheFirstSummaryIsWrittenWithoutAPreviousOne(t *testing.T) {
	service, _, summarizer := summaryService(MinSummarizedReviews)
	if _, err := service.ListingSummary(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if asked := summarizer.requests[0]; asked.Current != "" || asked.CurrentSince != nil {
		t.Fatalf("first request carried a previous summary: %+v", asked)
	}
}

func TestAFailedRegenerationKeepsTheOlderSummary(t *testing.T) {
	service, cache, summarizer := summaryService(MinSummarizedReviews)
	if _, err := service.ListingSummary(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	cache.addReview("second")
	summarizer.err = errors.New("model unavailable")
	summary, err := service.ListingSummary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Summary != "The house was great, but the circuit breaker kept tripping." {
		t.Fatalf("lost the older summary: %+v", summary)
	}
}

func TestAFailedFirstSummaryIsHiddenNotAnError(t *testing.T) {
	cases := map[string]*summarizerStub{
		"model error":   {err: errors.New("model unavailable")},
		"empty answer":  {answer: "   "},
		"overlong text": {answer: strings.Repeat("word ", MaxSummaryRunes)},
	}
	for name, summarizer := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &summaryStoreStub{state: SummaryState{Fingerprint: "first", ReviewCount: MinSummarizedReviews}}
			service := NewReviewService(&listingReviewsStub{}).WithSummaries(cache, summarizer)
			summary, err := service.ListingSummary(context.Background(), uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			if summary.Summary != "" || len(cache.saved) != 0 {
				t.Fatalf("showed %+v, saved %+v", summary, cache.saved)
			}
		})
	}
}

func TestSummariesAreHiddenWhenTheModelIsNotConfigured(t *testing.T) {
	summary, err := NewReviewService(&listingReviewsStub{}).ListingSummary(context.Background(), uuid.New())
	if err != nil || summary.Summary != "" {
		t.Fatalf("summary %+v, error %v", summary, err)
	}
}

func TestGuestSummaryIsOnlyForTheBookingsHost(t *testing.T) {
	finished := time.Now().Add(-time.Hour)
	store := &listingReviewsStub{reviewStoreStub: *completedStay(finished)}
	cache := &summaryStoreStub{state: SummaryState{Fingerprint: "first", ReviewCount: MinSummarizedReviews}}
	summarizer := &summarizerStub{answer: "Leaves the place a bit messy, but quiet and respectful of house rules."}
	service := NewReviewService(store).WithSummaries(cache, summarizer)

	if _, err := service.BookingGuestSummary(context.Background(), store.booking.GuestID, store.booking.BookingID); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("the guest read their own host summary: %v", err)
	}
	summary, err := service.BookingGuestSummary(context.Background(), store.booking.HostID, store.booking.BookingID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Summary == "" || summarizer.subjects[0] != SummaryOfGuest {
		t.Fatalf("summary %+v, subjects %v", summary, summarizer.subjects)
	}
}
