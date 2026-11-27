package listings

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	foreignKeyViolationCode = "23503"
	restrictViolationCode   = "23001"
	conversationsTable      = "conversations"
)

type ListingStore interface {
	Create(
		ctx context.Context,
		userID string,
		listing Listing,
	) error
}

type PostgresListingStore struct {
	DB *pgxpool.Pool
}

func NewPostgresListingStore(pool *pgxpool.Pool) *PostgresListingStore {
	return &PostgresListingStore{DB: pool}
}

func (s *PostgresListingStore) Create(
	ctx context.Context,
	userID string,
	listing Listing,
) error {
	return db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		if err := ensureHostAccount(ctx, tx, listing.HostID); err != nil {
			return err
		}
		if err := insertListingRow(ctx, tx, listing); err != nil {
			return err
		}
		if err := insertListingAmenities(ctx, tx, listing.ID, listing.Amenities); err != nil {
			return err
		}
		if err := insertListingPhotos(ctx, tx, listing.ID, listing.Photos); err != nil {
			return err
		}
		return insertListingRules(ctx, tx, listing.ID, listing.Rules)
	})
}

const uniqueViolationCode = "23505"

const maxHostListings = 100

func (s *PostgresListingStore) ListForHost(ctx context.Context, hostID uuid.UUID) ([]HostListing, error) {
	found := []HostListing{}
	err := db.WithUserTx(ctx, s.DB, hostID.String(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, title, city, status
			FROM app.listings
			WHERE host_id = $1
			ORDER BY created_at DESC, id
			LIMIT $2`, hostID, maxHostListings)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var listing HostListing
			if err := rows.Scan(&listing.ID, &listing.Title, &listing.City, &listing.Status); err != nil {
				return err
			}
			found = append(found, listing)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list host listings: %w", err)
	}
	return found, nil
}

func (s *PostgresListingStore) Publish(ctx context.Context, userID string, listingID uuid.UUID) error {
	return db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		var hostID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT host_id FROM app.listings WHERE id = $1`, listingID).Scan(&hostID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrListingNotFound
		}
		if err != nil {
			return fmt.Errorf("find listing: %w", err)
		}
		if hostID.String() != userID {
			return ErrNotListingOwner
		}
		if err := requirePriceAndPhoto(ctx, tx, listingID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE app.listings SET status = 'published', published_at = coalesce(published_at, now()), updated_at = now() WHERE id = $1`, listingID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
			return ErrDuplicateListing
		}
		if err != nil {
			return fmt.Errorf("publish listing: %w", err)
		}
		return nil
	})
}

func ensureHostAccount(ctx context.Context, tx pgx.Tx, hostID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, hostID); err != nil {
		return fmt.Errorf("ensure host account: %w", err)
	}
	return nil
}

func insertListingRow(ctx context.Context, tx pgx.Tx, listing Listing) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO app.listings (
			id,
			host_id,
			title,
			description,
			address_line1,
			address_line2,
			city,
			region,
			postal_code,
			country_code,
			latitude,
			longitude,
			place_id,
			listing_type,
			property_type,
			max_guests,
			minimum_age_to_book,
			bedrooms,
			beds,
			bathrooms,
			status,
			created_at,
			updated_at
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			NULLIF($6,''),
			$7,
			NULLIF($8,''),
			NULLIF($9,''),
			$10,
			$11,
			$12,
			NULLIF($13,''),
			$14,
			$15,
			$16,
			$17,
			$18,
			$19,
			$20,
			$21,
			$22,
			$23
		)
	`,
		listing.ID,
		listing.HostID,
		listing.Title,
		listing.Description,
		listing.Address.Line1,
		listing.Address.Line2,
		listing.Address.City,
		listing.Address.Region,
		listing.Address.PostalCode,
		listing.Address.CountryCode,
		listing.Address.Latitude,
		listing.Address.Longitude,
		listing.Address.PlaceID,
		listing.Type,
		listing.PropertyType,
		listing.MaxGuests,
		listing.MinimumAgeToBook,
		listing.Bedrooms,
		listing.Beds,
		listing.Bathrooms,
		listing.Status,
		listing.CreatedAt,
		listing.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert listing: %w", err)
	}
	return nil
}

func insertListingAmenities(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, amenities []Amenity) error {
	if len(amenities) == 0 {
		return nil
	}
	ids := make([]string, len(amenities))
	keys := make([]string, len(amenities))
	for index, amenity := range amenities {
		ids[index] = uuid.NewString()
		keys[index] = string(amenity)
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO app.listing_amenities (id, listing_id, amenity_key)
		SELECT input.id::uuid, $2, catalog.key
		FROM unnest($1::text[], $3::text[]) AS input(id, key)
		JOIN app.amenities catalog ON catalog.key = input.key
	`, ids, listingID, keys)
	if err != nil {
		return fmt.Errorf("link amenities: %w", err)
	}
	if result.RowsAffected() != int64(len(amenities)) {
		return fmt.Errorf("amenity catalog is missing %d of %d requested amenities", int64(len(amenities))-result.RowsAffected(), len(amenities))
	}
	return nil
}

func insertListingPhotos(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, photos []ListingPhoto) error {
	if len(photos) == 0 {
		return nil
	}
	ids := make([]string, len(photos))
	objectKeys := make([]string, len(photos))
	altTexts := make([]string, len(photos))
	positions := make([]int32, len(photos))
	screenings := make([]string, len(photos))
	fingerprints := make([]int64, len(photos))
	for index, photo := range photos {
		ids[index] = photo.ID.String()
		objectKeys[index] = photo.ObjectKey
		altTexts[index] = photo.AltText
		positions[index] = int32(photo.Position)
		screenings[index] = string(cmp.Or(photo.Screening, ScreeningPassed))
		fingerprints[index] = int64(photo.Fingerprint)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app.listing_photos (id, listing_id, object_key, alt_text, position, screening, fingerprint)
		SELECT input.id::uuid, $2, input.object_key, NULLIF(input.alt_text, ''), input.position, input.screening, NULLIF(input.fingerprint, 0)
		FROM unnest($1::text[], $3::text[], $4::text[], $5::int[], $6::text[], $7::bigint[]) AS input(id, object_key, alt_text, position, screening, fingerprint)
	`, ids, listingID, objectKeys, altTexts, positions, screenings, fingerprints); err != nil {
		return fmt.Errorf("insert listing photos: %w", err)
	}
	return nil
}

func insertListingRules(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, rules []HouseRule) error {
	if len(rules) == 0 {
		return nil
	}
	ids := make([]string, len(rules))
	titles := make([]string, len(rules))
	descriptions := make([]string, len(rules))
	positions := make([]int32, len(rules))
	for index, rule := range rules {
		ids[index] = uuid.NewString()
		titles[index] = rule.Title
		descriptions[index] = rule.Description
		positions[index] = int32(index)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app.listing_rules (id, listing_id, title, description, position)
		SELECT input.id::uuid, $2, input.title, NULLIF(input.description, ''), input.position
		FROM unnest($1::text[], $3::text[], $4::text[], $5::int[]) AS input(id, title, description, position)
	`, ids, listingID, titles, descriptions, positions); err != nil {
		return fmt.Errorf("insert listing rules: %w", err)
	}
	return nil
}

const publicListingColumns = `
	l.id, coalesce(l.slug, ''), l.title, coalesce(l.summary, ''), l.description,
	coalesce(l.host_name, ''), coalesce(l.host_avatar_url, ''), l.city, coalesce(l.region, ''), l.country_code,
	l.latitude, l.longitude, l.listing_type, l.property_type, l.max_guests,
	l.bedrooms, l.beds, l.bathrooms::float8, l.minimum_nights,
	l.nightly_price_cents, l.cleaning_fee_cents, l.currency,
	l.rating_avg::float8, l.rating_count, l.created_at, l.published_at,
	to_char(l.check_in_time, 'HH24:MI'), to_char(l.check_out_time, 'HH24:MI')`

func (s *PostgresListingStore) ListPublished(ctx context.Context) ([]PublicListing, error) {
	var listings []PublicListing
	err := db.WithPublicTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+publicListingColumns+`
			FROM app.listings l
			WHERE l.status = 'published'
			ORDER BY l.created_at, l.id
			LIMIT 200`)
		if err != nil {
			return fmt.Errorf("query published listings: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			listing, err := scanPublicListing(rows)
			if err != nil {
				return err
			}
			listings = append(listings, listing)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read published listings: %w", err)
		}
		return attachChildren(ctx, tx, listings)
	})
	if err != nil {
		return nil, err
	}
	return listings, nil
}

func (s *PostgresListingStore) GetPublished(ctx context.Context, id uuid.UUID) (PublicListing, error) {
	var found []PublicListing
	err := db.WithPublicTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT `+publicListingColumns+`
			FROM app.listings l
			WHERE l.id = $1 AND l.status = 'published'`, id)
		if err != nil {
			return fmt.Errorf("query published listing: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			listing, err := scanPublicListing(rows)
			if err != nil {
				return err
			}
			found = append(found, listing)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read published listing: %w", err)
		}
		return attachChildren(ctx, tx, found)
	})
	if err != nil {
		return PublicListing{}, err
	}
	if len(found) == 0 {
		return PublicListing{}, ErrListingNotFound
	}
	return found[0], nil
}

