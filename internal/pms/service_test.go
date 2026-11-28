package pms

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
)

type sourceStub struct {
	properties []Property
	err        error
}

func (stub sourceStub) Properties(context.Context, Credentials) ([]Property, error) {
	return stub.properties, stub.err
}

type ownedStub []listings.HostListing

func (stub ownedStub) ListForHost(context.Context, uuid.UUID) ([]listings.HostListing, error) {
	return stub, nil
}

type writerStub struct {
	created []listings.CreateListingInput
	prices  []int
	photos  []string
	refuse  error
}

func (stub *writerStub) CreateListing(_ context.Context, _ uuid.UUID, input listings.CreateListingInput) (listings.Listing, error) {
	if stub.refuse != nil {
		return listings.Listing{}, stub.refuse
	}
	stub.created = append(stub.created, input)
	return listings.Listing{ID: uuid.New()}, nil
}

func (stub *writerStub) SetPrice(_ context.Context, _, _ uuid.UUID, cents int) (listings.Setup, error) {
	stub.prices = append(stub.prices, cents)
	return listings.Setup{}, nil
}

func (stub *writerStub) ImportPhoto(_ context.Context, _, _ uuid.UUID, _ string, data []byte) error {
	stub.photos = append(stub.photos, string(data))
	return nil
}

type fetcherStub struct{}

func (fetcherStub) Fetch(_ context.Context, address string) ([]byte, string, error) {
	if strings.HasSuffix(address, "broken") {
		return nil, "", errors.New("gone")
	}
	return []byte(address), "image/jpeg", nil
}

func completeProperty(id, title string) Property {
	return Property{
		ID: id, Title: title, Description: "A quiet cabin with a wide porch and a fire pit.",
		Address:      listings.Address{Line1: "1 Pine Road", City: "Bend", CountryCode: "us", Latitude: 44.05, Longitude: -121.31},
		PropertyType: listings.Cabin, MaxGuests: 4, Bedrooms: 2, Beds: 3, Bathrooms: 1.5,
		NightlyCents: 18500, Currency: "usd", PhotoURLs: []string{"https://photos.test/a", "https://photos.test/broken", "https://photos.test/c"},
	}
}

func serviceWith(properties []Property, owned ownedStub, writer *writerStub) *ImportService {
	service := NewImportService(map[ProviderName]Source{Lodgify: sourceStub{properties: properties}}, owned, writer, fetcherStub{})
	service.inBackground = func(work func()) { work() }
	return service
}

var key = Credentials{Secret: "key"}

func TestImportCreatesADraftWithPriceAndKeepsGoingPastABrokenPhoto(t *testing.T) {
	writer := &writerStub{}
	service := serviceWith([]Property{completeProperty("1", "Pine cabin")}, nil, writer)

	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: []string{"1"}})

	if err != nil || len(outcomes) != 1 || outcomes[0].State != Imported || !outcomes[0].PriceSet || outcomes[0].PhotosQueued != 3 {
		t.Fatalf("outcomes %+v, error %v", outcomes, err)
	}
	if len(writer.created) != 1 || writer.created[0].Address.CountryCode != "US" || writer.created[0].MinimumAgeToBook != 18 {
		t.Fatalf("created %+v", writer.created)
	}
	if strings.Join(writer.photos, ",") != "https://photos.test/a,https://photos.test/c" {
		t.Fatalf("photos %v", writer.photos)
	}
}

func TestImportSkipsPlacesTheHostAlreadyHasEvenWithDifferentSpacingAndCase(t *testing.T) {
	writer := &writerStub{}
	service := serviceWith([]Property{completeProperty("1", "Pine  Cabin")}, ownedStub{{Title: "pine cabin", City: "bend"}}, writer)

	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: []string{"1"}})

	if err != nil || outcomes[0].State != AlreadyAdded || len(writer.created) != 0 {
		t.Fatalf("outcomes %+v, error %v", outcomes, err)
	}
}

func TestTwoIdenticalPlacesInOneImportBecomeOneDraft(t *testing.T) {
	writer := &writerStub{}
	service := serviceWith([]Property{completeProperty("1", "Pine cabin"), completeProperty("2", "Pine cabin")}, nil, writer)

	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: []string{"1", "2"}})

	if err != nil || len(writer.created) != 1 || outcomes[1].State != AlreadyAdded {
		t.Fatalf("outcomes %+v, error %v", outcomes, err)
	}
}

