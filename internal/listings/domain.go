package listings

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
)

// T Y P E S --------------------------------------- //

type Address struct {
	Line1       string  `json:"line1"`
	Line2       string  `json:"line2,omitempty"`
	City        string  `json:"city"`
	Region      string  `json:"region,omitempty"`
	PostalCode  string  `json:"postal_code,omitempty"`
	CountryCode string  `json:"country_code"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	PlaceID     string  `json:"place_id,omitempty"`
}

type Listing struct {
	ID               uuid.UUID      `json:"id"`
	HostID           uuid.UUID      `json:"host_id"`
	Title            string         `json:"title"`
	Description      string         `json:"description"`
	Address          Address        `json:"address"`
	Type             ListingType    `json:"type"`
	PropertyType     PropertyType   `json:"property_type"`
	MaxGuests        int            `json:"max_guests"`
	MinimumAgeToBook int            `json:"minimum_age_to_book"`
	Bedrooms         int            `json:"bedrooms"`
	Beds             int            `json:"beds"`
	Bathrooms        float64        `json:"bathrooms"`
	Amenities        []Amenity      `json:"amenities"`
	Photos           []ListingPhoto `json:"photos"`
	Rules            []HouseRule    `json:"rules"`
	Status           ListingStatus  `json:"status"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	ReviewCount      int            `json:"review_count"`
	Reviews          []Review       `json:"reviews,omitempty"`
}

type ListingPhoto struct {
	ID        uuid.UUID      `json:"id"`
	ObjectKey string         `json:"-"`
	URL       string         `json:"url,omitempty"`
	CardURL   string         `json:"card_url,omitempty"`
	AltText   string         `json:"alt_text,omitempty"`
	Position  int            `json:"position"`
	Screening PhotoScreening `json:"screening"`
	Refusal   PhotoRefusal   `json:"refusal,omitempty"`

	Fingerprint PhotoFingerprint `json:"-"`
}

type PhotoScreening string

const (
	ScreeningPending PhotoScreening = "pending"
	ScreeningPassed  PhotoScreening = "passed"
	ScreeningRefused PhotoScreening = "refused"
)

type HouseRule struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type Amenity string

