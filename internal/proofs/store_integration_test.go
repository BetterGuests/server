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
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
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
	f.priceAndPhotograph(t, listing)
	return listing
}

func (f fixture) priceAndPhotograph(t *testing.T, listing listings.Listing) {
	t.Helper()
	store := listings.NewPostgresListingStore(f.pool)
	if err := store.SetPrice(f.ctx, listing.HostID.String(), listing.ID, 12000); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		photo := listings.ListingPhoto{ID: uuid.New(), ObjectKey: "listings/" + listing.ID.String() + "/" + uuid.NewString() + ".jpg"}
		if err := store.AddPhoto(f.ctx, listing.HostID.String(), listing.ID, photo); err != nil {
			t.Fatal(err)
		}
	}
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

func TestReviewerSeesTheNameFromTheHostsID(t *testing.T) {
	f := newFixture(t)
	named := f.draft(t, uuid.New())
	unnamed := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)
	verifications := verification.NewPostgresVerificationStore(f.pool)
	inquiryID := "inq_" + uuid.NewString()
	at := time.Now().UTC().Truncate(time.Second)
	name := verification.IDName{First: "John", Middle: "Michael", Last: "Smith", Suffix: "Jr"}

	if err := verifications.Create(f.ctx, verification.Verification{ID: uuid.New(), AccountID: named.HostID, PersonaInquiryID: inquiryID, Status: verification.StatusCreated, CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	apply := func(status verification.Status, offset time.Duration, saved verification.IDName) {
		t.Helper()
		if err := verifications.ApplyStatus(f.ctx, named.HostID, inquiryID, status, at.Add(offset)); err != nil {
			t.Fatal(err)
		}
		if err := verifications.SyncIDName(f.ctx, named.HostID, inquiryID, saved); err != nil {
			t.Fatal(err)
		}
	}
	reviewNames := func(readerID uuid.UUID) map[uuid.UUID]verification.IDName {
		t.Helper()
		items, err := f.proofs.store.ListSubmitted(f.ctx, readerID)
		if err != nil {
			t.Fatal(err)
		}
		names := map[uuid.UUID]verification.IDName{}
		for _, item := range items {
			names[item.ListingID] = verification.IDName{First: item.IDFirstName, Middle: item.IDMiddleName, Last: item.IDLastName, Suffix: item.IDNameSuffix}
		}
		return names
	}
	f.submit(t, named)
	f.submit(t, unnamed)

	apply(verification.StatusCompleted, time.Second, name)
	names := reviewNames(adminID)
	if names[named.ID] != name {
		t.Fatalf("admin must see the ID name, got %+v", names[named.ID])
	}
	if got, listed := names[unnamed.ID]; !listed || got != (verification.IDName{}) {
		t.Fatalf("a host with no saved name must still be listed: %+v %v", got, listed)
	}

	t.Run("an event without a name keeps the saved one", func(t *testing.T) {
		apply(verification.StatusCompleted, 2*time.Second, verification.IDName{})
		if got := reviewNames(adminID)[named.ID]; got != name {
			t.Fatalf("name = %+v", got)
		}
	})

	t.Run("another account cannot read the name", func(t *testing.T) {
		var visible int
		err := db.WithUserTx(f.ctx, f.pool, unnamed.HostID.String(), func(tx pgx.Tx) error {
			return tx.QueryRow(f.ctx, `SELECT count(*) FROM app.identity_names WHERE account_id = $1`, named.HostID).Scan(&visible)
		})
		if err != nil || visible != 0 {
			t.Fatalf("visible = %d err = %v", visible, err)
		}
	})

	t.Run("a decline removes the name", func(t *testing.T) {
		apply(verification.StatusDeclined, 3*time.Second, verification.IDName{})
		if got := reviewNames(adminID)[named.ID]; got != (verification.IDName{}) {
			t.Fatalf("name = %+v", got)
		}
	})

	t.Run("a late older completion does not bring a declined name back", func(t *testing.T) {
		apply(verification.StatusCompleted, time.Second, name)
		if got := reviewNames(adminID)[named.ID]; got != (verification.IDName{}) {
			t.Fatalf("name = %+v", got)
		}
	})

	t.Run("scheduled redaction keeps the name", func(t *testing.T) {
		apply(verification.StatusCompleted, 4*time.Second, name)
		completed, found, err := verifications.LatestCompleted(f.ctx, named.HostID)
		if err != nil || !found {
			t.Fatal(found, err)
		}
		if err := verifications.MarkRedacted(f.ctx, completed.ID); err != nil {
			t.Fatal(err)
		}
		if got := reviewNames(adminID)[named.ID]; got != name {
			t.Fatalf("name = %+v", got)
		}
	})

	t.Run("the user deleting ID data removes the name for good", func(t *testing.T) {
		if _, err := verifications.RequestDeletion(f.ctx, named.HostID); err != nil {
			t.Fatal(err)
		}
		apply(verification.StatusCompleted, 5*time.Second, name)
		if got := reviewNames(adminID)[named.ID]; got != (verification.IDName{}) {
			t.Fatalf("name = %+v", got)
		}
	})
}

func TestHeldIDNameIsDeletedOnceTheHoldIsReleased(t *testing.T) {
	f := newFixture(t)
	hostID, verificationID := uuid.New(), uuid.New()
	verifications := verification.NewPostgresVerificationStore(f.pool)
	inquiryID := "inq_" + uuid.NewString()
	at := time.Now().UTC().Truncate(time.Second)

	if err := verifications.Create(f.ctx, verification.Verification{ID: verificationID, AccountID: hostID, PersonaInquiryID: inquiryID, Status: verification.StatusCreated, CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := verifications.ApplyStatus(f.ctx, hostID, inquiryID, verification.StatusCompleted, at); err != nil {
		t.Fatal(err)
	}
	if err := verifications.SyncIDName(f.ctx, hostID, inquiryID, verification.IDName{First: "John", Last: "Smith"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.super.Exec(f.ctx, `INSERT INTO app.identity_verification_holds (id, verification_id, case_reference, approved_by) VALUES ($1, $2, 'case-1', 'counsel')`, uuid.New(), verificationID); err != nil {
		t.Fatal(err)
	}
	saved := func() int {
		t.Helper()
		var count int
		if err := f.super.QueryRow(f.ctx, `SELECT count(*) FROM app.identity_names WHERE account_id = $1`, hostID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if _, err := verifications.RequestDeletion(f.ctx, hostID); err != nil {
		t.Fatal(err)
	}
	if saved() != 1 {
		t.Fatal("a held name must survive a deletion request")
	}
	if _, err := f.super.Exec(f.ctx, `UPDATE app.identity_verification_holds SET released_at = now(), released_by = 'counsel' WHERE verification_id = $1`, verificationID); err != nil {
		t.Fatal(err)
	}
	if err := verifications.MarkRedacted(f.ctx, verificationID); err != nil {
		t.Fatal(err)
	}
	if saved() != 0 {
		t.Fatal("redacting a requested deletion must delete the name")
	}
}

func TestCheckedDocumentIsApprovedForItsOwnerOnly(t *testing.T) {
	f := newFixture(t)
	listing := f.draft(t, uuid.New())
	store := NewPostgresProofStore(f.pool)
	subject, err := store.CheckSubject(f.ctx, listing.HostID, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	inquiryID := "inq_" + uuid.NewString()
	now := time.Now().UTC()
	if err := store.SaveInquiry(f.ctx, CheckInquiry{ID: uuid.New(), InquiryID: inquiryID, ListingID: listing.ID, HostID: listing.HostID,
		DocumentType: UtilityBill, AddressKey: subject.AddressKey, UnitKey: subject.UnitKey, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindInquiry(f.ctx, uuid.New(), inquiryID); err != nil || found {
		t.Fatalf("a stranger must not see the inquiry: %v %v", found, err)
	}
	proof := Proof{ID: uuid.New(), ListingID: listing.ID, HostID: listing.HostID, DocumentType: UtilityBill,
		ObjectKey: keyPrefix(listing.ID) + uuid.NewString() + ".pdf", ContentType: "application/pdf", SizeBytes: 10,
		InquiryID: inquiryID, AddressKey: subject.AddressKey, UnitKey: subject.UnitKey, CreatedAt: now}
	if err := store.SaveFromInquiry(f.ctx, proof); err != nil {
		t.Fatal(err)
	}
	if approved, err := store.ApproveFromInquiry(f.ctx, uuid.New(), inquiryID, now); err != nil || approved {
		t.Fatalf("a stranger must not approve: %v %v", approved, err)
	}
	if approved, err := store.ApproveFromInquiry(f.ctx, listing.HostID, inquiryID, now); err != nil || !approved {
		t.Fatalf("expected approval: %v %v", approved, err)
	}
	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); err != nil {
		t.Fatalf("an automatically approved proof must unlock publishing: %v", err)
	}
}