func scanPublicListing(rows pgx.Rows) (PublicListing, error) {
	var (
		listing       PublicListing
		nightly       *int
		cleaning      int
		currency      string
		ratingAverage *float64
		ratingCount   int
	)
	if err := rows.Scan(
		&listing.ID, &listing.Slug, &listing.Title, &listing.Summary, &listing.Description,
		&listing.HostName, &listing.HostAvatarURL, &listing.Location.City, &listing.Location.Region, &listing.Location.CountryCode,
		&listing.Location.Latitude, &listing.Location.Longitude, &listing.Type, &listing.PropertyType, &listing.MaxGuests,
		&listing.Bedrooms, &listing.Beds, &listing.Bathrooms, &listing.MinimumNights,
		&nightly, &cleaning, &currency,
		&ratingAverage, &ratingCount, &listing.CreatedAt, &listing.PublishedAt,
		&listing.CheckInTime, &listing.CheckOutTime,
	); err != nil {
		return PublicListing{}, fmt.Errorf("scan published listing: %w", err)
	}
	if nightly != nil && *nightly > 0 {
		listing.Price = &ListingPrice{NightlyCents: *nightly, CleaningFeeCents: cleaning, Currency: currency}
	}
	if ratingAverage != nil && ratingCount > 0 {
		listing.Rating = &ListingRating{Average: *ratingAverage, Count: ratingCount}
	}
	listing.Amenities = []Amenity{}
	listing.Photos = []ListingPhoto{}
	listing.Rules = []HouseRule{}
	return listing, nil
}

