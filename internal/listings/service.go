package listings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type ListingService struct {
	store        ListingStore
	publishStore ListingPublishStore
	verifier     HostVerifier
	media        MediaStore
	objects      PhotoObjects
	screener     PhotoScreener
	inBackground func(func())
	now          func() time.Time
	newID        func() uuid.UUID
}

func NewListingService(store ListingStore) *ListingService {
	return &ListingService{
		store:        store,
		inBackground: func(work func()) { go work() },
		now:          time.Now,
		newID:        uuid.New,
	}
}

type CreateListingInput struct {
	Title            string
	Description      string
	Address          Address
	Type             ListingType
	PropertyType     PropertyType
	MaxGuests        int
	MinimumAgeToBook int
	Bedrooms         int
	Beds             int
	Bathrooms        float64
	Amenities        []Amenity
	Photos           []CreateListingPhotoInput
	Rules            []HouseRule
}

type CreateListingPhotoInput struct {
	ObjectKey string
	AltText   string
	Position  int
}

func (service *ListingService) CreateListing(
	ctx context.Context,
	hostID uuid.UUID,
	input CreateListingInput,
) (Listing, error) {
	input, err := validateCreateInput(input)
	if err != nil {
		return Listing{}, err
	}
	now := service.now().UTC()
	listing := Listing{
		ID:               service.newID(),
		HostID:           hostID,
		Title:            input.Title,
		Description:      input.Description,
		Address:          input.Address,
		Type:             input.Type,
		PropertyType:     input.PropertyType,
		MaxGuests:        input.MaxGuests,
		MinimumAgeToBook: input.MinimumAgeToBook,
		Bedrooms:         input.Bedrooms,
		Beds:             input.Beds,
		Bathrooms:        input.Bathrooms,
		Amenities:        input.Amenities,
		Rules:            input.Rules,
		Status:           Draft,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	listing.Photos = []ListingPhoto{}

	err = service.store.Create(ctx, hostID.String(), listing)

	if err != nil {
		return Listing{}, err
	}

	return listing, nil
}

const maxRules = 50

var ErrInvalidListing = errors.New("invalid listing")

func validateCreateInput(input CreateListingInput) (CreateListingInput, error) {
	input = trimCreateInput(input)

	if err := validateCoreFields(input); err != nil {
		return input, err
	}
	if err := validateAddress(input.Address); err != nil {
		return input, err
	}
	if err := validateCapacity(input); err != nil {
		return input, err
	}
	if err := validateCollectionSizes(input); err != nil {
		return input, err
	}

	amenities, err := dedupeAmenities(input.Amenities)
	if err != nil {
		return input, err
	}
	input.Amenities = amenities

	if len(input.Photos) > 0 {
		return input, invalidListing("photos are added after the draft is created")
	}
	if err := validateRules(input.Rules); err != nil {
		return input, err
	}
	return input, nil
}

func trimCreateInput(input CreateListingInput) CreateListingInput {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.Address.Line1 = strings.TrimSpace(input.Address.Line1)
	input.Address.Line2 = strings.TrimSpace(input.Address.Line2)
	input.Address.City = strings.TrimSpace(input.Address.City)
	input.Address.Region = strings.TrimSpace(input.Address.Region)
	input.Address.PostalCode = strings.TrimSpace(input.Address.PostalCode)
	input.Address.CountryCode = strings.ToUpper(strings.TrimSpace(input.Address.CountryCode))
	return input
}

func validateCoreFields(input CreateListingInput) error {
	if len(input.Title) < 4 || len(input.Title) > 120 {
		return invalidListing("title must be 4-120 characters")
	}
	if len(input.Description) < 20 || len(input.Description) > 5000 {
		return invalidListing("description must be 20-5000 characters")
	}
	if !validListingType(input.Type) || !validPropertyType(input.PropertyType) {
		return invalidListing("unsupported listing or property type")
	}
	return nil
}

func validateAddress(address Address) error {
	if address.Line1 == "" || address.City == "" || len(address.CountryCode) != 2 {
		return invalidListing("line1, city, and a two-letter country_code are required")
	}
	if address.Latitude < -90 || address.Latitude > 90 || address.Longitude < -180 || address.Longitude > 180 {
		return invalidListing("latitude or longitude is out of range")
	}
	return nil
}

func validateCapacity(input CreateListingInput) error {
	if input.MaxGuests < 1 || input.MaxGuests > 50 {
		return invalidListing("max_guests must be between 1 and 50")
	}
	if input.MinimumAgeToBook < 18 || input.MinimumAgeToBook > 99 {
		return invalidListing("minimum_age_to_book must be between 18 and 99")
	}
	if input.Bedrooms < 0 || input.Bedrooms > 50 || input.Beds < 1 || input.Beds > 100 || input.Bathrooms < 0.5 || input.Bathrooms > 50 {
		return invalidListing("bedroom, bed, or bathroom count is out of range")
	}
	return nil
}

func validateCollectionSizes(input CreateListingInput) error {
	if len(input.Amenities) > 100 || len(input.Rules) > maxRules {
		return invalidListing("too many amenities or rules")
	}
	return nil
}

func dedupeAmenities(input []Amenity) ([]Amenity, error) {
	seen := map[Amenity]struct{}{}
	amenities := make([]Amenity, 0, len(input))
	for _, amenity := range input {
		if !validAmenity(amenity) {
			return nil, invalidListing("unknown amenity %q", amenity)
		}
		if _, duplicate := seen[amenity]; duplicate {
			continue
		}
		seen[amenity] = struct{}{}
		amenities = append(amenities, amenity)
	}
	return amenities, nil
}

func validateRules(rules []HouseRule) error {
	for index := range rules {
		rules[index].Title = strings.TrimSpace(rules[index].Title)
		rules[index].Description = strings.TrimSpace(rules[index].Description)
		if rules[index].Title == "" || len(rules[index].Title) > 120 || len(rules[index].Description) > 1000 {
			return invalidListing("house rule is invalid")
		}
	}
	return nil
}

func invalidListing(format string, values ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidListing, fmt.Sprintf(format, values...))
}
func validListingType(value ListingType) bool {
	return value == EntirePlace || value == PrivateRoom || value == SharedRoom
}
func IsPropertyType(value PropertyType) bool { return validPropertyType(value) }

