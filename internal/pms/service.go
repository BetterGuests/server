package pms

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
)

type ImportService struct {
	sources      map[ProviderName]Source
	existing     HostListings
	writer       DraftWriter
	photos       PhotoFetcher
	photoSlots   chan struct{}
	inBackground func(func())
}

func NewImportService(sources map[ProviderName]Source, existing HostListings, writer DraftWriter, photos PhotoFetcher) *ImportService {
	return &ImportService{
		sources:      sources,
		existing:     existing,
		writer:       writer,
		photos:       photos,
		photoSlots:   make(chan struct{}, parallelPhotoJobs),
		inBackground: func(work func()) { go work() },
	}
}

func (service *ImportService) Preview(ctx context.Context, hostID uuid.UUID, provider ProviderName, credentials Credentials) ([]Candidate, error) {
	properties, err := service.read(ctx, provider, credentials)
	if err != nil {
		return nil, err
	}
	taken, _, err := service.taken(ctx, hostID)
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(properties))
	for _, property := range properties {
		candidates = append(candidates, candidateFor(property, taken))
	}
	return candidates, nil
}

func (service *ImportService) Import(ctx context.Context, hostID uuid.UUID, provider ProviderName, credentials Credentials, selection Selection) ([]Outcome, error) {
	propertyIDs := selection.PropertyIDs
	if len(propertyIDs) == 0 {
		return nil, ErrNothingSelected
	}
	if len(propertyIDs) > maxPropertiesPerBatch {
		return nil, ErrTooManySelected
	}
	properties, err := service.read(ctx, provider, credentials)
	if err != nil {
		return nil, err
	}
	taken, owned, err := service.taken(ctx, hostID)
	if err != nil {
		return nil, err
	}
	outcomes := []Outcome{}
	var queued []photoJob
	for _, property := range properties {
		if !slices.Contains(propertyIDs, property.ID) {
			continue
		}
		candidate := candidateFor(answered(property, selection.Answers[property.ID]), taken)
		outcome := Outcome{ID: property.ID, Title: candidate.Draft.Title, City: candidate.Draft.Address.City, State: candidate.State}
		switch {
		case candidate.State == AlreadyAdded:
		case candidate.State == NeedsDetails:
			outcome.Message = "Missing " + strings.Join(candidate.Missing, ", ")
		case owned >= maxHostListings:
			outcome.State = Failed
			outcome.Message = "You have reached the listing limit"
		default:
			job := service.create(ctx, hostID, property, candidate.Draft, &outcome)
			if outcome.State == Imported {
				owned++
				taken[listingKey(candidate.Draft.Title, candidate.Draft.Address.City)] = true
			}
			if len(job.addresses) > 0 {
				queued = append(queued, job)
			}
		}
		outcomes = append(outcomes, outcome)
	}
	service.fetchLater(ctx, hostID, queued)
	return outcomes, nil
}

type photoJob struct {
	listingID uuid.UUID
	addresses []string
}

func (service *ImportService) create(ctx context.Context, hostID uuid.UUID, property Property, draft Draft, outcome *Outcome) photoJob {
	listing, err := service.writer.CreateListing(ctx, hostID, listings.CreateListingInput{
		Title:            draft.Title,
		Description:      draft.Description,
		Address:          draft.Address,
		Type:             draft.Type,
		PropertyType:     draft.PropertyType,
		MaxGuests:        draft.MaxGuests,
		MinimumAgeToBook: minimumBookingAge,
		Bedrooms:         draft.Bedrooms,
		Beds:             draft.Beds,
		Bathrooms:        draft.Bathrooms,
	})
	if err != nil {
		outcome.State = Failed
		if errors.Is(err, listings.ErrInvalidListing) {
			outcome.Message = "Some details are not accepted, add this one by hand"
		} else {
			slog.Error("import listing draft", "error", err)
			outcome.Message = "We could not save this one, try again"
		}
		return photoJob{}
	}
	outcome.State = Imported
	outcome.ListingID = &listing.ID
	if property.NightlyCents > 0 && strings.EqualFold(property.Currency, platformCurrency) {
		if _, err := service.writer.SetPrice(ctx, hostID, listing.ID, property.NightlyCents); err != nil {
			slog.Warn("import listing price", "listing", listing.ID, "error", err)
		} else {
			outcome.PriceSet = true
		}
	}
	addresses := property.PhotoURLs[:min(len(property.PhotoURLs), maxPhotosPerListing)]
	outcome.PhotosQueued = len(addresses)
	return photoJob{listingID: listing.ID, addresses: addresses}
}

func (service *ImportService) fetchLater(ctx context.Context, hostID uuid.UUID, jobs []photoJob) {
	if len(jobs) == 0 {
		return
	}
	detached := context.WithoutCancel(ctx)
	service.inBackground(func() {
		service.photoSlots <- struct{}{}
		defer func() { <-service.photoSlots }()
		ctx, cancel := context.WithTimeout(detached, photoJobDeadline)
		defer cancel()
		for _, job := range jobs {
			service.fetchPhotos(ctx, hostID, job)
		}
	})
}

func (service *ImportService) fetchPhotos(ctx context.Context, hostID uuid.UUID, job photoJob) {
	for position, address := range job.addresses {
		data, contentType, err := service.photos.Fetch(ctx, address)
		if err == nil {
			err = service.writer.ImportPhoto(ctx, hostID, job.listingID, contentType, data)
		}
		if err != nil {
			slog.Warn("import listing photo", "listing", job.listingID, "position", position, "error", err)
		}
	}
}