func attachChildren(ctx context.Context, tx pgx.Tx, listings []PublicListing) error {
	if len(listings) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(listings))
	position := make(map[uuid.UUID]int, len(listings))
	for index, listing := range listings {
		ids[index] = listing.ID
		position[listing.ID] = index
	}
	if err := attachPhotos(ctx, tx, listings, ids, position); err != nil {
		return err
	}
	if err := attachAmenities(ctx, tx, listings, ids, position); err != nil {
		return err
	}
	return attachRules(ctx, tx, listings, ids, position)
}

func attachPhotos(ctx context.Context, tx pgx.Tx, listings []PublicListing, ids []uuid.UUID, position map[uuid.UUID]int) error {
	rows, err := tx.Query(ctx, `
		SELECT listing_id, id, object_key, coalesce(alt_text, ''), position
		FROM app.listing_photos WHERE listing_id = ANY($1) AND screening = 'passed' ORDER BY listing_id, position`, ids)
	if err != nil {
		return fmt.Errorf("query listing photos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var listingID uuid.UUID
		var photo ListingPhoto
		if err := rows.Scan(&listingID, &photo.ID, &photo.ObjectKey, &photo.AltText, &photo.Position); err != nil {
			return fmt.Errorf("scan listing photo: %w", err)
		}
		index := position[listingID]
		listings[index].Photos = append(listings[index].Photos, photo)
	}
	return rows.Err()
}

func attachAmenities(ctx context.Context, tx pgx.Tx, listings []PublicListing, ids []uuid.UUID, position map[uuid.UUID]int) error {
	rows, err := tx.Query(ctx, `
		SELECT listing_id, amenity_key
		FROM app.listing_amenities WHERE listing_id = ANY($1) ORDER BY listing_id, amenity_key`, ids)
	if err != nil {
		return fmt.Errorf("query listing amenities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var listingID uuid.UUID
		var key Amenity
		if err := rows.Scan(&listingID, &key); err != nil {
			return fmt.Errorf("scan listing amenity: %w", err)
		}
		index := position[listingID]
		listings[index].Amenities = append(listings[index].Amenities, key)
	}
	return rows.Err()
}

func attachRules(ctx context.Context, tx pgx.Tx, listings []PublicListing, ids []uuid.UUID, position map[uuid.UUID]int) error {
	rows, err := tx.Query(ctx, `
		SELECT listing_id, title, coalesce(description, '')
		FROM app.listing_rules WHERE listing_id = ANY($1) ORDER BY listing_id, position`, ids)
	if err != nil {
		return fmt.Errorf("query listing rules: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var listingID uuid.UUID
		var rule HouseRule
		if err := rows.Scan(&listingID, &rule.Title, &rule.Description); err != nil {
			return fmt.Errorf("scan listing rule: %w", err)
		}
		index := position[listingID]
		listings[index].Rules = append(listings[index].Rules, rule)
	}
	return rows.Err()
}

func requirePriceAndPhoto(ctx context.Context, tx pgx.Tx, listingID uuid.UUID) error {
	var priced, pictured, unchecked bool
	err := tx.QueryRow(ctx, `
		SELECT l.nightly_price_cents IS NOT NULL,
		       (SELECT count(*) FROM app.listing_photos p WHERE p.listing_id = l.id) >= $2,
		       EXISTS (SELECT 1 FROM app.listing_photos p WHERE p.listing_id = l.id AND p.screening <> 'passed')
		FROM app.listings l WHERE l.id = $1 FOR UPDATE`, listingID, minPublishPhotos).Scan(&priced, &pictured, &unchecked)
	if err != nil {
		return fmt.Errorf("check price and photos: %w", err)
	}
	if unchecked {
		return ErrPhotosRefused
	}
	if !priced {
		return ErrPriceRequired
	}
	if !pictured {
		return ErrPhotoRequired
	}
	return nil
}

func requireOwnedListing(ctx context.Context, tx pgx.Tx, userID string, listingID uuid.UUID) (Setup, error) {
	var hostID uuid.UUID
	setup := Setup{Photos: []ListingPhoto{}}
	err := tx.QueryRow(ctx,
		`SELECT host_id, status, COALESCE(nightly_price_cents, 0) FROM app.listings WHERE id = $1`, listingID,
	).Scan(&hostID, &setup.Status, &setup.NightlyPriceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return Setup{}, ErrListingNotFound
	}
	if err != nil {
		return Setup{}, fmt.Errorf("find listing: %w", err)
	}
	if hostID.String() != userID {
		return Setup{}, ErrNotListingOwner
	}
	return setup, nil
}

func lockOwnedListing(ctx context.Context, tx pgx.Tx, userID string, listingID uuid.UUID) (ListingStatus, error) {
	if _, err := requireOwnedListing(ctx, tx, userID, listingID); err != nil {
		return "", err
	}
	var status ListingStatus
	if err := tx.QueryRow(ctx, `SELECT status FROM app.listings WHERE id = $1 FOR UPDATE`, listingID).Scan(&status); err != nil {
		return "", fmt.Errorf("lock listing: %w", err)
	}
	return status, nil
}

func (s *PostgresListingStore) Setup(ctx context.Context, userID string, listingID uuid.UUID) (Setup, error) {
	var setup Setup
	err := db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		found, err := requireOwnedListing(ctx, tx, userID, listingID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, object_key, coalesce(alt_text, ''), position, screening, coalesce(refusal, ''), coalesce(fingerprint, 0)
			FROM app.listing_photos WHERE listing_id = $1 ORDER BY position`, listingID)
		if err != nil {
			return fmt.Errorf("query listing photos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var photo ListingPhoto
			var fingerprint int64
			if err := rows.Scan(&photo.ID, &photo.ObjectKey, &photo.AltText, &photo.Position, &photo.Screening, &photo.Refusal, &fingerprint); err != nil {
				return fmt.Errorf("scan listing photo: %w", err)
			}
			photo.Fingerprint = PhotoFingerprint(fingerprint)
			found.Photos = append(found.Photos, photo)
		}
		setup = found
		return rows.Err()
	})
	if err != nil {
		return Setup{}, err
	}
	return setup, nil
}

func (s *PostgresListingStore) AddPhoto(ctx context.Context, userID string, listingID uuid.UUID, photo ListingPhoto) error {
	return db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		if _, err := lockOwnedListing(ctx, tx, userID, listingID); err != nil {
			return err
		}
		var count, next int
		var duplicate bool
		if err := tx.QueryRow(ctx, `
			SELECT count(*), coalesce(max(position) + 1, 0), coalesce(bool_or(object_key = $2), false)
			FROM app.listing_photos WHERE listing_id = $1`, listingID, photo.ObjectKey,
		).Scan(&count, &next, &duplicate); err != nil {
			return fmt.Errorf("count listing photos: %w", err)
		}
		if duplicate {
			return ErrPhotoAlreadyAdded
		}
		if count >= maxPhotos {
			return ErrTooManyPhotos
		}
		if err := refuseListedLookalike(ctx, tx, listingID, photo.Fingerprint); err != nil {
			return err
		}
		photo.Position = next
		return insertListingPhotos(ctx, tx, listingID, []ListingPhoto{photo})
	})
}

func refuseListedLookalike(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, fingerprint PhotoFingerprint) error {
	if fingerprint == noFingerprint {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT fingerprint FROM app.listing_photos WHERE listing_id = $1 AND fingerprint IS NOT NULL`, listingID)
	if err != nil {
		return fmt.Errorf("query listing photo fingerprints: %w", err)
	}
	listed, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("scan listing photo fingerprints: %w", err)
	}
	for _, other := range listed {
		if fingerprint.sameImageAs(PhotoFingerprint(other)) {
			return ErrDuplicatePhoto
		}
	}
	return nil
}

func (s *PostgresListingStore) RemovePhoto(ctx context.Context, userID string, listingID, photoID uuid.UUID) (string, error) {
	var key string
	err := db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		status, err := lockOwnedListing(ctx, tx, userID, listingID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx,
			`DELETE FROM app.listing_photos WHERE id = $1 AND listing_id = $2 RETURNING object_key`, photoID, listingID,
		).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPhotoNotFound
		}
		if err != nil {
			return fmt.Errorf("delete listing photo: %w", err)
		}
		if status != Published {
			return nil
		}
		var remaining int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.listing_photos WHERE listing_id = $1 AND screening = 'passed'`, listingID).Scan(&remaining); err != nil {
			return fmt.Errorf("count listing photos: %w", err)
		}
		if remaining < minPublishPhotos {
			return ErrLastPhoto
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return key, nil
}

const positionShiftDuringReorder = 1000

func (s *PostgresListingStore) ReorderPhotos(ctx context.Context, userID string, listingID uuid.UUID, photoIDs []uuid.UUID) error {
	return db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		if _, err := lockOwnedListing(ctx, tx, userID, listingID); err != nil {
			return err
		}
		ids := make([]string, len(photoIDs))
		for index, id := range photoIDs {
			ids[index] = id.String()
		}
		var listed, matched int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE id::text = ANY($2::text[]))
			FROM app.listing_photos WHERE listing_id = $1`, listingID, ids,
		).Scan(&listed, &matched); err != nil {
			return fmt.Errorf("check listing photo order: %w", err)
		}
		if listed != len(ids) || matched != len(ids) {
			return ErrPhotoOrderMismatch
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.listing_photos SET position = position + $2 WHERE listing_id = $1`, listingID, positionShiftDuringReorder,
		); err != nil {
			return fmt.Errorf("make room to reorder listing photos: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app.listing_photos AS photo SET position = ordered.position - 1
			FROM unnest($2::text[]) WITH ORDINALITY AS ordered(id, position)
			WHERE photo.listing_id = $1 AND photo.id::text = ordered.id`, listingID, ids,
		); err != nil {
			return fmt.Errorf("reorder listing photos: %w", err)
		}
		return nil
	})
}