func validPropertyType(value PropertyType) bool {
	return slices.Contains(
		[]PropertyType{House, Apartment, Condo, Townhouse, Villa, Cabin, Cottage, Unique},
		value,
	)
}

var (
	ErrListingNotFound      = errors.New("listing not found")
	ErrListingInUse         = errors.New("this listing cannot be deleted")
	ErrDuplicateListing     = errors.New("a published listing already exists at this address")
	ErrNotListingOwner      = errors.New("only the host can publish this listing")
	ErrVerificationRequired = errors.New("a current ID verification is required to publish a listing")
	ErrPriceRequired        = errors.New("set a nightly price before publishing")
	ErrPhotoRequired        = errors.New("add at least 5 photos before publishing")
	ErrUploadsDisabled      = errors.New("photo uploads are not available")
	ErrPhotoRejected        = errors.New("photo must be a JPEG, PNG or WebP image up to 10 MB")
	ErrPhotoNotAllowed      = errors.New("that photo cannot be used")
	ErrPhotoCheckFailed     = errors.New("we could not check that photo right now, try again")
	ErrPhotoTooLargeToCheck = errors.New("photo must be 7 MB or smaller")
	ErrInvalidPhotoKey      = errors.New("photo key is not valid for this listing")
	ErrPhotoNotUploaded     = errors.New("photo was not uploaded")
	ErrPhotoAlreadyAdded    = errors.New("that photo is already on the listing")
	ErrDuplicatePhoto       = errors.New("it is already on your listing")
	ErrPhotoOrderMismatch   = errors.New("the photo order must list each photo on the listing once")
	ErrTooManyPhotos        = errors.New("a listing can have up to 20 photos")
	ErrPhotoNotFound        = errors.New("photo not found")
	ErrLastPhoto            = errors.New("a published listing needs at least 5 photos")
	ErrPhotosRefused        = errors.New("some photos cannot be used, remove them to publish")
	ErrInvalidPrice         = errors.New("nightly price must be between $1 and $100,000")
)

type ListingPublishStore interface {
	Publish(ctx context.Context, userID string, listingID uuid.UUID) error
}

type HostVerifier interface {
	CurrentVerificationID(ctx context.Context, accountID uuid.UUID) (uuid.UUID, bool, error)
}