const (
	// Common amenities.
	BBQArea                Amenity = "bbq_area"
	FreeParking            Amenity = "free_parking"
	OutdoorKitchen         Amenity = "outdoor_kitchen"
	Washer                 Amenity = "washer"
	Dryer                  Amenity = "dryer"
	CarbonMonoxideDetector Amenity = "carbon_monoxide_detector"
	FireExtinguisher       Amenity = "fire_extinguisher"
	FirstAidKit            Amenity = "first_aid_kit"
	CleaningSupplies       Amenity = "cleaning_supplies"
	Dishwasher             Amenity = "dishwasher"
	StreetParking          Amenity = "street_parking"
	HairDryer              Amenity = "hair_dryer"
	HotWater               Amenity = "hot_water"
	AirConditioning        Amenity = "air_conditioning"

	// Bathrooms.
	Bathtub     Amenity = "bathtub"
	ShowerGel   Amenity = "shower_gel"
	BodySoap    Amenity = "body_soap"
	BathLinens  Amenity = "bath_linens"
	Conditioner Amenity = "conditioner"
	Shampoo     Amenity = "shampoo"

	// Patio.
	BBQUtensils    Amenity = "bbq_utensils"
	FirePit        Amenity = "fire_pit"
	OutdoorSeating Amenity = "outdoor_seating"
	PatioAmenity   Amenity = "patio"

	// Kitchen.
	BakingSheet    Amenity = "baking_sheet"
	Blender        Amenity = "blender"
	Coffee         Amenity = "coffee"
	CoffeeMaker    Amenity = "coffee_maker"
	CookingBasics  Amenity = "cooking_basics"
	Dishes         Amenity = "dishes"
	Freezer        Amenity = "freezer"
	IceMaker       Amenity = "ice_maker"
	Kettle         Amenity = "kettle"
	KitchenAmenity Amenity = "kitchen"
	Microwave      Amenity = "microwave"
	MiniFridge     Amenity = "mini_fridge"
	Oven           Amenity = "oven"
	Fridge         Amenity = "fridge"
	Stove          Amenity = "stove"
	Toaster        Amenity = "toaster"
	WineGlasses    Amenity = "wine_glasses"

	// Living room.
	BoardGames Amenity = "board_games"
	CeilingFan Amenity = "ceiling_fan"
	Heating    Amenity = "heating"
	Fireplace  Amenity = "fireplace"
	TV         Amenity = "tv"

	// Office and dining room.
	DedicatedWorkspace Amenity = "dedicated_workspace"
	DiningTable        Amenity = "dining_table"
	HighChair          Amenity = "high_chair"

	// Property areas.
	GarageAmenity Amenity = "garage"
	GardenAmenity Amenity = "garden"
	HotTub        Amenity = "hot_tub"
	OutdoorPool   Amenity = "outdoor_pool"

	// Common area and laundry room.
	WiFi                 Amenity = "wifi"
	LongTermStaysAllowed Amenity = "long_term_stays_allowed"
	SmokeDetector        Amenity = "smoke_detector"
	Iron                 Amenity = "iron"
	LaundromatNearby     Amenity = "laundromat_nearby"

	// Entry and exterior.
	LuggageDropOffAllowed Amenity = "luggage_drop_off_allowed"
	WheelchairAccessible  Amenity = "wheelchair_accessible"

	// Bedrooms.
	BedLinens    Amenity = "bed_linens"
	Safe         Amenity = "safe"
	Closet       Amenity = "closet"
	ExtraPillows Amenity = "extra_pillows"
	Hangers      Amenity = "hangers"
)

var CurrentAmenities = []Amenity{
	BBQArea, FreeParking, OutdoorKitchen, Washer, Dryer,
	CarbonMonoxideDetector, FireExtinguisher, FirstAidKit, CleaningSupplies,
	Dishwasher, StreetParking, HairDryer, HotWater, AirConditioning,
	Bathtub, ShowerGel, BodySoap, BathLinens, Conditioner, Shampoo,
	BBQUtensils, FirePit, OutdoorSeating, PatioAmenity,
	BakingSheet, Blender, Coffee, CoffeeMaker, CookingBasics, Dishes,
	Freezer, IceMaker, Kettle, KitchenAmenity, Microwave, MiniFridge,
	Oven, Fridge, Stove, Toaster, WineGlasses,
	BoardGames, CeilingFan, Heating, Fireplace, TV,
	DedicatedWorkspace, DiningTable, HighChair,
	GarageAmenity, GardenAmenity, HotTub, OutdoorPool,
	WiFi, LongTermStaysAllowed, SmokeDetector, Iron, LaundromatNearby,
	LuggageDropOffAllowed, WheelchairAccessible,
	BedLinens, Safe, Closet, ExtraPillows, Hangers,
}

func validAmenity(value Amenity) bool {
	return slices.Contains(CurrentAmenities, value)
}

type PropertyType string

const (
	House     PropertyType = "house"
	Apartment PropertyType = "apartment"
	Condo     PropertyType = "condo"
	Townhouse PropertyType = "townhouse"
	Villa     PropertyType = "villa"
	Cabin     PropertyType = "cabin"
	Cottage   PropertyType = "cottage"
	Unique    PropertyType = "unique"
)

type ListingStatus string

const (
	Draft     ListingStatus = "draft"
	Published ListingStatus = "published"
	Paused    ListingStatus = "paused"
)

type ListingType string

const (
	EntirePlace ListingType = "entire_place"
	PrivateRoom ListingType = "private_room"
	SharedRoom  ListingType = "shared_room"
)

