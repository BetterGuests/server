package pms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

const (
	guestyAddress     = "https://open-api.guesty.com"
	guestyPageSize    = 100
	guestyFields      = "_id title nickname publicDescription address propertyType roomType accommodates bedrooms beds bathrooms prices pictures"
	tokenExpiryMargin = 5 * time.Minute
	maxCachedTokens   = 1000
)

type GuestySource struct {
	client  *http.Client
	address string
	now     func() time.Time
	mu      sync.Mutex
	tokens  map[[sha256.Size]byte]cachedToken
}

func NewGuestySource() *GuestySource {
	return &GuestySource{client: newAPIClient(), address: guestyAddress, now: time.Now, tokens: map[[sha256.Size]byte]cachedToken{}}
}

type guestyListing struct {
	ID          string `json:"_id"`
	Title       string `json:"title"`
	Nickname    string `json:"nickname"`
	Description struct {
		Summary      string `json:"summary"`
		Space        string `json:"space"`
		Neighborhood string `json:"neighborhood"`
	} `json:"publicDescription"`
	Address struct {
		Street    string         `json:"street"`
		City      string         `json:"city"`
		State     string         `json:"state"`
		Zipcode   string         `json:"zipcode"`
		Country   string         `json:"country"`
		Latitude  flexibleNumber `json:"lat"`
		Longitude flexibleNumber `json:"lng"`
	} `json:"address"`
	PropertyType string         `json:"propertyType"`
	RoomType     string         `json:"roomType"`
	Accommodates flexibleNumber `json:"accommodates"`
	Bedrooms     flexibleNumber `json:"bedrooms"`
	Beds         flexibleNumber `json:"beds"`
	Bathrooms    flexibleNumber `json:"bathrooms"`
	Prices       struct {
		Base     flexibleNumber `json:"basePrice"`
		Currency string         `json:"currency"`
	} `json:"prices"`
	Pictures []struct {
		Large     string `json:"large"`
		Original  string `json:"original"`
		Regular   string `json:"regular"`
		Thumbnail string `json:"thumbnail"`
	} `json:"pictures"`
}

type guestyPage []guestyListing

func (page *guestyPage) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return json.Unmarshal(data, (*[]guestyListing)(page))
	}
	var wrapped struct {
		Results []guestyListing `json:"results"`
	}
	err := json.Unmarshal(data, &wrapped)
	*page = wrapped.Results
	return err
}

func (source *GuestySource) Properties(ctx context.Context, credentials Credentials) ([]Property, error) {
	token, err := source.token(ctx, credentials)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	var properties []Property
	for len(properties) < maxProperties {
		var page guestyPage
		query := url.Values{
			"limit":  {fmt.Sprint(guestyPageSize)},
			"skip":   {fmt.Sprint(len(properties))},
			"fields": {guestyFields},
		}
		if err := getJSON(ctx, source.client, source.address+"/v1/listings?"+query.Encode(), headers, &page); err != nil {
			return nil, err
		}
		for _, listing := range page {
			properties = append(properties, listing.property())
		}
		if len(page) < guestyPageSize {
			break
		}
	}
	completeEach(ctx, properties, func(ctx context.Context, property *Property) error {
		return source.completeSpaces(ctx, headers, property)
	})
	return properties, nil
}

func (source *GuestySource) completeSpaces(ctx context.Context, headers map[string]string, property *Property) error {
	if property.Beds > 0 && property.Bathrooms > 0 {
		return nil
	}
	var response struct {
		Spaces []struct {
			Beds  map[string]flexibleNumber `json:"beds"`
			Baths map[string]flexibleNumber `json:"bathrooms"`
		} `json:"spaces"`
		Baths map[string]flexibleNumber `json:"bathrooms"`
	}
	address := source.address + "/v1/properties/spaces/unit-type/" + url.PathEscape(property.ID)
	if err := getJSON(ctx, source.client, address, headers, &response); err != nil {
		return err
	}
	beds, baths := 0.0, sumOf(response.Baths)
	for _, space := range response.Spaces {
		beds += sumOf(space.Beds)
		if len(response.Baths) == 0 {
			baths += sumOf(space.Baths)
		}
	}
	if property.Beds < 1 {
		property.Beds = int(beds)
	}
	if property.Bathrooms <= 0 {
		property.Bathrooms = baths
	}
	return nil
}

func sumOf(counts map[string]flexibleNumber) float64 {
	total := 0.0
	for _, count := range counts {
		total += float64(count)
	}
	return total
}

func (source *GuestySource) token(ctx context.Context, credentials Credentials) (string, error) {
	key := sha256.Sum256([]byte(credentials.Account + "\x00" + credentials.Secret))
	source.mu.Lock()
	defer source.mu.Unlock()
	if held, found := source.tokens[key]; found && source.now().Before(held.expires) {
		return held.value, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"scope":         {"open-api"},
		"client_id":     {credentials.Account},
		"client_secret": {credentials.Secret},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.address+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token tokenResponse
	if err := fetchToken(source.client, request, &token); err != nil {
		return "", err
	}
	if len(source.tokens) >= maxCachedTokens {
		clear(source.tokens)
	}
	lifetime := time.Duration(token.ExpiresIn)*time.Second - tokenExpiryMargin
	source.tokens[key] = cachedToken{value: token.AccessToken, expires: source.now().Add(lifetime)}
	return token.AccessToken, nil
}

var guestyRoomTypes = map[string]listings.ListingType{
	"Entire home/apt": listings.EntirePlace,
	"Private room":    listings.PrivateRoom,
	"Shared room":     listings.SharedRoom,
}

func (listing guestyListing) property() Property {
	photos := []string{}
	for _, picture := range listing.Pictures {
		if address := firstFilled(picture.Large, picture.Original, picture.Regular, picture.Thumbnail); address != "" {
			photos = append(photos, address)
		}
	}
	paragraphs := []string{}
	for _, paragraph := range []string{listing.Description.Summary, listing.Description.Space, listing.Description.Neighborhood} {
		if strings.TrimSpace(paragraph) != "" {
			paragraphs = append(paragraphs, paragraph)
		}
	}
	return Property{
		ID:          listing.ID,
		Title:       firstFilled(listing.Title, listing.Nickname),
		Description: strings.Join(paragraphs, "\n\n"),
		Address: listings.Address{
			Line1: listing.Address.Street, City: listing.Address.City, Region: listing.Address.State,
			PostalCode: listing.Address.Zipcode, CountryCode: countryCode(listing.Address.Country),
			Latitude: float64(listing.Address.Latitude), Longitude: float64(listing.Address.Longitude),
		},
		Type:         guestyRoomTypes[listing.RoomType],
		PropertyType: propertyTypeNamed(listing.PropertyType),
		MaxGuests:    int(listing.Accommodates),
		Bedrooms:     int(listing.Bedrooms),
		Beds:         int(listing.Beds),
		Bathrooms:    float64(listing.Bathrooms),
		NightlyCents: cents(listing.Prices.Base),
		Currency:     listing.Prices.Currency,
		PhotoURLs:    photos,
	}
}