func (service *ListingService) WithPublishing(store ListingPublishStore, verifier HostVerifier) *ListingService {
	service.publishStore = store
	service.verifier = verifier
	return service
}

func (service *ListingService) PublishListing(ctx context.Context, hostID, listingID uuid.UUID) error {
	if service.publishStore == nil || service.verifier == nil {
		return ErrVerificationRequired
	}
	_, verified, err := service.verifier.CurrentVerificationID(ctx, hostID)
	if err != nil {
		return fmt.Errorf("check host verification: %w", err)
	}
	if !verified {
		return ErrVerificationRequired
	}
	if err := service.screenPendingPhotos(ctx, hostID, listingID); err != nil {
		return err
	}
	return service.publishStore.Publish(ctx, hostID.String(), listingID)
}

type ReusedPhotoSigner struct {
	signer PhotoSigner
	mu     sync.Mutex
	links  map[reusedPhotoKey]reusedPhotoLink
	now    func() time.Time
}

func NewReusedPhotoSigner(signer PhotoSigner) *ReusedPhotoSigner {
	return &ReusedPhotoSigner{signer: signer, links: make(map[reusedPhotoKey]reusedPhotoLink), now: time.Now}
}

func (reused *ReusedPhotoSigner) PresignDownload(ctx context.Context, key string, lifetime time.Duration) (string, error) {
	reused.mu.Lock()
	defer reused.mu.Unlock()

	wanted := reusedPhotoKey{objectKey: key, lifetime: lifetime}
	now := reused.now()
	if link, found := reused.links[wanted]; found && now.Before(link.reuseUntil) {
		return link.url, nil
	}
	url, err := reused.signer.PresignDownload(ctx, key, lifetime)
	if err != nil {
		return "", err
	}
	if len(reused.links) >= maxReusedPhotoLinks {
		reused.makeRoom(now)
	}
	reused.links[wanted] = reusedPhotoLink{url: url, reuseUntil: now.Add(lifetime / 2)}
	return url, nil
}

func (reused *ReusedPhotoSigner) makeRoom(now time.Time) {
	for key, link := range reused.links {
		if !now.Before(link.reuseUntil) {
			delete(reused.links, key)
		}
	}
	if len(reused.links) >= maxReusedPhotoLinks {
		clear(reused.links)
	}
}

const (
	photoURLLifetime      = time.Hour
	publicCoordinateScale = 100.0
)

type PublicListingService struct {
	reader PublishedListingReader
	signer PhotoSigner
}

func NewPublicListingService(reader PublishedListingReader, signer PhotoSigner) *PublicListingService {
	return &PublicListingService{reader: reader, signer: signer}
}

func (service *PublicListingService) ListPublished(ctx context.Context) ([]PublicListing, error) {
	listings, err := service.reader.ListPublished(ctx)
	if err != nil {
		return nil, err
	}
	for index := range listings {
		if err := service.prepare(ctx, &listings[index]); err != nil {
			return nil, err
		}
	}
	return listings, nil
}

func (service *PublicListingService) GetPublished(ctx context.Context, id uuid.UUID) (PublicListing, error) {
	listing, err := service.reader.GetPublished(ctx, id)
	if err != nil {
		return PublicListing{}, err
	}
	if err := service.prepare(ctx, &listing); err != nil {
		return PublicListing{}, err
	}
	return listing, nil
}

func (service *PublicListingService) prepare(ctx context.Context, listing *PublicListing) error {
	listing.Location.Latitude = roundCoordinate(listing.Location.Latitude)
	listing.Location.Longitude = roundCoordinate(listing.Location.Longitude)
	if service.signer == nil {
		return nil
	}
	for index := range listing.Photos {
		url, err := service.signer.PresignDownload(ctx, listing.Photos[index].ObjectKey, photoURLLifetime)
		if err != nil {
			return fmt.Errorf("sign photo for listing %s: %w", listing.ID, err)
		}
		listing.Photos[index].URL = url
		cardKey, sized := cardKeyOf(listing.Photos[index].ObjectKey)
		if !sized {
			continue
		}
		cardURL, err := service.signer.PresignDownload(ctx, cardKey, photoURLLifetime)
		if err != nil {
			return fmt.Errorf("sign card photo for listing %s: %w", listing.ID, err)
		}
		listing.Photos[index].CardURL = cardURL
	}
	return nil
}

