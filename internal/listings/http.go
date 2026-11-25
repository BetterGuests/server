package listings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type ListingCreator interface {
	CreateListing(
		ctx context.Context,
		hostID uuid.UUID,
		input CreateListingInput,
	) (Listing, error)
}

type ListingPublisher interface {
	PublishListing(ctx context.Context, hostID, listingID uuid.UUID) error
}

type MediaManager interface {
	Setup(ctx context.Context, hostID, listingID uuid.UUID) (Setup, error)
	RequestPhotoUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (PhotoTicket, error)
	AddPhoto(ctx context.Context, hostID, listingID uuid.UUID, key, altText string) (Setup, error)
	RemovePhoto(ctx context.Context, hostID, listingID, photoID uuid.UUID) (Setup, error)
	SetPrice(ctx context.Context, hostID, listingID uuid.UUID, nightlyCents int) (Setup, error)
}

type HTTPHandler struct {
	listingCreator ListingCreator
	publisher      ListingPublisher
	publicReader   PublicListingReader
	manager        MediaManager
}

func NewHTTPHandler(listingCreator ListingCreator) *HTTPHandler {
	return &HTTPHandler{listingCreator: listingCreator}
}

func (handler *HTTPHandler) WithPublisher(publisher ListingPublisher) *HTTPHandler {
	handler.publisher = publisher
	return handler
}

func (handler *HTTPHandler) PublishListing(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeListingError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeListingError(w, http.StatusNotFound, "listing not found")
		return
	}
	err = handler.publisher.PublishListing(r.Context(), hostID, listingID)
	switch {
	case errors.Is(err, ErrListingNotFound):
		writeListingError(w, http.StatusNotFound, "listing not found")
	case errors.Is(err, ErrNotListingOwner), errors.Is(err, ErrVerificationRequired), errors.Is(err, ErrProofRequired),
		errors.Is(err, ErrPriceRequired), errors.Is(err, ErrPhotoRequired):
		writeListingError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrDuplicateListing):
		writeListingError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.Error("publish listing", "error", err)
		writeListingError(w, http.StatusInternalServerError, "could not publish listing")
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": listingID.String(), "status": string(Published)})
	}
}

func (handler *HTTPHandler) WithPublicReader(reader PublicListingReader) *HTTPHandler {
	handler.publicReader = reader
	return handler
}

type CreateListingRequest struct {
	Title            string                      `json:"title"`
	Description      string                      `json:"description"`
	Address          Address                     `json:"address"`
	Type             ListingType                 `json:"type"`
	PropertyType     PropertyType                `json:"property_type"`
	MaxGuests        int                         `json:"max_guests"`
	MinimumAgeToBook int                         `json:"minimum_age_to_book"`
	Bedrooms         int                         `json:"bedrooms"`
	Beds             int                         `json:"beds"`
	Bathrooms        float64                     `json:"bathrooms"`
	Amenities        []Amenity                   `json:"amenities"`
	Photos           []CreateListingPhotoRequest `json:"photos"`
	Rules            []HouseRule                 `json:"rules"`
}

type CreateListingPhotoRequest struct {
	ObjectKey string `json:"object_key"`
	AltText   string `json:"alt_text,omitempty"`
	Position  int    `json:"position"`
}

