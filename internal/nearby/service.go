package nearby

import (
	"cmp"
	"context"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	defaultGeoapifyURL = "https://api.geoapify.com"
	defaultUserAgent   = "BetterGuests-Server/1.0"
	earthRadiusKm      = 6371.0
)

var categories = map[string]Category{
	"drinks": {
		ID:   "drinks",
		Keys: []string{"catering.bar", "catering.pub", "catering.biergarten", "catering.taproom", "adult.nightclub", "production.brewery"},
	},
	"liquor": {
		ID:   "liquor",
		Keys: []string{"commercial.food_and_drink.drinks"},
	},
	"coffee": {
		ID:   "coffee",
		Keys: []string{"catering.cafe"},
	},
	"food": {
		ID:   "food",
		Keys: []string{"catering.restaurant", "catering.fast_food"},
	},
	"groceries": {
		ID:   "groceries",
		Keys: []string{"commercial.supermarket", "commercial.convenience", "commercial.food_and_drink.fruit_and_vegetable"},
	},
	"pharmacy": {
		ID:   "pharmacy",
		Keys: []string{"healthcare.pharmacy"},
	},
	"gas": {
		ID:   "gas",
		Keys: []string{"service.vehicle.fuel"},
	},
}

func ConfigFromEnv() GeoapifyConfig {
	return GeoapifyConfig{
		BaseURL:   defaultGeoapifyURL,
		APIKey:    strings.TrimSpace(os.Getenv("GEOAPIFY_API_KEY")),
		UserAgent: defaultUserAgent,
	}
}

func (config GeoapifyConfig) Configured() bool {
	return config.APIKey != ""
}

type cachedSearch struct {
	places  []Place
	expires time.Time
}

type NearbyService struct {
	locations LocationReader
	source    PlaceSource
	now       func() time.Time
	flights   singleflight.Group
	mu        sync.Mutex
	cache     map[string]cachedSearch
}

func NewNearbyService(locations LocationReader, source PlaceSource) *NearbyService {
	return &NearbyService{
		locations: locations,
		source:    source,
		now:       time.Now,
		cache:     make(map[string]cachedSearch),
	}
}

func (service *NearbyService) Search(ctx context.Context, listingID uuid.UUID, categoryID string) ([]Place, error) {
	category, known := categories[categoryID]
	if !known {
		return nil, ErrUnknownCategory
	}
	key := listingID.String() + ":" + category.ID
	if places, hit := service.remembered(key); hit {
		return places, nil
	}
	origin, err := service.locations.Location(ctx, listingID)
	if err != nil {
		return nil, err
	}
	result, err, _ := service.flights.Do(key, func() (any, error) {
		found, err := service.source.Find(context.WithoutCancel(ctx), category, origin)
		if err != nil {
			return nil, err
		}
		ranked := rank(found, origin)
		service.remember(key, ranked)
		return ranked, nil
	})
	if err != nil {
		return nil, err
	}
	places, _ := result.([]Place)
	return slices.Clone(places), nil
}

func (service *NearbyService) remembered(key string) ([]Place, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	entry, ok := service.cache[key]
	if !ok || !service.now().Before(entry.expires) {
		return nil, false
	}
	return slices.Clone(entry.places), true
}

func (service *NearbyService) remember(key string, places []Place) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.cache) >= maxCachedSearches {
		service.evict()
	}
	service.cache[key] = cachedSearch{places: places, expires: service.now().Add(cacheLifetime)}
}

func (service *NearbyService) evict() {
	now := service.now()
	for key, entry := range service.cache {
		if !now.Before(entry.expires) {
			delete(service.cache, key)
		}
	}
	for key := range service.cache {
		if len(service.cache) < maxCachedSearches {
			return
		}
		delete(service.cache, key)
	}
}

func rank(found []Place, origin Point) []Place {
	ranked := slices.Clone(found)
	for index := range ranked {
		ranked[index].DistanceKm = distanceKm(origin, Point{
			Latitude:  ranked[index].Latitude,
			Longitude: ranked[index].Longitude,
		})
	}
	slices.SortFunc(ranked, func(a, b Place) int { return cmp.Compare(a.DistanceKm, b.DistanceKm) })
	if len(ranked) > maxPlaces {
		ranked = ranked[:maxPlaces]
	}
	return ranked
}

func distanceKm(a, b Point) float64 {
	const radians = math.Pi / 180
	haversine := math.Pow(math.Sin((b.Latitude-a.Latitude)*radians/2), 2) +
		math.Cos(a.Latitude*radians)*math.Cos(b.Latitude*radians)*
			math.Pow(math.Sin((b.Longitude-a.Longitude)*radians/2), 2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(haversine))
}