func roundCoordinate(value float64) float64 {
	return math.Round(value*publicCoordinateScale) / publicCoordinateScale
}

const (
	maxPhotoBytes               = 10 << 20
	maxPhotos                   = 20
	minPublishPhotos            = 5
	maxAltTextLength            = 500
	minNightlyCents             = 100
	maxNightlyCents             = 10_000_000
	photoUploadLifetime         = 10 * time.Minute
	maxScreenedPhotoBytes       = 7 * 1024 * 1024
	backgroundScreeningDeadline = 2 * time.Minute
)

var allowedPhotoTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

var (
	uploadNamePattern = regexp.MustCompile(`^[0-9a-f-]{36}\.(jpg|png|webp)$`)
	storedNamePattern = regexp.MustCompile(`^[0-9a-f-]{36}(\.sized\.jpg|\.(jpg|png|webp))$`)
	resizeSlots       = make(chan struct{}, maxParallelResizes)
)

func photoKeyPrefix(listingID uuid.UUID) string {
	return "listings/" + listingID.String() + "/"
}

func validPhotoKey(key string, listingID uuid.UUID) bool {
	name, owned := strings.CutPrefix(key, photoKeyPrefix(listingID))
	return owned && storedNamePattern.MatchString(name)
}

func validUploadKey(key string, listingID uuid.UUID) bool {
	name, owned := strings.CutPrefix(key, photoKeyPrefix(listingID))
	return owned && uploadNamePattern.MatchString(name)
}

func serverPhotoBase(listingID uuid.UUID, uploadKey string) string {
	return photoKeyPrefix(listingID) + uuid.NewSHA1(listingID, []byte(uploadKey)).String()
}

func cardKeyOf(storedKey string) (string, bool) {
	base, sized := strings.CutSuffix(storedKey, sizedPhotoSuffix)
	if !sized {
		return "", false
	}
	return base + cardPhotoSuffix, true
}

func (service *ListingService) storeResized(ctx context.Context, listingID uuid.UUID, uploadKey string, data []byte) (string, error) {
	card := boundedCardVersionOf(data)
	if card == nil {
		return "", nil
	}
	sizedKey := serverPhotoBase(listingID, uploadKey) + sizedPhotoSuffix
	if err := writeSizedPhoto(ctx, service.objects, sizedKey, data, card); err != nil {
		return "", err
	}
	return sizedKey, nil
}

func boundedCardVersionOf(data []byte) []byte {
	resizeSlots <- struct{}{}
	defer func() { <-resizeSlots }()
	return cardVersionOf(data)
}

func writeSizedPhoto(ctx context.Context, files PhotoFileWriter, sizedKey string, original, card []byte) error {
	cardKey, _ := cardKeyOf(sizedKey)
	if err := files.Write(ctx, cardKey, "image/jpeg", card); err != nil {
		return fmt.Errorf("store card photo: %w", err)
	}
	if err := files.Write(ctx, sizedKey, "image/jpeg", original); err != nil {
		removePhotoFiles(ctx, files, cardKey)
		return fmt.Errorf("store sized photo: %w", err)
	}
	return nil
}

func removePhotoFiles(ctx context.Context, files PhotoFileWriter, keys ...string) {
	for _, key := range keys {
		if err := files.Remove(ctx, key); err != nil {
			slog.Warn("Remove listing photo", "error", err)
		}
	}
}

func (service *ListingService) WithScreening(screener PhotoScreener) *ListingService {
	service.screener = screener
	return service
}

func (service *ListingService) WithMedia(store MediaStore, objects PhotoObjects) *ListingService {
	service.media = store
	service.objects = objects
	return service
}

func (service *ListingService) Setup(ctx context.Context, hostID, listingID uuid.UUID) (Setup, error) {
	setup, err := service.media.Setup(ctx, hostID.String(), listingID)
	if err != nil {
		return Setup{}, err
	}
	if service.objects == nil {
		return setup, nil
	}
	for index := range setup.Photos {
		url, err := service.objects.PresignDownload(ctx, setup.Photos[index].ObjectKey, photoURLLifetime)
		if err != nil {
			return Setup{}, fmt.Errorf("sign listing photo: %w", err)
		}
		setup.Photos[index].URL = url
	}
	return setup, nil
}