func (handler *HTTPHandler) CreateListing(w http.ResponseWriter, r *http.Request) {
	hostID, ok := accounts.UserID(r.Context())

	if !ok {
		writeListingError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var request CreateListingRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&request); err != nil {
		writeListingError(w, http.StatusBadRequest, "invalid JSON request")
		return
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeListingError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}

	photos := make([]CreateListingPhotoInput, len(request.Photos))
	for index, photo := range request.Photos {
		photos[index] = CreateListingPhotoInput(photo)
	}

	listing, err := handler.listingCreator.CreateListing(r.Context(), hostID, CreateListingInput{
		Title: request.Title, Description: request.Description, Address: request.Address,
		Type: request.Type, PropertyType: request.PropertyType, MaxGuests: request.MaxGuests,
		MinimumAgeToBook: request.MinimumAgeToBook, Bedrooms: request.Bedrooms,
		Beds: request.Beds, Bathrooms: request.Bathrooms, Amenities: request.Amenities,
		Photos: photos, Rules: request.Rules,
	})

	if errors.Is(err, ErrInvalidListing) {
		writeListingError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err != nil {
		slog.Error("create listing", "error", err)
		writeListingError(w, http.StatusInternalServerError, "could not create listing")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/api/v1/listings/"+listing.ID.String())
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(listing)
}

func writeListingError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (handler *HTTPHandler) ListListings(w http.ResponseWriter, r *http.Request) {
	listings, err := handler.publicReader.ListPublished(r.Context())
	if err != nil {
		slog.Error("list published listings", "error", err)
		writeListingError(w, http.StatusInternalServerError, "could not load listings")
		return
	}
	writePublicJSON(w, http.StatusOK, map[string]any{"listings": listings})
}

func (handler *HTTPHandler) GetListing(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeListingError(w, http.StatusBadRequest, "invalid listing id")
		return
	}
	listing, err := handler.publicReader.GetPublished(r.Context(), id)
	if errors.Is(err, ErrListingNotFound) {
		writeListingError(w, http.StatusNotFound, "listing not found")
		return
	}
	if err != nil {
		slog.Error("get published listing", "error", err, "listing_id", id)
		writeListingError(w, http.StatusInternalServerError, "could not load listing")
		return
	}
	writePublicJSON(w, http.StatusOK, listing)
}

func writePublicJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (handler *HTTPHandler) WithMedia(manager MediaManager) *HTTPHandler {
	handler.manager = manager
	return handler
}

func (handler *HTTPHandler) GetSetup(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	setup, err := handler.manager.Setup(r.Context(), hostID, listingID)
	writeMediaResult(w, http.StatusOK, setup, err)
}

func (handler *HTTPHandler) RequestPhotoUpload(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request struct {
		ContentType string `json:"content_type"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if !decodeMediaBody(w, r, &request) {
		return
	}
	ticket, err := handler.manager.RequestPhotoUpload(r.Context(), hostID, listingID, request.ContentType, request.SizeBytes)
	writeMediaResult(w, http.StatusCreated, ticket, err)
}

func (handler *HTTPHandler) AddPhoto(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request struct {
		PhotoKey string `json:"photo_key"`
		AltText  string `json:"alt_text"`
	}
	if !decodeMediaBody(w, r, &request) {
		return
	}
	setup, err := handler.manager.AddPhoto(r.Context(), hostID, listingID, request.PhotoKey, request.AltText)
	writeMediaResult(w, http.StatusCreated, setup, err)
}

func (handler *HTTPHandler) RemovePhoto(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	photoID, err := uuid.Parse(r.PathValue("photo_id"))
	if err != nil {
		writeListingError(w, http.StatusNotFound, ErrPhotoNotFound.Error())
		return
	}
	setup, err := handler.manager.RemovePhoto(r.Context(), hostID, listingID, photoID)
	writeMediaResult(w, http.StatusOK, setup, err)
}

func (handler *HTTPHandler) SetPrice(w http.ResponseWriter, r *http.Request) {
	hostID, listingID, ok := hostAndListing(w, r)
	if !ok {
		return
	}
	var request struct {
		NightlyCents int `json:"nightly_cents"`
	}
	if !decodeMediaBody(w, r, &request) {
		return
	}
	setup, err := handler.manager.SetPrice(r.Context(), hostID, listingID, request.NightlyCents)
	writeMediaResult(w, http.StatusOK, setup, err)
}

func hostAndListing(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	hostID, ok := accounts.UserID(r.Context())
	if !ok {
		writeListingError(w, http.StatusUnauthorized, "authentication required")
		return uuid.Nil, uuid.Nil, false
	}
	listingID, err := uuid.Parse(r.PathValue("listing_id"))
	if err != nil {
		writeListingError(w, http.StatusNotFound, "listing not found")
		return uuid.Nil, uuid.Nil, false
	}
	return hostID, listingID, true
}

func decodeMediaBody(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeListingError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeListingError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return false
	}
	return true
}

func mediaErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrListingNotFound), errors.Is(err, ErrPhotoNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNotListingOwner):
		return http.StatusForbidden
	case errors.Is(err, ErrPhotoAlreadyAdded), errors.Is(err, ErrTooManyPhotos), errors.Is(err, ErrLastPhoto):
		return http.StatusConflict
	case errors.Is(err, ErrPhotoRejected), errors.Is(err, ErrInvalidPhotoKey), errors.Is(err, ErrPhotoNotUploaded),
		errors.Is(err, ErrInvalidPrice), errors.Is(err, ErrInvalidListing):
		return http.StatusBadRequest
	case errors.Is(err, ErrUploadsDisabled):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func writeMediaResult(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		code := mediaErrorStatus(err)
		message := err.Error()
		if code == http.StatusInternalServerError {
			slog.Error("listing media", "error", err)
			message = "could not complete request"
		}
		writeListingError(w, code, message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
