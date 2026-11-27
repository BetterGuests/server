package pms

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

const (
	hostawayAddress  = "https://api.hostaway.com"
	hostawayPageSize = 100
)

type HostawaySource struct {
	client          *http.Client
	address         string
	tokenSettleTime time.Duration
}

func NewHostawaySource() *HostawaySource {
	return &HostawaySource{client: newAPIClient(), address: hostawayAddress, tokenSettleTime: time.Second}
}

type hostawayListing struct {
	ID           flexibleID     `json:"id"`
	Name         string         `json:"name"`
	PublicName   string         `json:"externalListingName"`
	Description  string         `json:"description"`
	Street       string         `json:"street"`
	City         string         `json:"city"`
	State        string         `json:"state"`
	Zipcode      string         `json:"zipcode"`
	CountryCode  string         `json:"countryCode"`
	Country      string         `json:"country"`
	Latitude     flexibleNumber `json:"lat"`
	Longitude    flexibleNumber `json:"lng"`
	PropertyType flexibleID     `json:"propertyTypeId"`
	RoomType     string         `json:"roomType"`
	Capacity     flexibleNumber `json:"personCapacity"`
	Bedrooms     flexibleNumber `json:"bedroomsNumber"`
	Beds         flexibleNumber `json:"bedsNumber"`
	Bathrooms    flexibleNumber `json:"bathroomsNumber"`
	Price        flexibleNumber `json:"price"`
	Currency     string         `json:"currencyCode"`
	Images       []struct {
		URL   string         `json:"url"`
		Order flexibleNumber `json:"sortOrder"`
	} `json:"listingImages"`
}

func (source *HostawaySource) Properties(ctx context.Context, credentials Credentials) ([]Property, error) {
	headers, err := source.signIn(ctx, credentials)
	if err != nil {
		return nil, err
	}
	var found []hostawayListing
	for len(found) < maxProperties {
		var page struct {
			Result []hostawayListing `json:"result"`
		}
		address := fmt.Sprintf("%s/v1/listings?includeResources=1&limit=%d&offset=%d", source.address, hostawayPageSize, len(found))
		if err := getJSON(ctx, source.client, address, headers, &page); err != nil {
			return nil, err
		}
		found = append(found, page.Result...)
		if len(page.Result) < hostawayPageSize {
			break
		}
	}
	types := source.propertyTypes(ctx, headers)
	properties := make([]Property, 0, len(found))
	for _, listing := range found {
		properties = append(properties, listing.property(types))
	}
	return properties, nil
}

func (source *HostawaySource) signIn(ctx context.Context, credentials Credentials) (map[string]string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {credentials.Account},
		"client_secret": {credentials.Secret},
		"scope":         {"general"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.address+"/v1/accessTokens", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token tokenResponse
	if err := fetchToken(source.client, request, &token); err != nil {
		return nil, err
	}
	select {
	case <-time.After(source.tokenSettleTime):
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrProviderFailed, ctx.Err())
	}
	return map[string]string{"Authorization": "Bearer " + token.AccessToken}, nil
}

func (source *HostawaySource) propertyTypes(ctx context.Context, headers map[string]string) map[flexibleID]string {
	var response struct {
		Result []struct {
			ID   flexibleID `json:"id"`
			Name string     `json:"name"`
		} `json:"result"`
	}
	if err := getJSON(ctx, source.client, source.address+"/v1/propertyTypes", headers, &response); err != nil {
		return nil
	}
	types := make(map[flexibleID]string, len(response.Result))
	for _, entry := range response.Result {
		types[entry.ID] = entry.Name
	}
	return types
}

var hostawayRoomTypes = map[string]listings.ListingType{
	"entire_home":  listings.EntirePlace,
	"private_room": listings.PrivateRoom,
	"shared_room":  listings.SharedRoom,
}

func (listing hostawayListing) property(types map[flexibleID]string) Property {
	images := slices.Clone(listing.Images)
	slices.SortStableFunc(images, func(a, b struct {
		URL   string         `json:"url"`
		Order flexibleNumber `json:"sortOrder"`
	}) int {
		return cmp.Compare(a.Order, b.Order)
	})
	photos := []string{}
	for _, image := range images {
		if image.URL != "" {
			photos = append(photos, image.URL)
		}
	}
	return Property{
		ID:          string(listing.ID),
		Title:       firstFilled(listing.PublicName, listing.Name),
		Description: listing.Description,
		Address: listings.Address{
			Line1: listing.Street, City: listing.City, Region: listing.State, PostalCode: listing.Zipcode,
			CountryCode: countryCode(firstFilled(listing.CountryCode, listing.Country)),
			Latitude:    float64(listing.Latitude), Longitude: float64(listing.Longitude),
		},
		Type:         hostawayRoomTypes[listing.RoomType],
		PropertyType: propertyTypeNamed(types[listing.PropertyType]),
		MaxGuests:    int(listing.Capacity),
		Bedrooms:     int(listing.Bedrooms),
		Beds:         int(listing.Beds),
		Bathrooms:    float64(listing.Bathrooms),
		NightlyCents: cents(listing.Price),
		Currency:     listing.Currency,
		PhotoURLs:    photos,
	}
}