func (s *PostgresListingStore) RecordScreening(ctx context.Context, userID string, listingID uuid.UUID, verdict ScreeningVerdict) (bool, error) {
	screening, objectKey, refusal := ScreeningPassed, verdict.ScreenedKey, ""
	if verdict.Refusal != "" {
		screening, objectKey, refusal = ScreeningRefused, verdict.UploadKey, string(verdict.Refusal)
	}
	var recorded bool
	err := db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE app.listing_photos SET screening = $4, object_key = $5, refusal = NULLIF($6, '')
			WHERE id = $1 AND listing_id = $2 AND object_key = $3 AND screening = 'pending'`,
			verdict.PhotoID, listingID, verdict.UploadKey, screening, objectKey, refusal)
		if err != nil {
			return fmt.Errorf("record photo screening: %w", err)
		}
		recorded = tag.RowsAffected() == 1
		return nil
	})
	return recorded, err
}

func (s *PostgresListingStore) SetPrice(ctx context.Context, userID string, listingID uuid.UUID, nightlyCents int) error {
	return db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		if _, err := lockOwnedListing(ctx, tx, userID, listingID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app.listings SET nightly_price_cents = $2, updated_at = now() WHERE id = $1`, listingID, nightlyCents,
		); err != nil {
			return fmt.Errorf("set nightly price: %w", err)
		}
		return nil
	})
}