func (service *ListingService) RequestPhotoUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (PhotoTicket, error) {
	if service.objects == nil {
		return PhotoTicket{}, ErrUploadsDisabled
	}
	extension, allowed := allowedPhotoTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxPhotoBytes {
		return PhotoTicket{}, ErrPhotoRejected
	}
	setup, err := service.media.Setup(ctx, hostID.String(), listingID)
	if err != nil {
		return PhotoTicket{}, err
	}
	if len(setup.Photos) >= maxPhotos {
		return PhotoTicket{}, ErrTooManyPhotos
	}
	key := photoKeyPrefix(listingID) + service.newID().String() + extension
	url, err := service.objects.PresignUpload(ctx, key, contentType, photoUploadLifetime)
	if err != nil {
		return PhotoTicket{}, fmt.Errorf("presign listing photo: %w", err)
	}
	return PhotoTicket{
		UploadURL:   url,
		PhotoKey:    key,
		ContentType: contentType,
		ExpiresIn:   int(photoUploadLifetime.Seconds()),
		MaxBytes:    maxPhotoBytes,
	}, nil
}

func (service *ListingService) AddPhoto(ctx context.Context, hostID, listingID uuid.UUID, key, altText string) (Setup, error) {
	if service.objects == nil {
		return Setup{}, ErrUploadsDisabled
	}
	if !validUploadKey(key, listingID) {
		return Setup{}, ErrInvalidPhotoKey
	}
	altText = strings.TrimSpace(altText)
	if len(altText) > maxAltTextLength {
		return Setup{}, invalidListing("photo description must be %d characters or fewer", maxAltTextLength)
	}
	current, err := service.media.Setup(ctx, hostID.String(), listingID)
	if err != nil {
		return Setup{}, err
	}
	resizedKey := serverPhotoBase(listingID, key) + sizedPhotoSuffix
	if slices.ContainsFunc(current.Photos, func(photo ListingPhoto) bool {
		return photo.ObjectKey == key || photo.ObjectKey == resizedKey
	}) {
		return Setup{}, ErrPhotoAlreadyAdded
	}
	if len(current.Photos) >= maxPhotos {
		return Setup{}, ErrTooManyPhotos
	}
	info, err := service.objects.Stat(ctx, key)
	if err != nil {
		return Setup{}, ErrPhotoNotUploaded
	}
	if _, allowed := allowedPhotoTypes[info.ContentType]; !allowed || info.Size <= 0 || info.Size > maxPhotoBytes {
		service.discard(ctx, key)
		return Setup{}, ErrPhotoRejected
	}
	if service.screener != nil && info.Size > maxScreenedPhotoBytes {
		service.discard(ctx, key)
		return Setup{}, ErrPhotoTooLargeToCheck
	}
	data, err := service.objects.Read(ctx, key, maxPhotoBytes)
	if err != nil {
		return Setup{}, fmt.Errorf("%w: read upload: %w", ErrPhotoCheckFailed, err)
	}
	fingerprint := fingerprintOf(data)
	if service.alreadyListed(ctx, fingerprint, current.Photos) {
		service.discard(ctx, key)
		return Setup{}, ErrDuplicatePhoto
	}
	photo := ListingPhoto{ID: service.newID(), ObjectKey: key, AltText: altText, Screening: ScreeningPassed, Fingerprint: fingerprint}
	if service.screener != nil {
		photo.Screening = ScreeningPending
	} else if sizedKey, err := service.storeResized(ctx, listingID, key, data); err != nil {
		slog.Warn("Resize listing photo", "error", err)
	} else if sizedKey != "" {
		photo.ObjectKey = sizedKey
	}
	if err := service.media.AddPhoto(ctx, hostID.String(), listingID, photo); err != nil {
		if photo.ObjectKey != key {
			service.discardUnlessListed(ctx, hostID, listingID, photo.ObjectKey)
		}
		if errors.Is(err, ErrDuplicatePhoto) {
			service.discard(ctx, key)
		}
		return Setup{}, err
	}
	if photo.ObjectKey != key {
		service.discard(ctx, key)
	}
	if photo.Screening == ScreeningPending {
		service.inBackground(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundScreeningDeadline)
			defer cancel()
			if err := service.screenPhoto(ctx, hostID, listingID, photo); err != nil {
				slog.Warn("background listing photo screening", "error", err)
			}
		})
	}
	return service.Setup(ctx, hostID, listingID)
}

