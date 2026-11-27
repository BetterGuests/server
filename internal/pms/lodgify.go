package pms

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

const (
	lodgifyAddress  = "https://api.lodgify.com"
	lodgifyPageSize = 50
)

type LodgifySource struct {
	client  *http.Client
	address string
}

func NewLodgifySource() *LodgifySource {
	return &LodgifySource{client: newAPIClient(), address: lodgifyAddress}
}

type lodgifyProperty struct {
	ID          flexibleID     `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Address     string         `json:"address"`
	Zip         string         `json:"zip"`
	City        string         `json:"city"`
	State       string         `json:"state"`
	CountryCode string         `json:"country_code"`
	Country     string         `json:"country"`
	Latitude    flexibleNumber `json:"latitude"`
	Longitude   flexibleNumber `json:"longitude"`
	ImageURL    string         `json:"image_url"`
}

type lodgifyRoom struct {
	MaxPeople flexibleNumber `json:"max_people"`
	Bedrooms  flexibleNumber `json:"bedrooms"`
	Bathrooms flexibleNumber `json:"bathrooms"`
	Images    []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (source *LodgifySource) Properties(ctx context.Context, credentials Credentials) ([]Property, error) {
	headers := map[string]string{"X-ApiKey": credentials.Secret}
	var properties []Property
	for page := 1; len(properties) < maxProperties; page++ {
		var response struct {
			Items []lodgifyProperty `json:"items"`
		}
		address := fmt.Sprintf("%s/v2/properties?page=%d&size=%d&includeInOut=false", source.address, page, lodgifyPageSize)
		if err := getJSON(ctx, source.client, address, headers, &response); err != nil {
			return nil, err
		}
		for _, found := range response.Items {
			properties = append(properties, found.property())
		}
		if len(response.Items) < lodgifyPageSize {
			break
		}
	}
	completeEach(ctx, properties, func(ctx context.Context, property *Property) error {
		var rooms []lodgifyRoom
		address := source.address + "/v2/properties/" + url.PathEscape(property.ID) + "/rooms"
		if err := getJSON(ctx, source.client, address, headers, &rooms); err != nil {
			return err
		}
		addRooms(property, rooms)
		return nil
	})
	return properties, nil
}

func (found lodgifyProperty) property() Property {
	photos := []string{}
	if found.ImageURL != "" {
		photos = append(photos, withScheme(found.ImageURL))
	}
	return Property{
		ID:          string(found.ID),
		Title:       found.Name,
		Description: found.Description,
		Address: listings.Address{
			Line1: found.Address, City: found.City, Region: found.State, PostalCode: found.Zip,
			CountryCode: countryCode(firstFilled(found.CountryCode, found.Country)),
			Latitude:    float64(found.Latitude), Longitude: float64(found.Longitude),
		},
		PhotoURLs: photos,
	}
}

func addRooms(property *Property, rooms []lodgifyRoom) {
	if len(rooms) == 1 {
		property.MaxGuests = int(rooms[0].MaxPeople)
		property.Bedrooms = int(rooms[0].Bedrooms)
		property.Bathrooms = float64(rooms[0].Bathrooms)
	}
	for _, room := range rooms {
		for _, image := range room.Images {
			address := withScheme(image.URL)
			if address != "" && !slices.Contains(property.PhotoURLs, address) {
				property.PhotoURLs = append(property.PhotoURLs, address)
			}
		}
	}
}
