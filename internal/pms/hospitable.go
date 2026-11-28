package pms

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

const (
	hospitableAddress     = "https://public.api.hospitable.com"
	hospitableAuthAddress = "https://auth.hospitable.com"
	hospitablePageSize    = 100
)

type HospitableSource struct {
	client       *http.Client
	address      string
	authAddress  string
	clientID     string
	clientSecret string
}

func NewHospitableSource(clientID, clientSecret string) *HospitableSource {
	return &HospitableSource{client: newAPIClient(), address: hospitableAddress, authAddress: hospitableAuthAddress, clientID: clientID, clientSecret: clientSecret}
}

func (source *HospitableSource) AuthorizeURL(state string) string {
	query := url.Values{"client_id": {source.clientID}, "response_type": {"code"}, "state": {state}}
	return source.authAddress + "/oauth/authorize?" + query.Encode()
}

func (source *HospitableSource) Exchange(ctx context.Context, code string) (string, error) {
	body, err := json.Marshal(map[string]string{
		"client_id":     source.clientID,
		"client_secret": source.clientSecret,
		"grant_type":    "authorization_code",
		"code":          code,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.authAddress+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	request.Header.Set("Content-Type", "application/json")
	var token tokenResponse
	if err := fetchToken(source.client, request, &token); err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

type hospitableProperty struct {
	ID          flexibleID `json:"id"`
	Name        string     `json:"name"`
	PublicName  string     `json:"public_name"`
	Summary     string     `json:"summary"`
	Description string     `json:"description"`
	Picture     string     `json:"picture"`
	Address     struct {
		Number      flexibleID `json:"number"`
		Street      string     `json:"street"`
		City        string     `json:"city"`
		State       string     `json:"state"`
		Postcode    flexibleID `json:"postcode"`
		Country     string     `json:"country"`
		CountryName string     `json:"country_name"`
		Coordinates struct {
			Latitude  flexibleNumber `json:"latitude"`
			Longitude flexibleNumber `json:"longitude"`
		} `json:"coordinates"`
	} `json:"address"`
	PropertyType string `json:"property_type"`
	RoomType     string `json:"room_type"`
	Capacity     struct {
		Max       flexibleNumber `json:"max"`
		Bedrooms  flexibleNumber `json:"bedrooms"`
		Beds      flexibleNumber `json:"beds"`
		Bathrooms flexibleNumber `json:"bathrooms"`
	} `json:"capacity"`
}

func (source *HospitableSource) Properties(ctx context.Context, credentials Credentials) ([]Property, error) {
	headers := map[string]string{"Authorization": "Bearer " + credentials.Secret}
	var properties []Property
	for page := 1; len(properties) < maxProperties; page++ {
		var response struct {
			Data []hospitableProperty `json:"data"`
		}
		address := fmt.Sprintf("%s/v2/properties?page=%d&per_page=%d&include=details", source.address, page, hospitablePageSize)
		if err := getJSON(ctx, source.client, address, headers, &response); err != nil {
			return nil, err
		}
		for _, found := range response.Data {
			properties = append(properties, found.property())
		}
		if len(response.Data) < hospitablePageSize {
			break
		}
	}
	completeEach(ctx, properties, func(ctx context.Context, property *Property) error {
		source.addPrice(ctx, headers, property)
		source.addPhotos(ctx, headers, property)
		return nil
	})
	return properties, nil
}

func (source *HospitableSource) addPrice(ctx context.Context, headers map[string]string, property *Property) {
	var response struct {
		Data struct {
			Currency string `json:"currency"`
			Base     struct {
				Amount flexibleNumber `json:"amount"`
			} `json:"base_nightly_price"`
		} `json:"data"`
	}
	address := source.address + "/v2/properties/" + url.PathEscape(property.ID) + "/pricing"
	if err := getJSON(ctx, source.client, address, headers, &response); err != nil {
		return
	}
	property.NightlyCents = int(response.Data.Base.Amount)
	property.Currency = response.Data.Currency
}

func (source *HospitableSource) addPhotos(ctx context.Context, headers map[string]string, property *Property) {
	type image struct {
		URL   string         `json:"url"`
		Order flexibleNumber `json:"order"`
	}
	var response struct {
		Data []image `json:"data"`
	}
	address := source.address + "/v2/properties/" + url.PathEscape(property.ID) + "/images"
	if err := getJSON(ctx, source.client, address, headers, &response); err != nil {
		return
	}
	slices.SortStableFunc(response.Data, func(a, b image) int { return cmp.Compare(a.Order, b.Order) })
	photos := []string{}
	for _, found := range response.Data {
		if found.URL != "" {
			photos = append(photos, found.URL)
		}
	}
	if len(photos) > 0 {
		property.PhotoURLs = photos
	}
}

var hospitableRoomTypes = map[string]listings.ListingType{
	"entire_home":  listings.EntirePlace,
	"private_room": listings.PrivateRoom,
	"shared_room":  listings.SharedRoom,
}

func (found hospitableProperty) property() Property {
	photos := []string{}
	if found.Picture != "" {
		photos = append(photos, found.Picture)
	}
	line1, line2 := streetLines(string(found.Address.Number), found.Address.Street)
	return Property{
		ID:          string(found.ID),
		Title:       firstFilled(found.PublicName, found.Name),
		Description: firstFilled(found.Description, found.Summary),
		Address: listings.Address{
			Line1: line1, Line2: line2, City: found.Address.City, Region: found.Address.State,
			PostalCode:  string(found.Address.Postcode),
			CountryCode: countryCode(firstFilled(found.Address.Country, found.Address.CountryName)),
			Latitude:    float64(found.Address.Coordinates.Latitude), Longitude: float64(found.Address.Coordinates.Longitude),
		},
		Type:         hospitableRoomTypes[strings.ToLower(found.RoomType)],
		PropertyType: propertyTypeNamed(found.PropertyType),
		MaxGuests:    int(found.Capacity.Max),
		Bedrooms:     int(found.Capacity.Bedrooms),
		Beds:         int(found.Capacity.Beds),
		Bathrooms:    float64(found.Capacity.Bathrooms),
		PhotoURLs:    photos,
	}
}

func streetLines(number, street string) (string, string) {
	number, street = strings.TrimSpace(number), strings.TrimSpace(street)
	if street == "" || number == "" {
		return street, ""
	}
	if unicode.IsDigit([]rune(street)[0]) {
		return street, number
	}
	return number + " " + street, ""
}