func (service *ListingService) ImportPhoto(ctx context.Context, hostID, listingID uuid.UUID, contentType string, data []byte) error {
	if service.objects == nil {
		return ErrUploadsDisabled
	}
	extension, allowed := allowedPhotoTypes[contentType]
	if !allowed || len(data) == 0 || len(data) > maxPhotoBytes {
		return ErrPhotoRejected
	}
	if _, err := service.media.Setup(ctx, hostID.String(), listingID); err != nil {
		return err
	}
	key := photoKeyPrefix(listingID) + service.newID().String() + extension
	if err := service.objects.Write(ctx, key, contentType, data); err != nil {
		return fmt.Errorf("store imported photo: %w", err)
	}
	if _, err := service.AddPhoto(ctx, hostID, listingID, key, ""); err != nil {
		service.discard(ctx, key)
		return err
	}
	return nil
}

func (service *ListingService) ReorderPhotos(ctx context.Context, hostID, listingID uuid.UUID, photoIDs []uuid.UUID) (Setup, error) {
	if service.media == nil {
		return Setup{}, ErrUploadsDisabled
	}
	if len(photoIDs) == 0 || len(photoIDs) > maxPhotos || hasRepeats(photoIDs) {
		return Setup{}, ErrPhotoOrderMismatch
	}
	if err := service.media.ReorderPhotos(ctx, hostID.String(), listingID, photoIDs); err != nil {
		return Setup{}, err
	}
	return service.Setup(ctx, hostID, listingID)
}

func hasRepeats(ids []uuid.UUID) bool {
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return true
		}
		seen[id] = true
	}
	return false
}

func (service *ListingService) alreadyListed(ctx context.Context, fingerprint PhotoFingerprint, listed []ListingPhoto) bool {
	if fingerprint == noFingerprint {
		return false
	}
	known := make([]PhotoFingerprint, len(listed))
	group, groupCtx := errgroup.WithContext(ctx)
	for index, photo := range listed {
		known[index] = photo.Fingerprint
		if photo.Fingerprint != noFingerprint {
			continue
		}
		group.Go(func() error {
			data, err := service.objects.Read(groupCtx, photo.ObjectKey, maxPhotoBytes)
			if err == nil {
				known[index] = fingerprintOf(data)
			}
			return nil
		})
	}
	_ = group.Wait()
	return slices.ContainsFunc(known, fingerprint.sameImageAs)
}

func (service *ListingService) screenPhoto(ctx context.Context, hostID, listingID uuid.UUID, photo ListingPhoto) error {
	verdict := ScreeningVerdict{PhotoID: photo.ID, UploadKey: photo.ObjectKey}
	screenedKey, err := service.screenedCopy(ctx, listingID, photo.ObjectKey)
	switch {
	case errors.Is(err, ErrPhotoRejected):
		verdict.Refusal = RefusedNotAPhoto
	case errors.Is(err, ErrPhotoNotAllowed):
		verdict.Refusal = refusalOf(err)
	case err != nil:
		return err
	default:
		verdict.ScreenedKey = screenedKey
	}
	recorded, err := service.media.RecordScreening(ctx, hostID.String(), listingID, verdict)
	if err != nil {
		return fmt.Errorf("record photo screening: %w", err)
	}
	if !recorded {
		service.discardUnlessListed(ctx, hostID, listingID, screenedKey)
		return nil
	}
	if verdict.ScreenedKey != "" {
		service.discard(ctx, photo.ObjectKey)
	}
	return nil
}

func (service *ListingService) discardUnlessListed(ctx context.Context, hostID, listingID uuid.UUID, key string) {
	if key == "" {
		return
	}
	setup, err := service.media.Setup(ctx, hostID.String(), listingID)
	if err != nil {
		return
	}
	if !slices.ContainsFunc(setup.Photos, func(photo ListingPhoto) bool { return photo.ObjectKey == key }) {
		service.discard(ctx, key)
	}
}