type Review struct {
	ID        uuid.UUID `json:"id"`
	ListingID uuid.UUID `json:"listing_id"`
	BookingID uuid.UUID `json:"booking_id"`
	GuestID   uuid.UUID `json:"guest_id"`

	Rating       int    `json:"rating"`
	Comment      string `json:"comment"`
	LengthOfStay string `json:"length_of_stay"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PublicLocation struct {
	City        string  `json:"city"`
	Region      string  `json:"region,omitempty"`
	CountryCode string  `json:"country_code"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type ListingPrice struct {
	NightlyCents     int    `json:"nightly_cents"`
	CleaningFeeCents int    `json:"cleaning_fee_cents"`
	Currency         string `json:"currency"`
}

type ListingRating struct {
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

type PublicListing struct {
	ID            uuid.UUID      `json:"id"`
	Slug          string         `json:"slug,omitempty"`
	Title         string         `json:"title"`
	Summary       string         `json:"summary,omitempty"`
	Description   string         `json:"description"`
	HostName      string         `json:"host_name,omitempty"`
	HostAvatarURL string         `json:"host_avatar_url,omitempty"`
	Location      PublicLocation `json:"location"`
	Type          ListingType    `json:"type"`
	PropertyType  PropertyType   `json:"property_type"`
	MaxGuests     int            `json:"max_guests"`
	Bedrooms      int            `json:"bedrooms"`
	Beds          int            `json:"beds"`
	Bathrooms     float64        `json:"bathrooms"`
	MinimumNights int            `json:"minimum_nights"`
	CheckInTime   string         `json:"check_in_time"`
	CheckOutTime  string         `json:"check_out_time"`
	Price         *ListingPrice  `json:"price,omitempty"`
	Rating        *ListingRating `json:"rating,omitempty"`
	Amenities     []Amenity      `json:"amenities"`
	Photos        []ListingPhoto `json:"photos"`
	Rules         []HouseRule    `json:"rules"`
	CreatedAt     time.Time      `json:"created_at"`
	PublishedAt   *time.Time     `json:"published_at,omitempty"`
}

type PublishedListingReader interface {
	ListPublished(ctx context.Context) ([]PublicListing, error)
	GetPublished(ctx context.Context, id uuid.UUID) (PublicListing, error)
}

type PhotoSigner interface {
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
}

type reusedPhotoKey struct {
	objectKey string
	lifetime  time.Duration
}

type reusedPhotoLink struct {
	url        string
	reuseUntil time.Time
}

const maxReusedPhotoLinks = 10_000

const (
	cardPhotoEdge      = 960
	cardPhotoQuality   = 80
	maxResizedPixels   = 40_000_000
	maxParallelResizes = 2
	sizedPhotoSuffix   = ".sized.jpg"
	cardPhotoSuffix    = ".card.jpg"
)

const (
	jpegSegmentExif    = 0xe1
	jpegSegmentProfile = 0xe2
	jpegStartOfScan    = 0xda
	jpegStartBytes     = 2
	exifOrientationTag = 0x0112
	exifShortType      = 3
	exifUpright        = 1
	tiffHeaderBytes    = 8
	tiffEntryBytes     = 12
)

var (
	exifHeader    = []byte("Exif\x00\x00")
	profileHeader = []byte("ICC_PROFILE\x00")
)

type jpegMetadata struct {
	colourProfile []byte
	upright       bool
}

type PublicListingReader interface {
	ListPublished(ctx context.Context) ([]PublicListing, error)
	GetPublished(ctx context.Context, id uuid.UUID) (PublicListing, error)
}

type HostListing struct {
	ID     uuid.UUID     `json:"id"`
	Title  string        `json:"title"`
	City   string        `json:"city"`
	Status ListingStatus `json:"status"`
}

type HostListingReader interface {
	ListForHost(ctx context.Context, hostID uuid.UUID) ([]HostListing, error)
}

type Setup struct {
	Status            ListingStatus  `json:"status"`
	NightlyPriceCents int            `json:"nightly_price_cents"`
	Photos            []ListingPhoto `json:"photos"`
}

type PhotoTicket struct {
	UploadURL   string `json:"upload_url"`
	PhotoKey    string `json:"photo_key"`
	ContentType string `json:"content_type"`
	ExpiresIn   int    `json:"expires_in_seconds"`
	MaxBytes    int64  `json:"max_bytes"`
}

type ObjectInfo struct {
	Size        int64
	ContentType string
}

type PhotoObjects interface {
	PresignUpload(ctx context.Context, key, contentType string, expires time.Duration) (string, error)
	PresignDownload(ctx context.Context, key string, expires time.Duration) (string, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	Remove(ctx context.Context, key string) error
	Read(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Write(ctx context.Context, key, contentType string, data []byte) error
}

type PhotoRefusal string

const (
	RefusedExplicit            PhotoRefusal = "it shows nudity or sexual content"
	RefusedViolent             PhotoRefusal = "it shows violence, weapons or drugs"
	RefusedHateful             PhotoRefusal = "it shows hateful or harassing content"
	RefusedPersonalInformation PhotoRefusal = "it shows personal documents or private details"
	RefusedContactDetails      PhotoRefusal = "it advertises contact or payment details"
	RefusedUnrelated           PhotoRefusal = "it does not show your house or a room inside it"
	RefusedUnsafe              PhotoRefusal = "it did not pass our safety check"
	RefusedNotAPhoto           PhotoRefusal = "it is not a JPEG, PNG or WebP photo"
)

type ScreeningVerdict struct {
	PhotoID     uuid.UUID
	UploadKey   string
	ScreenedKey string
	Refusal     PhotoRefusal
}

type PhotoScreener interface {
	Screen(ctx context.Context, photo []byte, contentType string) (PhotoRefusal, error)
}

type MediaStore interface {
	Setup(ctx context.Context, userID string, listingID uuid.UUID) (Setup, error)
	AddPhoto(ctx context.Context, userID string, listingID uuid.UUID, photo ListingPhoto) error
	RemovePhoto(ctx context.Context, userID string, listingID, photoID uuid.UUID) (string, error)
	ReorderPhotos(ctx context.Context, userID string, listingID uuid.UUID, photoIDs []uuid.UUID) error
	RecordScreening(ctx context.Context, userID string, listingID uuid.UUID, verdict ScreeningVerdict) (bool, error)
	SetPrice(ctx context.Context, userID string, listingID uuid.UUID, nightlyCents int) error
}

type ListingDeleteStore interface {
	DeleteUntouched(ctx context.Context, userID string, listingID uuid.UUID) ([]string, error)
}

type ListingDeleter interface {
	DeleteListing(ctx context.Context, hostID, listingID uuid.UUID) error
}

type PhotoToResize struct {
	HostID    uuid.UUID
	ListingID uuid.UUID
	PhotoID   uuid.UUID
	ObjectKey string
}

type PhotoKeyChange struct {
	PhotoID uuid.UUID
	From    string
	To      string
}

type BackfillReport struct {
	Photos        int
	AlreadySized  int
	KeptOriginal  int
	Resizable     int
	Resized       int
	Failed        int
	OriginalBytes int
	CardBytes     int
	Changes       []PhotoKeyChange
}

type CardBackfillStore interface {
	PublishedPhotos(ctx context.Context) ([]PhotoToResize, error)
	ReplacePhotoKey(ctx context.Context, photo PhotoToResize, newKey string) (bool, error)
}

type PhotoFileReader interface {
	Read(ctx context.Context, key string, maxBytes int64) ([]byte, error)
}

type PhotoFileWriter interface {
	Write(ctx context.Context, key, contentType string, data []byte) error
	Remove(ctx context.Context, key string) error
}

type CardBackfillFiles interface {
	PhotoFileReader
	PhotoFileWriter
}
