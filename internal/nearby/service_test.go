package nearby

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type locationStub struct {
	point Point
	err   error
}

func (stub locationStub) Location(context.Context, uuid.UUID) (Point, error) {
	return stub.point, stub.err
}

type sourceStub struct {
	places   []Place
	err      error
	calls    atomic.Int32
	category Category
	origin   Point
	gate     chan struct{}
}

func (stub *sourceStub) Find(_ context.Context, category Category, origin Point) ([]Place, error) {
	stub.calls.Add(1)
	stub.category = category
	stub.origin = origin
	if stub.gate != nil {
		<-stub.gate
	}
	return stub.places, stub.err
}

var home = Point{Latitude: 20.7, Longitude: -156.45}

func placeAt(id string, latitude, longitude float64) Place {
	return Place{ID: id, Name: id, Latitude: latitude, Longitude: longitude}
}

func newService(source *sourceStub) *NearbyService {
	return NewNearbyService(locationStub{point: home}, source)
}

func TestSearchRanksPlacesNearestFirstWithDistances(t *testing.T) {
	source := &sourceStub{places: []Place{
		placeAt("far", 20.9, -156.45),
		placeAt("near", 20.71, -156.45),
		placeAt("middle", 20.78, -156.45),
	}}

	places, err := newService(source).Search(context.Background(), uuid.New(), "drinks")
	if err != nil {
		t.Fatal(err)
	}

	order := []string{places[0].ID, places[1].ID, places[2].ID}
	want := []string{"near", "middle", "far"}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if places[0].DistanceKm <= 0 || places[0].DistanceKm >= places[1].DistanceKm {
		t.Fatalf("distances not filled in ascending: %v", places)
	}
}

func TestSearchLimitsTheNumberOfPlaces(t *testing.T) {
	var found []Place
	for index := range maxPlaces + 10 {
		found = append(found, placeAt(uuid.NewString(), 20.7+float64(index)/1000, -156.45))
	}

	places, err := newService(&sourceStub{places: found}).Search(context.Background(), uuid.New(), "drinks")
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != maxPlaces {
		t.Fatalf("got %d places, want %d", len(places), maxPlaces)
	}
}

func TestSearchAsksTheSourceAroundTheListingLocation(t *testing.T) {
	source := &sourceStub{}

	if _, err := newService(source).Search(context.Background(), uuid.New(), "coffee"); err != nil {
		t.Fatal(err)
	}

	if source.category.ID != "coffee" {
		t.Fatalf("category = %q", source.category.ID)
	}
	if source.origin != home {
		t.Fatalf("origin = %v, want %v", source.origin, home)
	}
}

func TestSearchRemembersAnswersPerListingAndCategory(t *testing.T) {
	source := &sourceStub{places: []Place{placeAt("a", 20.71, -156.45)}}
	service := newService(source)
	listing := uuid.New()

	for range 3 {
		if _, err := service.Search(context.Background(), listing, "drinks"); err != nil {
			t.Fatal(err)
		}
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("same listing and category made %d upstream calls, want 1", calls)
	}

	if _, err := service.Search(context.Background(), listing, "coffee"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Search(context.Background(), uuid.New(), "drinks"); err != nil {
		t.Fatal(err)
	}
	if calls := source.calls.Load(); calls != 3 {
		t.Fatalf("a new category and a new listing made %d upstream calls in total, want 3", calls)
	}
}

func TestSearchAsksAgainOnceTheCacheHasExpired(t *testing.T) {
	source := &sourceStub{}
	service := newService(source)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	listing := uuid.New()

	if _, err := service.Search(context.Background(), listing, "drinks"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(cacheLifetime - time.Minute)
	if _, err := service.Search(context.Background(), listing, "drinks"); err != nil {
		t.Fatal(err)
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("calls before expiry = %d, want 1", calls)
	}

	clock = clock.Add(2 * time.Minute)
	if _, err := service.Search(context.Background(), listing, "drinks"); err != nil {
		t.Fatal(err)
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("calls after expiry = %d, want 2", calls)
	}
}

func TestSearchDoesNotRememberFailures(t *testing.T) {
	source := &sourceStub{err: ErrSourceUnavailable}
	service := newService(source)
	listing := uuid.New()

	for range 2 {
		if _, err := service.Search(context.Background(), listing, "drinks"); !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("err = %v, want ErrSourceUnavailable", err)
		}
	}
	if calls := source.calls.Load(); calls != 2 {
		t.Fatalf("failed lookups made %d upstream calls, want 2", calls)
	}
}

func TestSearchRejectsAnUnknownCategoryWithoutCallingOut(t *testing.T) {
	source := &sourceStub{}

	_, err := newService(source).Search(context.Background(), uuid.New(), "'; drop table")
	if !errors.Is(err, ErrUnknownCategory) {
		t.Fatalf("err = %v, want ErrUnknownCategory", err)
	}
	if source.calls.Load() != 0 {
		t.Fatal("source was called for an unknown category")
	}
}

func TestSearchReportsAMissingListing(t *testing.T) {
	source := &sourceStub{}
	service := NewNearbyService(locationStub{err: ErrListingNotFound}, source)

	if _, err := service.Search(context.Background(), uuid.New(), "drinks"); !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("err = %v, want ErrListingNotFound", err)
	}
	if source.calls.Load() != 0 {
		t.Fatal("source was called for a missing listing")
	}
}

func TestSimultaneousSearchesShareOneUpstreamCall(t *testing.T) {
	source := &sourceStub{gate: make(chan struct{})}
	service := newService(source)
	listing := uuid.New()

	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := service.Search(context.Background(), listing, "drinks"); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(source.gate)
	group.Wait()

	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("simultaneous searches made %d upstream calls, want 1", calls)
	}
}

func TestEveryAdvertisedCategoryExists(t *testing.T) {
	for _, id := range []string{"drinks", "liquor", "coffee", "food", "groceries", "pharmacy", "gas"} {
		category, ok := categories[id]
		if !ok || len(category.Keys) == 0 || category.ID != id {
			t.Errorf("category %q is missing, mislabelled or has no keys", id)
		}
	}
}

func TestConfigFromEnvReadsTheAPIKey(t *testing.T) {
	t.Setenv("GEOAPIFY_API_KEY", "")
	config := ConfigFromEnv()
	if config.Configured() {
		t.Fatal("an empty key must not count as configured")
	}
	if config.BaseURL != defaultGeoapifyURL || config.UserAgent != defaultUserAgent {
		t.Fatalf("defaults = %+v", config)
	}

	t.Setenv("GEOAPIFY_API_KEY", "  abc123  ")
	config = ConfigFromEnv()
	if !config.Configured() || config.APIKey != "abc123" {
		t.Fatalf("config = %+v", config)
	}
}
