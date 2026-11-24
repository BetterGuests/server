package nearby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	geoapifyPlacesPath       = "/v2/places"
	geoapifyTimeout          = 6 * time.Second
	geoapifyMaxResponseBytes = 4 << 20
	detailSeparator          = " · "
	categorySeparator        = "."
)

type geoapifyFeature struct {
	Properties struct {
		PlaceID    looseText `json:"place_id"`
		Name       looseText `json:"name"`
		Lat        *float64  `json:"lat"`
		Lon        *float64  `json:"lon"`
		Street     looseText `json:"street"`
		City       looseText `json:"city"`
		Categories []string  `json:"categories"`
	} `json:"properties"`
	Geometry struct {
		Coordinates []float64 `json:"coordinates"`
	} `json:"geometry"`
}

type looseText string

func (text *looseText) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*text = looseText(asString)
		return nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*text = looseText(asNumber.String())
		return nil
	}
	*text = ""
	return nil
}

type geoapifyResponse struct {
	Features []geoapifyFeature `json:"features"`
}

type GeoapifySource struct {
	config  GeoapifyConfig
	client  *http.Client
	timeout time.Duration
}

func NewGeoapifySource(config GeoapifyConfig, client *http.Client) *GeoapifySource {
	if client == nil {
		client = &http.Client{}
	}
	return &GeoapifySource{config: config, client: client, timeout: geoapifyTimeout}
}

func (source *GeoapifySource) Find(ctx context.Context, category Category, origin Point) ([]Place, error) {
	if !source.config.Configured() {
		return nil, fmt.Errorf("%w: no geoapify api key configured", ErrSourceUnavailable)
	}
	ctx, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.placesURL(category, origin), nil)
	if err != nil {
		return nil, fmt.Errorf("build geoapify request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", source.config.UserAgent)

	response, err := source.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceUnavailable, withoutURL(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: geoapify answered %d", ErrSourceUnavailable, response.StatusCode)
	}

	var body geoapifyResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, geoapifyMaxResponseBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: decode geoapify response: %w", ErrSourceUnavailable, err)
	}
	return placesFrom(body.Features), nil
}

func (source *GeoapifySource) placesURL(category Category, origin Point) string {
	longitude, latitude := formatCoordinate(origin.Longitude), formatCoordinate(origin.Latitude)
	params := url.Values{
		"categories": {strings.Join(category.Keys, ",")},
		"filter":     {fmt.Sprintf("circle:%s,%s,%d", longitude, latitude, searchRadiusMeters)},
		"bias":       {fmt.Sprintf("proximity:%s,%s", longitude, latitude)},
		"conditions": {"named"},
		"limit":      {strconv.Itoa(sourcePlaceLimit)},
		"lang":       {"en"},
		"apiKey":     {source.config.APIKey},
	}
	return strings.TrimRight(source.config.BaseURL, "/") + geoapifyPlacesPath + "?" + params.Encode()
}

func withoutURL(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err
	}
	return err
}

func formatCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func placesFrom(features []geoapifyFeature) []Place {
	places := make([]Place, 0, len(features))
	for _, feature := range features {
		if place, ok := placeFrom(feature); ok {
			places = append(places, place)
		}
	}
	return places
}

func placeFrom(feature geoapifyFeature) (Place, bool) {
	properties := feature.Properties
	latitude, longitude, located := coordinatesOf(feature)
	if properties.PlaceID == "" || properties.Name == "" || !located {
		return Place{}, false
	}
	return Place{
		ID:        string(properties.PlaceID),
		Name:      string(properties.Name),
		Detail:    detailOf(properties.Categories, string(properties.Street), string(properties.City)),
		Latitude:  latitude,
		Longitude: longitude,
	}, true
}

func coordinatesOf(feature geoapifyFeature) (float64, float64, bool) {
	if feature.Properties.Lat != nil && feature.Properties.Lon != nil {
		return *feature.Properties.Lat, *feature.Properties.Lon, true
	}
	if coordinates := feature.Geometry.Coordinates; len(coordinates) >= 2 {
		return coordinates[1], coordinates[0], true
	}
	return 0, 0, false
}

func detailOf(categories []string, street, city string) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{kindOf(categories), street, city} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, detailSeparator)
}

func kindOf(categories []string) string {
	deepest := ""
	for _, category := range categories {
		if strings.Count(category, categorySeparator) > strings.Count(deepest, categorySeparator) {
			deepest = category
		}
	}
	if !strings.Contains(deepest, categorySeparator) {
		return ""
	}
	return humanize(deepest[strings.LastIndex(deepest, categorySeparator)+1:])
}

func humanize(value string) string {
	spaced := strings.ReplaceAll(value, "_", " ")
	if spaced == "" {
		return ""
	}
	return strings.ToUpper(spaced[:1]) + spaced[1:]
}