func (s *PostgresListingStore) DeleteUntouched(ctx context.Context, userID string, listingID uuid.UUID) ([]string, error) {
	var keys []string
	err := db.WithUserTx(ctx, s.DB, userID, func(tx pgx.Tx) error {
		status, err := lockOwnedListing(ctx, tx, userID, listingID)
		if err != nil {
			return err
		}
		if err := returnToDraft(ctx, tx, listingID, status); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT object_key FROM app.listing_photos WHERE listing_id = $1`, listingID)
		if err != nil {
			return fmt.Errorf("query listing photos: %w", err)
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read listing photos: %w", err)
		}
		var released bool
		if err := tx.QueryRow(ctx, `SELECT app.release_draft_listing_documents($1)`, listingID).Scan(&released); err != nil {
			return fmt.Errorf("release listing documents: %w", err)
		}
		if !released {
			return ErrDeleteProofHeld
		}
		_, err = tx.Exec(ctx, `DELETE FROM app.listings WHERE id = $1`, listingID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
			if pgErr.TableName == conversationsTable {
				return ErrDeleteHasMessages
			}
			return ErrDeleteHasBookings
		}
		if err != nil {
			return fmt.Errorf("delete listing: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func returnToDraft(ctx context.Context, tx pgx.Tx, listingID uuid.UUID, status ListingStatus) error {
	var suspended bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app.listing_suspensions WHERE listing_id = $1)`, listingID,
	).Scan(&suspended); err != nil {
		return fmt.Errorf("check listing suspension: %w", err)
	}
	if suspended || status == Paused {
		return ErrDeletePaused
	}
	if status == Draft {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE app.listings SET status = 'draft', updated_at = now() WHERE id = $1`, listingID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == restrictViolationCode {
		return ErrDeleteHasBookings
	}
	if err != nil {
		return fmt.Errorf("unpublish listing: %w", err)
	}
	return nil
}

func (s *PostgresListingStore) PublishedPhotos(ctx context.Context) ([]PhotoToResize, error) {
	var photos []PhotoToResize
	err := db.WithPublicTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT l.host_id, l.id, p.id, p.object_key
			FROM app.listing_photos p
			JOIN app.listings l ON l.id = p.listing_id
			WHERE l.status = 'published' AND p.screening = 'passed'
			ORDER BY l.id, p.position`)
		if err != nil {
			return fmt.Errorf("query published photos: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var photo PhotoToResize
			if err := rows.Scan(&photo.HostID, &photo.ListingID, &photo.PhotoID, &photo.ObjectKey); err != nil {
				return fmt.Errorf("scan published photo: %w", err)
			}
			photos = append(photos, photo)
		}
		return rows.Err()
	})
	return photos, err
}

func (s *PostgresListingStore) ReplacePhotoKey(ctx context.Context, photo PhotoToResize, newKey string) (bool, error) {
	var replaced bool
	err := db.WithUserTx(ctx, s.DB, photo.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE app.listing_photos SET object_key = $4
			WHERE id = $1 AND listing_id = $2 AND object_key = $3 AND screening = 'passed'`,
			photo.PhotoID, photo.ListingID, photo.ObjectKey, newKey)
		if err != nil {
			return fmt.Errorf("replace listing photo key: %w", err)
		}
		replaced = tag.RowsAffected() == 1
		return nil
	})
	return replaced, err
}
