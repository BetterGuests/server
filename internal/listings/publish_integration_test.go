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

func (alwaysVerified) HasCompleted(context.Context, uuid.UUID) (bool, error) { return true, nil }

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

func draftAt(t *testing.T, ctx context.Context, service *ListingService, hostID uuid.UUID, line1, line2, postalCode string) Listing {
	t.Helper()
	input := validCreateInput()
	input.Address = Address{Line1: line1, Line2: line2, City: "Phoenix", PostalCode: postalCode, CountryCode: "US", Latitude: 33.4, Longitude: -112.1}
	input.Photos = nil
	listing, err := service.CreateListing(ctx, hostID, input)
	if err != nil {
		t.Fatal(err)
	}
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
	input.Photos = nil
	listing, err := service.CreateListing(ctx, uuid.New(), input)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.PublishListing(ctx, listing.HostID, listing.ID); !errors.Is(err, ErrProofRequired) {
		t.Fatalf("expected ErrProofRequired, got %v", err)
	}
}
