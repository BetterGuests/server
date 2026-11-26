package listings

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type alwaysVerified struct{}

func (alwaysVerified) CurrentVerificationID(context.Context, uuid.UUID) (uuid.UUID, bool, error) {
	return uuid.Nil, true, nil
}

func publishFixture(t *testing.T) (context.Context, *ListingService) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewPostgresListingStore(pool)
	return ctx, NewListingService(store).WithPublishing(store, alwaysVerified{})
}

func approveProof(t *testing.T, ctx context.Context, listing Listing) {
	t.Helper()
	superuserURL := os.Getenv("TEST_SUPERUSER_DATABASE_URL")
	if superuserURL == "" {
		t.Skip("set TEST_SUPERUSER_DATABASE_URL through go run ./cmd/dev test")
	}
	admin, err := pgx.Connect(ctx, superuserURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	reviewerID := uuid.New()
	_, err = admin.Exec(ctx, `INSERT INTO app.accounts (id) VALUES ($1)`, reviewerID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `
		INSERT INTO app.property_proofs (
			id, listing_id, host_id, document_type, object_key, content_type, size_bytes,
			address_key, unit_key, status, reviewed_by, reviewed_at, created_at, updated_at
		)
		SELECT $1, l.id, l.host_id, 'utility_bill', $2, 'application/pdf', 1000,
			l.address_key, l.unit_key, 'approved', $3, now(), now(), now()
		FROM app.listings l WHERE l.id = $4`,
		uuid.New(), "property-proofs/"+listing.ID.String()+"/"+uuid.NewString()+".pdf", reviewerID, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
}

func priceAndPicture(t *testing.T, ctx context.Context, service *ListingService, listing Listing) {
	t.Helper()
	store := service.store.(*PostgresListingStore)
	if err := store.SetPrice(ctx, listing.HostID.String(), listing.ID, 12000); err != nil {
		t.Fatal(err)
	}
	for range minPublishPhotos {
		photo := ListingPhoto{ID: uuid.New(), ObjectKey: ownKey(listing.ID)}
		if err := store.AddPhoto(ctx, listing.HostID.String(), listing.ID, photo); err != nil {
			t.Fatal(err)
		}
	}
}

func draftAt(t *testing.T, ctx context.Context, service *ListingService, hostID uuid.UUID, line1, line2, postalCode string) Listing {
	t.Helper()
	input := validCreateInput()
	input.Address = Address{Line1: line1, Line2: line2, City: "Phoenix", PostalCode: postalCode, CountryCode: "US", Latitude: 33.4, Longitude: -112.1}
	listing, err := service.CreateListing(ctx, hostID, input)
	if err != nil {
		t.Fatal(err)
	}
	priceAndPicture(t, ctx, service, listing)
	approveProof(t, ctx, listing)
	return listing
}

func TestPublishBlocksSecondHostAtSameAddressDespiteFormattingDifferences(t *testing.T) {
	ctx, service := publishFixture(t)
	street := "9" + uuid.NewString()[:6] + " Dedupe Way"
	first := draftAt(t, ctx, service, uuid.New(), street, "", "85001")
	second := draftAt(t, ctx, service, uuid.New(), "  "+street+".  ", "", "85 001")

	if err := service.PublishListing(ctx, first.HostID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.PublishListing(ctx, second.HostID, second.ID); !errors.Is(err, ErrDuplicateListing) {
		t.Fatalf("expected ErrDuplicateListing, got %v", err)
	}
}

func TestPublishAllowsDifferentUnitsInTheSameBuilding(t *testing.T) {
	ctx, service := publishFixture(t)
	street := "7" + uuid.NewString()[:6] + " Tower Rd"
	first := draftAt(t, ctx, service, uuid.New(), street, "Unit 1", "85001")
	second := draftAt(t, ctx, service, uuid.New(), street, "Unit 2", "85001")

	if err := service.PublishListing(ctx, first.HostID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.PublishListing(ctx, second.HostID, second.ID); err != nil {
		t.Fatalf("different units must both publish: %v", err)
	}
}

func TestPublishAllowsOverlappingDrafts(t *testing.T) {
	ctx, service := publishFixture(t)
	street := "5" + uuid.NewString()[:6] + " Draft Ln"
	draftAt(t, ctx, service, uuid.New(), street, "", "85001")
	draftAt(t, ctx, service, uuid.New(), street, "", "85001")
}

func TestPublishConcurrentRequestsPublishExactlyOne(t *testing.T) {
	ctx, service := publishFixture(t)
	street := "3" + uuid.NewString()[:6] + " Race St"
	listings := []Listing{
		draftAt(t, ctx, service, uuid.New(), street, "", "85001"),
		draftAt(t, ctx, service, uuid.New(), street, "", "85001"),
		draftAt(t, ctx, service, uuid.New(), street, "", "85001"),
	}

	results := make([]error, len(listings))
	var group sync.WaitGroup
	for index, listing := range listings {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = service.PublishListing(ctx, listing.HostID, listing.ID)
		}()
	}
	group.Wait()

	published := 0
	for _, err := range results {
		switch {
		case err == nil:
			published++
		case !errors.Is(err, ErrDuplicateListing):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if published != 1 {
		t.Fatalf("expected exactly one published listing, got %d", published)
	}
}

func TestPublishRefusesAnotherHostsListing(t *testing.T) {
	ctx, service := publishFixture(t)
	listing := draftAt(t, ctx, service, uuid.New(), "1"+uuid.NewString()[:6]+" Owner Ave", "", "85001")

	if err := service.PublishListing(ctx, uuid.New(), listing.ID); !errors.Is(err, ErrNotListingOwner) && !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("expected ownership refusal, got %v", err)
	}
}

func TestPublishMissingListingIsNotFound(t *testing.T) {
	ctx, service := publishFixture(t)

	if err := service.PublishListing(ctx, uuid.New(), uuid.New()); !errors.Is(err, ErrListingNotFound) {
		t.Fatalf("expected ErrListingNotFound, got %v", err)
	}
}

func TestPublishRequiresApprovedProof(t *testing.T) {
	ctx, service := publishFixture(t)
	input := validCreateInput()
	input.Address = Address{Line1: "2" + uuid.NewString()[:6] + " Proofless Rd", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1}
	listing, err := service.CreateListing(ctx, uuid.New(), input)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.PublishListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("expected ErrProofRequired, got %v", err)
	}
}

func publishedAt(t *testing.T, ctx context.Context, service *ListingService, listingID uuid.UUID) time.Time {
	t.Helper()
	store := service.store.(*PostgresListingStore)
	found, err := store.GetPublished(ctx, listingID)
	if err != nil {
		t.Fatal(err)
	}
	if found.PublishedAt == nil {
		t.Fatal("a published listing must carry its publish date")
	}
	return *found.PublishedAt
}

func TestPublishDateIsWhenTheListingWentLiveAndSurvivesRepublishing(t *testing.T) {
	ctx, service := publishFixture(t)
	listing := draftAt(t, ctx, service, uuid.New(), "4"+uuid.NewString()[:6]+" Fresh Ct", "", "85001")
	beforePublish := time.Now().Add(-time.Second)

	if err := service.PublishListing(ctx, listing.HostID, listing.ID); err != nil {
		t.Fatal(err)
	}
	first := publishedAt(t, ctx, service, listing.ID)
	if first.Before(beforePublish) {
		t.Fatalf("publish date %v predates publishing at %v", first, beforePublish)
	}

	if err := service.PublishListing(ctx, listing.HostID, listing.ID); err != nil {
		t.Fatal(err)
	}
	if second := publishedAt(t, ctx, service, listing.ID); !second.Equal(first) {
		t.Fatalf("publish date moved from %v to %v", first, second)
	}
}

func TestPublishRequiresAPriceThenFivePhotos(t *testing.T) {
	ctx, service := publishFixture(t)
	store := service.store.(*PostgresListingStore)
	input := validCreateInput()
	input.Address = Address{Line1: "6" + uuid.NewString()[:6] + " Bare Rd", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1}
	listing, err := service.CreateListing(ctx, uuid.New(), input)
	if err != nil {
		t.Fatal(err)
	}
	approveProof(t, ctx, listing)
	host := listing.HostID.String()

	if err := service.PublishListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrPriceRequired) {
		t.Fatalf("expected ErrPriceRequired, got %v", err)
	}
	if err := store.SetPrice(ctx, host, listing.ID, 9900); err != nil {
		t.Fatal(err)
	}
	if err := service.PublishListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrPhotoRequired) {
		t.Fatalf("expected ErrPhotoRequired, got %v", err)
	}
	first := ListingPhoto{ID: uuid.New(), ObjectKey: ownKey(listing.ID)}
	if err := store.AddPhoto(ctx, host, listing.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPhoto(ctx, host, listing.ID, ListingPhoto{ID: uuid.New(), ObjectKey: first.ObjectKey}); !errors.Is(err, ErrPhotoAlreadyAdded) {
		t.Fatalf("duplicate error = %v", err)
	}
	addAnother := func() ListingPhoto {
		t.Helper()
		photo := ListingPhoto{ID: uuid.New(), ObjectKey: ownKey(listing.ID)}
		if err := store.AddPhoto(ctx, host, listing.ID, photo); err != nil {
			t.Fatal(err)
		}
		return photo
	}
	for range 3 {
		addAnother()
	}
	if err := service.PublishListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrPhotoRequired) {
		t.Fatalf("four photos must not be enough, got %v", err)
	}
	second := addAnother()
	if err := service.PublishListing(ctx, listing.HostID, listing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RemovePhoto(ctx, host, listing.ID, first.ID); !errors.Is(err, ErrLastPhoto) {
		t.Fatalf("dropping below five = %v", err)
	}

	addAnother()
	if key, err := store.RemovePhoto(ctx, host, listing.ID, first.ID); err != nil || key != first.ObjectKey {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	setup, err := store.Setup(ctx, host, listing.ID)
	if err != nil || setup.NightlyPriceCents != 9900 || len(setup.Photos) != minPublishPhotos || setup.Photos[0].Position != 1 {
		t.Fatalf("setup = %+v, err = %v", setup, err)
	}

	stranger := uuid.NewString()
	if _, err := store.Setup(ctx, stranger, listing.ID); !errors.Is(err, ErrNotListingOwner) {
		t.Fatalf("stranger setup error = %v", err)
	}
	if err := store.SetPrice(ctx, stranger, listing.ID, 100); !errors.Is(err, ErrNotListingOwner) {
		t.Fatalf("stranger price error = %v", err)
	}
	if _, err := store.RemovePhoto(ctx, stranger, listing.ID, second.ID); !errors.Is(err, ErrNotListingOwner) {
		t.Fatalf("stranger remove error = %v", err)
	}
}