func (service *ImportService) read(ctx context.Context, provider ProviderName, credentials Credentials) ([]Property, error) {
	source, known := service.sources[provider]
	if !known {
		return nil, ErrUnknownProvider
	}
	credentials.Account = strings.TrimSpace(credentials.Account)
	credentials.Secret = strings.TrimSpace(credentials.Secret)
	if credentials.Secret == "" || (needsAccount(provider) && credentials.Account == "") {
		return nil, ErrCredentialsRequired
	}
	if len(credentials.Account) > maxCredentialLength || len(credentials.Secret) > maxCredentialLength {
		return nil, ErrCredentialsRefused
	}
	ctx, cancel := context.WithTimeout(ctx, providerDeadline)
	defer cancel()
	properties, err := source.Properties(ctx, credentials)
	if err != nil {
		return nil, fmt.Errorf("read %s properties: %w", provider, err)
	}
	return properties[:min(len(properties), maxProperties)], nil
}

func needsAccount(provider ProviderName) bool {
	return provider == Guesty || provider == Hostaway
}

func (service *ImportService) taken(ctx context.Context, hostID uuid.UUID) (map[string]bool, int, error) {
	owned, err := service.existing.ListForHost(ctx, hostID)
	if err != nil {
		return nil, 0, err
	}
	taken := make(map[string]bool, len(owned))
	for _, listing := range owned {
		taken[listingKey(listing.Title, listing.City)] = true
	}
	return taken, len(owned), nil
}

func listingKey(title, city string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " ") + "\x00" + strings.Join(strings.Fields(city), " "))
}

func candidateFor(property Property, taken map[string]bool) Candidate {
	draft := draftFrom(property)
	candidate := Candidate{
		ID:         property.ID,
		State:      Ready,
		Missing:    missingFrom(draft),
		PhotoCount: min(len(property.PhotoURLs), maxPhotosPerListing),
		Draft:      draft,
	}
	if len(property.PhotoURLs) > 0 {
		candidate.CoverURL = withScheme(property.PhotoURLs[0])
	}
	if strings.EqualFold(property.Currency, platformCurrency) {
		candidate.NightlyCents = property.NightlyCents
	}
	if len(candidate.Missing) > 0 {
		candidate.State = NeedsDetails
		candidate.Askable = onlyMissing(candidate.Missing, askable)
	}
	if taken[listingKey(draft.Title, draft.Address.City)] {
		candidate.State = AlreadyAdded
	}
	return candidate
}

func answered(property Property, answer Answer) Property {
	if property.PropertyType == "" && listings.IsPropertyType(answer.PropertyType) {
		property.PropertyType = answer.PropertyType
	}
	if property.Beds < 1 {
		property.Beds = answer.Beds
	}
	if property.Bathrooms < 0.5 {
		property.Bathrooms = answer.Bathrooms
	}
	return property
}

func onlyMissing(missing, allowed []string) bool {
	return !slices.ContainsFunc(missing, func(name string) bool { return !slices.Contains(allowed, name) })
}

func draftFrom(property Property) Draft {
	address := property.Address
	address.Line1 = clip(plainText(address.Line1), 300)
	address.Line2 = clip(plainText(address.Line2), 300)
	address.City = clip(plainText(address.City), 120)
	address.Region = clip(plainText(address.Region), 120)
	address.PostalCode = clip(plainText(address.PostalCode), 32)
	address.CountryCode = strings.ToUpper(strings.TrimSpace(address.CountryCode))
	listingType := property.Type
	if listingType == "" {
		listingType = listings.EntirePlace
	}
	return Draft{
		Title:        clip(plainText(property.Title), 120),
		Description:  clip(plainText(property.Description), 5000),
		Address:      address,
		Type:         listingType,
		PropertyType: property.PropertyType,
		MaxGuests:    min(property.MaxGuests, 50),
		Bedrooms:     min(max(property.Bedrooms, 0), 50),
		Beds:         min(property.Beds, 100),
		Bathrooms:    min(math.Round(property.Bathrooms*2)/2, 50),
	}
}

func missingFrom(draft Draft) []string {
	address := draft.Address
	checks := []struct {
		absent bool
		name   string
	}{
		{len(draft.Title) < 4, "a title"},
		{len(draft.Description) < 20, "a description"},
		{address.Line1 == "" || address.City == "" || len(address.CountryCode) != 2, "the full address"},
		{!onTheMap(address), "a map location"},
		{draft.PropertyType == "", missingPropertyType},
		{draft.MaxGuests < 1, "the guest count"},
		{draft.Beds < 1, missingBeds},
		{draft.Bathrooms < 0.5, missingBathrooms},
	}
	missing := []string{}
	for _, check := range checks {
		if check.absent {
			missing = append(missing, check.name)
		}
	}
	return missing
}

func onTheMap(address listings.Address) bool {
	if address.Latitude == 0 && address.Longitude == 0 {
		return false
	}
	return math.Abs(address.Latitude) <= 90 && math.Abs(address.Longitude) <= 180
}

var (
	lineBreakTags = regexp.MustCompile(`(?i)<(br|/p|/div|/li)[^>]*>`)
	markupTags    = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	blankRuns     = regexp.MustCompile(`\n{3,}`)
)

func plainText(value string) string {
	value = lineBreakTags.ReplaceAllString(value, "\n")
	value = markupTags.ReplaceAllString(value, "")
	value = html.UnescapeString(value)
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\x00", "")
	value = strings.ToValidUTF8(value, "")
	return strings.TrimSpace(blankRuns.ReplaceAllString(value, "\n\n"))
}

func clip(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit])
}
