package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/cashback"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	defaultMinimumAge   = 18
	downloadTimeout     = 30 * time.Second
	maxPhotoBytes       = 25 << 20
	importTimeout       = 20 * time.Minute
	defaultNightlyFloor = 1
)

var importNamespace = uuid.MustParse("6f1c1f0e-5a2b-4c3d-9e8f-0a1b2c3d4e5f")

type legacyImage struct {
	URL      string `json:"url"`
	Alt      string `json:"alt"`
	Position int    `json:"position"`
}

type legacyListing struct {
	ID          string        `json:"id"`
	Slug        string        `json:"slug"`
	Title       string        `json:"title"`
	Summary     string        `json:"summary"`
	Description string        `json:"description"`
	Type        string        `json:"type"`
	City        string        `json:"city"`
	Region      string        `json:"region"`
	Country     string        `json:"country"`
	Latitude    float64       `json:"latitude"`
	Longitude   float64       `json:"longitude"`
	MaxGuests   int           `json:"maxGuests"`
	Bedrooms    int           `json:"bedrooms"`
	Beds        int           `json:"beds"`
	Bathrooms   float64       `json:"bathrooms"`
	CreatedAt   string        `json:"createdAt"`
	HostName    string        `json:"host_name"`
	Images      []legacyImage `json:"images"`
	Amenities   []string      `json:"amenities"`
}

type plannedListing struct {
	legacy       legacyListing
	listingID    uuid.UUID
	hostID       uuid.UUID
	propertyType string
	countryCode  string
	amenities    []string
	skipped      []string
	price        sitePrice
	createdAt    time.Time
}