func TestAPlaceWithGapsIsNamedForTheHostAndNotInvented(t *testing.T) {
	property := completeProperty("1", "Pine cabin")
	property.Beds = 0
	property.Address.Latitude, property.Address.Longitude = 0, 0
	writer := &writerStub{}
	service := serviceWith([]Property{property}, nil, writer)

	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: []string{"1"}})

	if err != nil || outcomes[0].State != NeedsDetails || outcomes[0].Message != "Missing a map location, the bed count" || len(writer.created) != 0 {
		t.Fatalf("outcomes %+v, error %v", outcomes, err)
	}
}

func TestAPriceInAnotherCurrencyIsLeftForTheHost(t *testing.T) {
	property := completeProperty("1", "Pine cabin")
	property.Currency = "EUR"
	writer := &writerStub{}
	service := serviceWith([]Property{property}, nil, writer)

	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: []string{"1"}})

	if err != nil || outcomes[0].PriceSet || len(writer.prices) != 0 {
		t.Fatalf("outcomes %+v, error %v", outcomes, err)
	}
}

func TestPreviewTurnsMarkupIntoPlainTextWithoutEatingLessThanSigns(t *testing.T) {
	property := completeProperty("1", "Pine cabin")
	property.Description = "<p>Sleeps &lt; 5 guests.</p><p>Walk 3 < 4 minutes to the river.</p>"
	service := serviceWith([]Property{property}, nil, &writerStub{})

	candidates, err := service.Preview(context.Background(), uuid.New(), Lodgify, key)

	want := "Sleeps < 5 guests.\nWalk 3 < 4 minutes to the river."
	if err != nil || candidates[0].Draft.Description != want {
		t.Fatalf("description %q, error %v", candidates[0].Draft.Description, err)
	}
}

func TestLongTextIsCutOnAWholeCharacter(t *testing.T) {
	if got := clip(strings.Repeat("é", 100), 121); got != strings.Repeat("é", 60) {
		t.Fatalf("got %d bytes", len(got))
	}
}

func TestConnectingNeedsEveryFieldTheSystemAsksFor(t *testing.T) {
	service := NewImportService(map[ProviderName]Source{Guesty: sourceStub{}}, ownedStub{}, &writerStub{}, fetcherStub{})

	_, err := service.Preview(context.Background(), uuid.New(), Guesty, key)
	if !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("error %v", err)
	}
	_, err = service.Preview(context.Background(), uuid.New(), "other", key)
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("error %v", err)
	}
}

func TestTheHostCanFillOnlyTheGapsTheSystemLeft(t *testing.T) {
	property := completeProperty("1", "Pine cabin")
	property.PropertyType, property.Beds = "", 0
	writer := &writerStub{}
	service := serviceWith([]Property{property}, nil, writer)

	candidates, err := service.Preview(context.Background(), uuid.New(), Lodgify, key)
	if err != nil || candidates[0].State != NeedsDetails || !candidates[0].Askable {
		t.Fatalf("candidates %+v, error %v", candidates, err)
	}
	outcomes, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{
		PropertyIDs: []string{"1"},
		Answers:     map[string]Answer{"1": {PropertyType: listings.House, Beds: 5, Bathrooms: 9}},
	})
	if err != nil || outcomes[0].State != Imported || writer.created[0].Beds != 5 || writer.created[0].Bathrooms != 1.5 || writer.created[0].PropertyType != listings.House {
		t.Fatalf("outcomes %+v, created %+v, error %v", outcomes, writer.created, err)
	}
}

func TestImportRefusesMoreThanOneBatch(t *testing.T) {
	service := serviceWith(nil, nil, &writerStub{})

	_, err := service.Import(context.Background(), uuid.New(), Lodgify, key, Selection{PropertyIDs: make([]string, maxPropertiesPerBatch+1)})

	if !errors.Is(err, ErrTooManySelected) {
		t.Fatalf("error %v", err)
	}
}

func TestPhotosAreOnlyFetchedOverHTTPS(t *testing.T) {
	for _, address := range []string{"http://photos.test/a.jpg", "file:///etc/passwd", "not a url"} {
		if _, _, err := NewWebPhotoFetcher(http.DefaultClient).Fetch(context.Background(), address); !errors.Is(err, errPhotoRefused) {
			t.Errorf("%s: error %v", address, err)
		}
	}
}
