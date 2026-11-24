package nearby

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	cacheLifetime      = 24 * time.Hour
	maxCachedSearches  = 1024
	maxPlaces          = 20
	sourcePlaceLimit   = 20
	searchRadiusMeters = 8000
)

var (
	ErrListingNotFound   = errors.New("listing not found")
	ErrUnknownCategory   = errors.New("unknown category")
	ErrSourceUnavailable = errors.New("nearby places are unavailable")
)

type Point struct {
	Latitude  float64
	Longitude float64
}

type Category struct {
	ID   string
	Keys []string
}

type Place struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Detail     string  `json:"detail"`
	Latitude   float64 `json:"lat"`
	Longitude  float64 `json:"lon"`
	DistanceKm float64 `json:"distance_km"`
}

type GeoapifyConfig struct {
	BaseURL   string
	APIKey    string
	UserAgent string
}

type LocationReader interface {
	Location(ctx context.Context, listingID uuid.UUID) (Point, error)
}

type PlaceSource interface {
	Find(ctx context.Context, category Category, origin Point) ([]Place, error)
}

type PlaceSearcher interface {
	Search(ctx context.Context, listingID uuid.UUID, categoryID string) ([]Place, error)
}