func main() {
	if err := run(); err != nil {
		slog.Error("import failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listingsPath := flag.String("listings", "", "path to the exported legacy listings JSON")
	pricesPath := flag.String("prices", "db/seed/ctc_nightly_prices.csv", "path to the nightly price CSV")
	apply := flag.Bool("apply", false, "write to the database and object storage; without it only the plan is printed")
	flag.Parse()
	if *listingsPath == "" {
		return errors.New("-listings is required")
	}

	plans, err := buildPlans(*listingsPath, *pricesPath)
	if err != nil {
		return err
	}
	printPlan(plans)
	if !*apply {
		slog.Info("dry run only; pass -apply to write")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	defer cancel()
	return applyPlans(ctx, plans)
}

func buildPlans(listingsPath, pricesPath string) ([]plannedListing, error) {
	raw, err := os.ReadFile(listingsPath)
	if err != nil {
		return nil, fmt.Errorf("read listings: %w", err)
	}
	var legacy []legacyListing
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, fmt.Errorf("parse listings: %w", err)
	}
	priceFile, err := os.Open(pricesPath)
	if err != nil {
		return nil, fmt.Errorf("open prices: %w", err)
	}
	defer priceFile.Close()
	prices, err := parsePrices(priceFile)
	if err != nil {
		return nil, err
	}

	plans := make([]plannedListing, 0, len(legacy))
	for _, item := range legacy {
		plan, err := planListing(item, prices)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.Slug, err)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func planListing(item legacyListing, prices map[string]sitePrice) (plannedListing, error) {
	propertyType, err := mapPropertyType(item.Type)
	if err != nil {
		return plannedListing{}, err
	}
	countryCode, err := mapCountryCode(item.Country)
	if err != nil {
		return plannedListing{}, err
	}
	createdAt, err := time.Parse("2006-01-02T15:04:05.999", item.CreatedAt)
	if err != nil {
		return plannedListing{}, fmt.Errorf("created at: %w", err)
	}
	price, ok := prices[siteSlug(item.Slug)]
	if !ok {
		return plannedListing{}, errors.New("no site price for this listing")
	}
	amenities, skipped := mapAmenities(item.Amenities)
	return plannedListing{
		legacy:       item,
		listingID:    uuid.NewSHA1(importNamespace, []byte("listing:"+item.ID)),
		hostID:       uuid.NewSHA1(importNamespace, []byte("host:"+item.HostName)),
		propertyType: propertyType,
		countryCode:  countryCode,
		amenities:    amenities,
		skipped:      skipped,
		price:        price,
		createdAt:    createdAt.UTC(),
	}, nil
}

func printPlan(plans []plannedListing) {
	photos := 0
	for _, plan := range plans {
		photos += len(plan.legacy.Images)
		fmt.Printf("%-45s %-9s $%-5d min %dn  %2d photos  %d amenities  skipped %v\n",
			plan.legacy.Title, plan.propertyType, plan.price.NightlyCents/centsPerDollar,
			plan.price.MinimumNights, len(plan.legacy.Images), len(plan.amenities), plan.skipped)
	}
	fmt.Printf("%d listings, %d photos\n", len(plans), photos)
}

func applyPlans(ctx context.Context, plans []plannedListing) error {
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	storage, bucket, err := newStorage()
	if err != nil {
		return err
	}
	for _, plan := range plans {
		exists, err := listingExists(ctx, pool, plan)
		if err != nil {
			return err
		}
		if exists {
			slog.Info("already imported, skipping", "slug", plan.legacy.Slug)
			continue
		}
		keys, err := uploadPhotos(ctx, storage, bucket, plan)
		if err != nil {
			return fmt.Errorf("%s photos: %w", plan.legacy.Slug, err)
		}
		if err := insertListing(ctx, pool, plan, keys); err != nil {
			return fmt.Errorf("%s insert: %w", plan.legacy.Slug, err)
		}
		slog.Info("imported", "slug", plan.legacy.Slug, "photos", len(keys))
	}
	return nil
}

func newStorage() (*minio.Client, string, error) {
	config, enabled, err := cashback.StorageConfigFromLookup(os.Getenv)
	if err != nil {
		return nil, "", err
	}
	if !enabled {
		return nil, "", errors.New("S3 settings are not configured")
	}
	lookup := minio.BucketLookupDNS
	if config.PathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(config.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(config.AccessKeyID, config.SecretAccessKey, ""),
		Secure:       config.UseSSL,
		Region:       config.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create storage client: %w", err)
	}
	return client, config.Bucket, nil
}

func listingExists(ctx context.Context, pool *pgxpool.Pool, plan plannedListing) (bool, error) {
	var exists bool
	err := db.WithUserTx(ctx, pool, plan.hostID.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.listings WHERE id = $1)`, plan.listingID).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("check existing listing: %w", err)
	}
	return exists, nil
}

func uploadPhotos(ctx context.Context, storage *minio.Client, bucket string, plan plannedListing) ([]string, error) {
	keys := make([]string, len(plan.legacy.Images))
	for index, image := range plan.legacy.Images {
		key := photoObjectKey(plan.listingID.String(), image.Position, image.URL)
		if err := copyPhoto(ctx, storage, bucket, key, image.URL); err != nil {
			return nil, fmt.Errorf("photo %d: %w", image.Position, err)
		}
		keys[index] = key
	}
	return keys, nil
}

func copyPhoto(ctx context.Context, storage *minio.Client, bucket, key, source string) error {
	if _, err := storage.StatObject(ctx, bucket, key, minio.StatObjectOptions{}); err == nil {
		return nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("refusing non-https source %q", source)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: downloadTimeout}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPhotoBytes+1))
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if len(body) > maxPhotoBytes {
		return errors.New("photo exceeds the size limit")
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	_, err = storage.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	return nil
}

func insertListing(ctx context.Context, pool *pgxpool.Pool, plan plannedListing, keys []string) error {
	return db.WithUserTx(ctx, pool, plan.hostID.String(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, plan.hostID); err != nil {
			return fmt.Errorf("host account: %w", err)
		}
		if err := insertListingRow(ctx, tx, plan); err != nil {
			return err
		}
		if err := insertAmenities(ctx, tx, plan); err != nil {
			return err
		}
		return insertPhotos(ctx, tx, plan, keys)
	})
}

func insertListingRow(ctx context.Context, tx pgx.Tx, plan plannedListing) error {
	item := plan.legacy
	_, err := tx.Exec(ctx, `
		INSERT INTO app.listings (
			id, host_id, title, description, summary, slug, host_name,
			city, region, country_code, latitude, longitude,
			listing_type, property_type, max_guests, minimum_age_to_book,
			bedrooms, beds, bathrooms, status,
			nightly_price_cents, minimum_nights, currency,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, NULLIF($5, ''), $6, $7,
			$8, NULLIF($9, ''), $10, $11, $12,
			'entire_place', $13, $14, $15,
			$16, $17, $18, 'published',
			$19, $20, 'USD',
			$21, now()
		)`,
		plan.listingID, plan.hostID, item.Title, item.Description, item.Summary, item.Slug, item.HostName,
		item.City, item.Region, plan.countryCode, item.Latitude, item.Longitude,
		plan.propertyType, item.MaxGuests, defaultMinimumAge,
		item.Bedrooms, bedCount(item.Beds, item.Bedrooms), item.Bathrooms,
		plan.price.NightlyCents, plan.price.MinimumNights,
		plan.createdAt,
	)
	if err != nil {
		return fmt.Errorf("listing row: %w", err)
	}
	return nil
}

func insertAmenities(ctx context.Context, tx pgx.Tx, plan plannedListing) error {
	for _, key := range plan.amenities {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.listing_amenities (id, listing_id, amenity_key)
			VALUES ($1, $2, $3) ON CONFLICT (listing_id, amenity_key) DO NOTHING`,
			uuid.New(), plan.listingID, key); err != nil {
			return fmt.Errorf("amenity %s: %w", key, err)
		}
	}
	return nil
}

func insertPhotos(ctx context.Context, tx pgx.Tx, plan plannedListing, keys []string) error {
	for index, image := range plan.legacy.Images {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.listing_photos (id, listing_id, object_key, alt_text, position)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5)`,
			uuid.New(), plan.listingID, keys[index], image.Alt, image.Position); err != nil {
			return fmt.Errorf("photo %d: %w", image.Position, err)
		}
	}
	return nil
}
