package pms

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

const (
	ownerRezAddress     = "https://api.ownerrez.com"
	ownerRezAuthAddress = "https://app.ownerrez.com"
	ownerRezPageSize    = 100
	maxOwnerRezPages    = 10
)

type OwnerRezSource struct {
	client       *http.Client
	address      string
	authAddress  string
	clientID     string
	clientSecret string
	redirect     string
}

func NewOwnerRezSource(clientID, clientSecret, redirect string) *OwnerRezSource {
	return &OwnerRezSource{client: newAPIClient(), address: ownerRezAddress, authAddress: ownerRezAuthAddress, clientID: clientID, clientSecret: clientSecret, redirect: redirect}
}

func (source *OwnerRezSource) AuthorizeURL(state string) string {
	query := url.Values{"response_type": {"code"}, "client_id": {source.clientID}, "redirect_uri": {source.redirect}, "state": {state}}
	return source.authAddress + "/oauth/authorize?" + query.Encode()
}

func (source *OwnerRezSource) userAgent() string {
	return "BetterGuests/1.0 (" + source.clientID + ")"
}

func (source *OwnerRezSource) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {source.redirect}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.address+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	basic := base64.StdEncoding.EncodeToString([]byte(source.clientID + ":" + source.clientSecret))
	request.Header.Set("Authorization", "Basic "+basic)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", source.userAgent())
	var token tokenResponse
	if err := fetchToken(source.client, request, &token); err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

type ownerRezProperty struct {
	ID         flexibleID `json:"id"`
	Name       string     `json:"name"`
	PublicName string     `json:"external_name"`
	Active     *bool      `json:"active"`
	Address    struct {
		Street1  string     `json:"street1"`
		Street2  string     `json:"street2"`
		City     string     `json:"city"`
		State    string     `json:"state"`
		Province string     `json:"province"`
		Postal   flexibleID `json:"postal_code"`
		Country  string     `json:"country"`
	} `json:"address"`
	Latitude     flexibleNumber `json:"latitude"`
	Longitude    flexibleNumber `json:"longitude"`
	PropertyType string         `json:"property_type"`
	MaxGuests    flexibleNumber `json:"max_guests"`
	Bedrooms     flexibleNumber `json:"bedrooms"`
	Bathrooms    flexibleNumber `json:"bathrooms"`
	Thumbnail    string         `json:"thumbnail_url_large"`
}

type ownerRezListing struct {
	PropertyID   flexibleID `json:"property_id"`
	SpaceType    string     `json:"property_space_type"`
	Descriptions struct {
		Short       string `json:"short_description"`
		Description string `json:"description"`
	} `json:"descriptions"`
	Rooms []struct {
		Beds flexibleNumber `json:"total_beds"`
	} `json:"rooms"`
	Photos []struct {
		Original string `json:"original_url"`
		Large    string `json:"large_url"`
	} `json:"photos"`
}

type ownerRezPage[Item any] struct {
	Items []Item `json:"items"`
	Next  string `json:"next_page_url"`
}

func (source *OwnerRezSource) Properties(ctx context.Context, credentials Credentials) ([]Property, error) {
	headers := map[string]string{"Authorization": "Bearer " + credentials.Secret, "User-Agent": source.userAgent()}
	found, err := ownerRezItems[ownerRezProperty](ctx, source, headers, fmt.Sprintf("/v2/properties?limit=%d", ownerRezPageSize))
	if err != nil {
		return nil, err
	}
	listingPath := fmt.Sprintf("/v2/listings?includeRooms=true&includeImages=true&includeDescriptions=Text&limit=%d", ownerRezPageSize)
	details, _ := ownerRezItems[ownerRezListing](ctx, source, headers, listingPath)
	byProperty := make(map[flexibleID]ownerRezListing, len(details))
	for _, listing := range details {
		byProperty[listing.PropertyID] = listing
	}
	properties := make([]Property, 0, len(found))
	for _, entry := range found {
		if entry.Active != nil && !*entry.Active {
			continue
		}
		property := entry.property()
		byProperty[entry.ID].addTo(&property)
		properties = append(properties, property)
	}
	return properties, nil
}

func ownerRezItems[Item any](ctx context.Context, source *OwnerRezSource, headers map[string]string, path string) ([]Item, error) {
	var items []Item
	for range maxOwnerRezPages {
		var page ownerRezPage[Item]
		if err := getJSON(ctx, source.client, source.address+path, headers, &page); err != nil {
			return items, err
		}
		items = append(items, page.Items...)
		if !strings.HasPrefix(page.Next, "/") || len(items) >= maxProperties*2 {
			break
		}
		path = page.Next
	}
	return items, nil
}

func (entry ownerRezProperty) property() Property {
	photos := []string{}
	if entry.Thumbnail != "" {
		photos = append(photos, entry.Thumbnail)
	}
	address := entry.Address
	return Property{
		ID:    string(entry.ID),
		Title: firstFilled(entry.PublicName, entry.Name),
		Address: listings.Address{
			Line1: address.Street1, Line2: address.Street2, City: address.City,
			Region: firstFilled(address.State, address.Province), PostalCode: string(address.Postal),
			CountryCode: countryCode(address.Country),
			Latitude:    float64(entry.Latitude), Longitude: float64(entry.Longitude),
		},
		PropertyType: propertyTypeNamed(entry.PropertyType),
		MaxGuests:    int(entry.MaxGuests),
		Bedrooms:     int(entry.Bedrooms),
		Bathrooms:    float64(entry.Bathrooms),
		PhotoURLs:    photos,
	}
}

var ownerRezSpaceTypes = map[string]listings.ListingType{
	"entire_home":  listings.EntirePlace,
	"private_room": listings.PrivateRoom,
	"shared_room":  listings.SharedRoom,
}

func (listing ownerRezListing) addTo(property *Property) {
	property.Type = ownerRezSpaceTypes[listing.SpaceType]
	property.Description = firstFilled(listing.Descriptions.Description, listing.Descriptions.Short)
	for _, room := range listing.Rooms {
		property.Beds += int(room.Beds)
	}
	photos := []string{}
	for _, photo := range listing.Photos {
		if address := firstFilled(photo.Large, photo.Original); address != "" {
			photos = append(photos, address)
		}
	}
	if len(photos) > 0 {
		property.PhotoURLs = photos
	}
}
