package proofs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type verifiedHosts struct{}

func (verifiedHosts) HasCompleted(context.Context, uuid.UUID) (bool, error) { return true, nil }

type uploadedObjects struct{ objectsStub }

func (objects *uploadedObjects) Stat(context.Context, string) (ObjectInfo, error) {
	return ObjectInfo{Size: 2048, ContentType: "application/pdf"}, nil
}

type fixture struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	super    *pgx.Conn
	proofs   *ProofService
	listings *listings.ListingService
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	appURL, superURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_SUPERUSER_DATABASE_URL")
	if appURL == "" || superURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_SUPERUSER_DATABASE_URL through go run ./cmd/dev test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	super, err := pgx.Connect(ctx, superURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = super.Close(ctx) })
	listingStore := listings.NewPostgresListingStore(pool)
	return fixture{
		ctx: ctx, pool: pool, super: super,
		proofs:   NewProofService(NewPostgresProofStore(pool), &uploadedObjects{}),
		listings: listings.NewListingService(listingStore).WithPublishing(listingStore, verifiedHosts{}),
	}
}

func (f fixture) draft(t *testing.T, hostID uuid.UUID) listings.Listing {
	t.Helper()
	listing, err := f.listings.CreateListing(f.ctx, hostID, listings.CreateListingInput{
		Title: "Proof House", Description: "A complete listing used by the proof integration test.",
		Address: listings.Address{Line1: uuid.NewString()[:6] + " Proof Way", City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return listing
}

func (f fixture) makeAdmin(t *testing.T) uuid.UUID {
	t.Helper()
	adminID := uuid.New()
	if _, err := f.super.Exec(f.ctx, `INSERT INTO app.accounts (id) VALUES ($1)`, adminID); err != nil {
		t.Fatal(err)
	}
	_, err := f.super.Exec(f.ctx, `INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, adminID, adminID.String()+"@admin.test")
	if err != nil {
		t.Fatal(err)
	}
	return adminID
}

func (f fixture) submit(t *testing.T, listing listings.Listing) Proof {
	t.Helper()
	key := keyPrefix(listing.ID) + uuid.NewString() + ".pdf"
	proof, err := f.proofs.Submit(f.ctx, listing.HostID, listing.ID, UtilityBill, key)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestApprovedProofUnlocksPublishing(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)

	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); !errors.Is(err, listings.ErrProofRequired) {
		t.Fatalf("publishing without a proof must be refused, got %v", err)
	}
	proof := f.submit(t, listing)
	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); !errors.Is(err, listings.ErrProofRequired) {
		t.Fatalf("a proof still waiting for review must not unlock publishing, got %v", err)
	}
	if err := f.proofs.Decide(f.ctx, adminID, proof.ID, Approve, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); err != nil {
		t.Fatalf("approved proof must unlock publishing: %v", err)
	}
}

func TestRejectedProofKeepsPublishingLockedAndAllowsResubmission(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)
	first := f.submit(t, listing)

	if err := f.proofs.Decide(f.ctx, adminID, first.ID, Reject, "Name does not match"); err != nil {
		t.Fatal(err)
	}
	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); !errors.Is(err, listings.ErrProofRequired) {
		t.Fatalf("rejected proof must not unlock publishing, got %v", err)
	}
	latest, found, err := f.proofs.Status(f.ctx, listing.HostID, listing.ID)
	if err != nil || !found || latest.Status != Rejected || latest.RejectionReason != "Name does not match" {
		t.Fatalf("host must see the rejection: %+v %v %v", latest, found, err)
	}
	f.submit(t, listing)
}

func TestSecondSubmissionWhileOneIsPendingIsRefused(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	f.submit(t, listing)

	key := keyPrefix(listing.ID) + uuid.NewString() + ".pdf"
	if _, err := f.proofs.Submit(f.ctx, listing.HostID, listing.ID, Deed, key); !errors.Is(err, ErrProofPending) {
		t.Fatalf("expected ErrProofPending, got %v", err)
	}
}

func TestHostsCannotSubmitProofForSomeoneElsesListing(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())

	key := keyPrefix(listing.ID) + uuid.NewString() + ".pdf"
	_, err := f.proofs.Submit(f.ctx, uuid.New(), listing.ID, Lease, key)

	if !errors.Is(err, ErrListingNotFound) && !errors.Is(err, ErrNotListingOwner) {
		t.Fatalf("expected an ownership refusal, got %v", err)
	}
}

func TestOnlyAdminsCanSeeOrDecideOtherHostsProofs(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	proof := f.submit(t, listing)
	stranger := uuid.New()

	visible := -1
	err := db.WithUserTx(f.ctx, f.pool, stranger.String(), func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, `SELECT count(*) FROM app.property_proofs WHERE id = $1`, proof.ID).Scan(&visible)
	})
	if err != nil || visible != 0 {
		t.Fatalf("a stranger must not see another host's proof: %d %v", visible, err)
	}
	if _, err := f.proofs.ListSubmitted(f.ctx, stranger); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("expected ErrNotAdmin, got %v", err)
	}
	if err := f.proofs.Decide(f.ctx, stranger, proof.ID, Approve, ""); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("expected ErrNotAdmin, got %v", err)
	}
}

func TestDatabaseRefusesSelfApprovalByTheHost(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	proof := f.submit(t, listing)

	var updated int64
	err := db.WithUserTx(f.ctx, f.pool, listing.HostID.String(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(f.ctx, `
			UPDATE app.property_proofs
			SET status = 'approved', reviewed_by = $2, reviewed_at = now(), updated_at = now()
			WHERE id = $1`, proof.ID, listing.HostID)
		updated = tag.RowsAffected()
		return err
	})
	if err != nil || updated != 0 {
		t.Fatalf("host must not be able to approve their own proof: %d rows, %v", updated, err)
	}
}

func TestAdminCannotApproveTheirOwnListing(t *testing.T) {
	f := newFixture(t)
	adminID := f.makeAdmin(t)
	listing := f.draft(t, adminID)
	proof := f.submit(t, listing)

	if err := f.proofs.Decide(f.ctx, adminID, proof.ID, Approve, ""); !errors.Is(err, ErrOwnListing) {
		t.Fatalf("expected ErrOwnListing, got %v", err)
	}
}

func TestDecidedProofCannotBeDecidedAgain(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)
	proof := f.submit(t, listing)

	if err := f.proofs.Decide(f.ctx, adminID, proof.ID, Approve, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.proofs.Decide(f.ctx, adminID, proof.ID, Reject, "changed my mind"); !errors.Is(err, ErrNotAwaitingReview) {
		t.Fatalf("expected ErrNotAwaitingReview, got %v", err)
	}
}

func TestAdminQueueListsSubmittedProofsWithAddress(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)
	proof := f.submit(t, listing)

	items, err := f.proofs.ListSubmitted(f.ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == proof.ID {
			if item.AddressLine1 != listing.Address.Line1 || item.CountryCode != "US" || !strings.Contains(item.DocumentURL, proof.ObjectKey) {
				t.Fatalf("queue item is missing address or signed link: %+v", item)
			}
			return
		}
	}
	t.Fatal("submitted proof is missing from the admin queue")
}

func TestProofsCannotBeDeletedByTheApplication(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	proof := f.submit(t, listing)

	err := db.WithUserTx(f.ctx, f.pool, listing.HostID.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `DELETE FROM app.property_proofs WHERE id = $1`, proof.ID)
		return err
	})
	if err == nil {
		t.Fatal("deleting a proof must be refused so documents stay available for disputes")
	}
}

func TestOnlyTheMigratorCanGrantAdmin(t *testing.T) {
	f := newFixture(t)
	account := uuid.New()

	err := db.WithUserTx(f.ctx, f.pool, account.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, `INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, 'self@grant.test')`, account)
		return err
	})
	if err == nil {
		t.Fatal("the application role must never be able to grant admin")
	}
}
