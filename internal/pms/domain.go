package pms

import (
	"context"
	"errors"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
)

type ProviderName string

const (
	Lodgify    ProviderName = "lodgify"
	Guesty     ProviderName = "guesty"
	Hostaway   ProviderName = "hostaway"
	Hospitable ProviderName = "hospitable"
	OwnerRez   ProviderName = "ownerrez"
)

var signInProviders = []ProviderName{Hospitable, OwnerRez}

const (
	purposeState = "state"
	purposePass  = "pass"
)

type Authorizer interface {
	AuthorizeURL(state string) string
	Exchange(ctx context.Context, code string) (string, error)
}

type App struct {
	Provider     ProviderName
	ClientSecret string
	Authorizer   Authorizer
	Source       Source
}

type signInApp struct {
	authorizer Authorizer
	seal       sealer
}

type sealed struct {
	Purpose  string       `json:"purpose"`
	Provider ProviderName `json:"provider"`
	Host     uuid.UUID    `json:"host"`
	Expires  time.Time    `json:"expires"`
	Token    string       `json:"token,omitempty"`
}

type PassOpener interface {
	Handles(provider ProviderName) bool
	Token(hostID uuid.UUID, provider ProviderName, pass string) (string, error)
}

type SignIns interface {
	Providers() []ProviderName
	Start(hostID uuid.UUID, provider ProviderName) (string, error)
	Finish(ctx context.Context, hostID uuid.UUID, provider ProviderName, code, state string) (string, error)
}

type Credentials struct {
	Account string
	Secret  string
}

type Property struct {
	ID           string
	Title        string
	Description  string
	Address      listings.Address
	Type         listings.ListingType
	PropertyType listings.PropertyType
	MaxGuests    int
	Bedrooms     int
	Beds         int
	Bathrooms    float64
	NightlyCents int
	Currency     string
	PhotoURLs    []string
}

type flexibleNumber float64

type flexibleID string

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

type cachedToken struct {
	value   string
	expires time.Time
}

type Source interface {
	Properties(ctx context.Context, credentials Credentials) ([]Property, error)
}

type State string

const (
	Ready        State = "ready"
	AlreadyAdded State = "already_added"
	NeedsDetails State = "needs_details"
	Imported     State = "imported"
	Failed       State = "failed"
)

type Draft struct {
	Title        string                `json:"title"`
	Description  string                `json:"description"`
	Address      listings.Address      `json:"address"`
	Type         listings.ListingType  `json:"type"`
	PropertyType listings.PropertyType `json:"property_type"`
	MaxGuests    int                   `json:"max_guests"`
	Bedrooms     int                   `json:"bedrooms"`
	Beds         int                   `json:"beds"`
	Bathrooms    float64               `json:"bathrooms"`
}

type Candidate struct {
	ID           string   `json:"id"`
	Askable      bool     `json:"askable"`
	State        State    `json:"state"`
	Missing      []string `json:"missing"`
	PhotoCount   int      `json:"photo_count"`
	CoverURL     string   `json:"cover_url,omitempty"`
	NightlyCents int      `json:"nightly_cents,omitempty"`
	Draft        Draft    `json:"draft"`
}

type Outcome struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	City         string     `json:"city"`
	State        State      `json:"state"`
	ListingID    *uuid.UUID `json:"listing_id,omitempty"`
	PhotosQueued int        `json:"photos_queued"`
	PriceSet     bool       `json:"price_set"`
	Message      string     `json:"message,omitempty"`
}

type Answer struct {
	PropertyType listings.PropertyType `json:"property_type"`
	Beds         int                   `json:"beds"`
	Bathrooms    float64               `json:"bathrooms"`
}

type Selection struct {
	PropertyIDs []string
	Answers     map[string]Answer
}

type HostListings interface {
	ListForHost(ctx context.Context, hostID uuid.UUID) ([]listings.HostListing, error)
}

type DraftWriter interface {
	CreateListing(ctx context.Context, hostID uuid.UUID, input listings.CreateListingInput) (listings.Listing, error)
	SetPrice(ctx context.Context, hostID, listingID uuid.UUID, nightlyCents int) (listings.Setup, error)
	ImportPhoto(ctx context.Context, hostID, listingID uuid.UUID, contentType string, data []byte) error
}

type PhotoFetcher interface {
	Fetch(ctx context.Context, address string) (data []byte, contentType string, err error)
}

type Importer interface {
	Preview(ctx context.Context, hostID uuid.UUID, provider ProviderName, credentials Credentials) ([]Candidate, error)
	Import(ctx context.Context, hostID uuid.UUID, provider ProviderName, credentials Credentials, selection Selection) ([]Outcome, error)
}

const (
	maxProperties         = 100
	maxPropertiesPerBatch = 25
	maxHostListings       = 100
	maxPhotosPerListing   = 20
	maxPhotoBytes         = 7 * 1024 * 1024
	maxCredentialLength   = 4096
	minimumBookingAge     = 18
	platformCurrency      = "USD"
	parallelPhotoJobs     = 2
	photoJobDeadline      = 15 * time.Minute
	providerDeadline      = 25 * time.Second
)

const (
	missingPropertyType = "the property type"
	missingBeds         = "the bed count"
	missingBathrooms    = "the bathroom count"
)

var askable = []string{missingPropertyType, missingBeds, missingBathrooms}

var (
	ErrUnknownProvider     = errors.New("that property system is not supported")
	ErrCredentialsRequired = errors.New("fill in every field to connect")
	ErrCredentialsRefused  = errors.New("that system refused those details, check them and try again")
	ErrProviderBusy        = errors.New("that system is asking us to slow down, try again in a few minutes")
	ErrProviderFailed      = errors.New("we could not read from that system, try again")
	ErrNothingSelected     = errors.New("choose at least one place to import")
	ErrTooManySelected     = errors.New("import up to 25 places at a time")
	ErrSignInExpired       = errors.New("that sign in ran out, connect again")
)