func (service *ListingService) screenPendingPhotos(ctx context.Context, hostID, listingID uuid.UUID) error {
	if service.media == nil || service.screener == nil {
		return nil
	}
	setup, err := service.media.Setup(ctx, hostID.String(), listingID)
	if err != nil {
		return err
	}
	group, groupCtx := errgroup.WithContext(ctx)
	for _, photo := range setup.Photos {
		if photo.Screening != ScreeningPending {
			continue
		}
		group.Go(func() error { return service.screenPhoto(groupCtx, hostID, listingID, photo) })
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("%w: %w", ErrPhotoCheckFailed, err)
	}
	return nil
}

func (service *ListingService) screenedCopy(ctx context.Context, listingID uuid.UUID, key string) (string, error) {
	data, err := service.objects.Read(ctx, key, maxScreenedPhotoBytes)
	if err != nil {
		return "", fmt.Errorf("%w: read upload: %w", ErrPhotoCheckFailed, err)
	}
	contentType := http.DetectContentType(data)
	extension, allowed := allowedPhotoTypes[contentType]
	if !allowed {
		return "", ErrPhotoRejected
	}
	refusal, err := service.screener.Screen(ctx, data, contentType)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPhotoCheckFailed, err)
	}
	if refusal != "" {
		return "", photoNotAllowed{refusal}
	}
	sizedKey, err := service.storeResized(ctx, listingID, key, data)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPhotoCheckFailed, err)
	}
	if sizedKey != "" {
		return sizedKey, nil
	}
	screenedKey := serverPhotoBase(listingID, key) + extension
	if err := service.objects.Write(ctx, screenedKey, contentType, data); err != nil {
		return "", fmt.Errorf("%w: store checked photo: %w", ErrPhotoCheckFailed, err)
	}
	return screenedKey, nil
}

type photoNotAllowed struct{ refusal PhotoRefusal }

func (refused photoNotAllowed) Error() string {
	return ErrPhotoNotAllowed.Error() + ": " + string(refused.refusal)
}

func (refused photoNotAllowed) Unwrap() error { return ErrPhotoNotAllowed }

func refusalOf(err error) PhotoRefusal {
	var refused photoNotAllowed
	if errors.As(err, &refused) {
		return refused.refusal
	}
	return RefusedUnsafe
}

func (service *ListingService) RemovePhoto(ctx context.Context, hostID, listingID, photoID uuid.UUID) (Setup, error) {
	key, err := service.media.RemovePhoto(ctx, hostID.String(), listingID, photoID)
	if err != nil {
		return Setup{}, err
	}
	if validPhotoKey(key, listingID) {
		service.discard(ctx, key)
	}
	return service.Setup(ctx, hostID, listingID)
}

func (service *ListingService) SetPrice(ctx context.Context, hostID, listingID uuid.UUID, nightlyCents int) (Setup, error) {
	if nightlyCents < minNightlyCents || nightlyCents > maxNightlyCents {
		return Setup{}, ErrInvalidPrice
	}
	if err := service.media.SetPrice(ctx, hostID.String(), listingID, nightlyCents); err != nil {
		return Setup{}, err
	}
	return service.Setup(ctx, hostID, listingID)
}

func (service *ListingService) SetRules(ctx context.Context, hostID, listingID uuid.UUID, rules []HouseRule) (Setup, error) {
	if len(rules) > maxRules {
		return Setup{}, invalidListing("keep to %d house rules", maxRules)
	}
	if err := validateRules(rules); err != nil {
		return Setup{}, err
	}
	if err := service.media.SetRules(ctx, hostID.String(), listingID, rules); err != nil {
		return Setup{}, err
	}
	return service.Setup(ctx, hostID, listingID)
}

func (service *ListingService) discard(ctx context.Context, key string) {
	if service.objects == nil {
		return
	}
	keys := []string{key}
	if cardKey, sized := cardKeyOf(key); sized {
		keys = append(keys, cardKey)
	}
	removePhotoFiles(ctx, service.objects, keys...)
}

func (service *ListingService) DeleteListing(ctx context.Context, hostID, listingID uuid.UUID) error {
	remover, ok := service.store.(ListingDeleteStore)
	if !ok {
		return errors.New("listing store cannot delete")
	}
	keys, err := remover.DeleteUntouched(ctx, hostID.String(), listingID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if validPhotoKey(key, listingID) {
			service.discard(ctx, key)
		}
	}
	return nil
}
