package pms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

func apiAnswering(t *testing.T, answers map[string]string, seen *[]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = append(*seen, r.Method+" "+r.URL.Path)
		}
		body, known := answers[r.URL.Path]
		if !known {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestHostawayListingsAreReadWithPhotosInTheirSortOrder(t *testing.T) {
	server := apiAnswering(t, map[string]string{
		"/v1/accessTokens":  `{"token_type":"Bearer","expires_in":100,"access_token":"t"}`,
		"/v1/propertyTypes": `{"status":"success","result":[{"id":1,"name":"Apartment"}]}`,
		"/v1/listings": `{"status":"success","result":[{"id":42,"name":"internal","externalListingName":"Harbour flat","description":"Two rooms above the harbour.",
			"street":"2 Quay","city":"Portland","state":"ME","zipcode":"04101","countryCode":"US","lat":43.6,"lng":"-70.2","propertyTypeId":1,"roomType":"entire_home",
			"personCapacity":4,"bedroomsNumber":2,"bedsNumber":2,"bathroomsNumber":1,"price":149.5,"currencyCode":"USD",
			"listingImages":[{"url":"https://img/2","sortOrder":2},{"url":"https://img/1","sortOrder":1}]}]}`,
	}, nil)
	source := NewHostawaySource()
	source.address, source.tokenSettleTime = server.URL, 0

	properties, err := source.Properties(context.Background(), Credentials{Account: "1", Secret: "k"})

	if err != nil || len(properties) != 1 {
		t.Fatalf("properties %+v, error %v", properties, err)
	}
	got := properties[0]
	if got.ID != "42" || got.Title != "Harbour flat" || got.PropertyType != listings.Apartment || got.NightlyCents != 14950 || got.Address.Longitude != -70.2 || got.PhotoURLs[0] != "https://img/1" {
		t.Fatalf("property %+v", got)
	}
}

func TestGuestyReusesItsTokenBecauseOnlyFiveAreAllowedADay(t *testing.T) {
	seen := []string{}
	server := apiAnswering(t, map[string]string{
		"/oauth2/token": `{"token_type":"Bearer","expires_in":86400,"access_token":"t"}`,
		"/v1/listings": `{"results":[{"_id":"a","title":"Loft","publicDescription":{"summary":"Bright loft by the station."},
			"address":{"street":"5 Rail St","city":"Leeds","country":"United Kingdom","lat":53.8,"lng":-1.5},
			"propertyType":"Loft","roomType":"Entire home/apt","accommodates":2,"bedrooms":1,"prices":{"basePrice":90,"currency":"GBP"},
			"pictures":[{"original":"https://img/o","large":"https://img/l"}]}]}`,
		"/v1/properties/spaces/unit-type/a": `{"spaces":[{"type":"BEDROOM","beds":{"QUEEN_BED":1,"SOFA_BED":1}}],"bathrooms":{"PRIVATE":1}}`,
	}, &seen)
	source := NewGuestySource()
	source.address = server.URL
	credentials := Credentials{Account: "id", Secret: "secret"}

	for range 2 {
		properties, err := source.Properties(context.Background(), credentials)
		if err != nil || properties[0].Beds != 2 || properties[0].Bathrooms != 1 || properties[0].Address.CountryCode != "GB" || properties[0].PhotoURLs[0] != "https://img/l" {
			t.Fatalf("properties %+v, error %v", properties, err)
		}
	}
	tokens := 0
	for _, request := range seen {
		if request == "POST /oauth2/token" {
			tokens++
		}
	}
	if tokens != 1 {
		t.Fatalf("asked for %d tokens", tokens)
	}
}

func TestGuestyAcceptsABareArrayOfListings(t *testing.T) {
	var page guestyPage
	if err := page.UnmarshalJSON([]byte(`[{"_id":"a"}]`)); err != nil || len(page) != 1 {
		t.Fatalf("page %+v, error %v", page, err)
	}
}

func TestLodgifyRoomDetailsFillASingleUnitProperty(t *testing.T) {
	server := apiAnswering(t, map[string]string{
		"/v2/properties":         `{"items":[{"id":7,"name":"Dune house","city":"Cannon Beach","country_code":"us","image_url":"//cdn/a.jpg"}]}`,
		"/v2/properties/7/rooms": `[{"max_people":6,"bedrooms":3,"bathrooms":2,"images":[{"url":"//cdn/a.jpg"},{"url":"//cdn/b.jpg"}]}]`,
	}, nil)
	source := NewLodgifySource()
	source.address = server.URL

	properties, err := source.Properties(context.Background(), Credentials{Secret: "k"})

	if err != nil || properties[0].MaxGuests != 6 || len(properties[0].PhotoURLs) != 2 || properties[0].PhotoURLs[1] != "https://cdn/b.jpg" {
		t.Fatalf("properties %+v, error %v", properties, err)
	}
}

func TestSeveralLodgifyRoomTypesAreNotAddedUpIntoOneHome(t *testing.T) {
	property := Property{}
	addRooms(&property, []lodgifyRoom{{MaxPeople: 2}, {MaxPeople: 4}})
	if property.MaxGuests != 0 {
		t.Fatalf("guests %d", property.MaxGuests)
	}
}

func TestARefusedKeyIsReportedAsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	source := NewLodgifySource()
	source.address = server.URL

	_, err := source.Properties(context.Background(), Credentials{Secret: "wrong"})

	if !errors.Is(err, ErrCredentialsRefused) {
		t.Fatalf("error %v", err)
	}
}

func TestAnAPIKeyIsNeverCarriedAlongARedirect(t *testing.T) {
	followed := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	t.Cleanup(elsewhere.Close)
	server := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusFound))
	t.Cleanup(server.Close)
	source := NewLodgifySource()
	source.address = server.URL

	_, err := source.Properties(context.Background(), Credentials{Secret: "k"})

	if !errors.Is(err, ErrProviderFailed) || followed {
		t.Fatalf("followed %v, error %v", followed, err)
	}
}

func TestCountryNamesBecomeTwoLetterCodes(t *testing.T) {
	for given, want := range map[string]string{"United States": "US", "usa": "US", "Israel": "IL", "uk": "GB", "de": "DE", "Narnia": ""} {
		if got := countryCode(given); got != want {
			t.Errorf("%q became %q, want %q", given, got, want)
		}
	}
}
