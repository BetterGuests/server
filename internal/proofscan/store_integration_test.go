package proofscan

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pdfObjects struct{}

func (pdfObjects) PresignUpload(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}

func (pdfObjects) PresignDownload(context.Context, string, time.Duration) (string, error) {
	return "", nil
}

func (pdfObjects) Stat(context.Context, string) (proofs.ObjectInfo, error) {
	return proofs.ObjectInfo{Size: 2048, ContentType: "application/pdf"}, nil
}

func (pdfObjects) Remove(context.Context, string) error { return nil }

type scanFixture struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	super *pgx.Conn
	store *PostgresStore
}

func newScanFixture(t *testing.T) scanFixture {
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
	return scanFixture{ctx: ctx, pool: pool, super: super, store: NewPostgresStore(pool)}
}

func (f scanFixture) submittedProof(t *testing.T, line1 string) uuid.UUID {
	t.Helper()
	hostID := uuid.New()
	dbtest.SignUp(t, f.ctx, f.pool, hostID)
	listing, err := listings.NewListingService(listings.NewPostgresListingStore(f.pool)).CreateListing(f.ctx, hostID, listings.CreateListingInput{
		Title: "Scan House", Description: "A listing used by the document scan integration test.",
		Address: listings.Address{Line1: line1, City: "Phoenix", CountryCode: "US", Latitude: 33.4, Longitude: -112.1},
		Type:    listings.EntirePlace, PropertyType: listings.House, MaxGuests: 4,
		MinimumAgeToBook: 21, Bedrooms: 2, Beds: 2, Bathrooms: 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "property-proofs/" + listing.ID.String() + "/" + uuid.NewString() + ".pdf"
	proof, err := proofs.NewProofService(proofs.NewPostgresProofStore(f.pool), pdfObjects{}).
		Submit(f.ctx, hostID, listing.ID, proofs.UtilityBill, key)
	if err != nil {
		t.Fatal(err)
	}
	return proof.ID
}

func (f scanFixture) dueIDs(t *testing.T) map[uuid.UUID]Due {
	t.Helper()
	due, err := f.store.DueForScan(f.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uuid.UUID]Due{}
	for _, proof := range due {
		byID[proof.ProofID] = proof
	}
	return byID
}

func TestAScannedProofIsNotDueAgain(t *testing.T) {
	f := newScanFixture(t)
	line1 := uuid.NewString()[:6] + " Scan Way"
	proofID := f.submittedProof(t, line1)

	due, ok := f.dueIDs(t)[proofID]
	if !ok || due.Address.Line1 != line1 || due.ContentType != "application/pdf" {
		t.Fatalf("expected the new proof to be due, got %+v", due)
	}

	result := Result{ProofID: proofID, FoundName: "Jason Coawette", NameVerdict: NameMatch, AddressMatched: true, Excerpt: "text", ScannedAt: time.Now()}
	if err := f.store.Record(f.ctx, result); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Record(f.ctx, Result{ProofID: proofID, NameVerdict: NameNoMatch, ScannedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, stillDue := f.dueIDs(t)[proofID]; stillDue {
		t.Fatal("a scanned proof was offered again")
	}
	var verdict string
	if err := f.super.QueryRow(f.ctx, `SELECT name_verdict FROM app.property_proof_scans WHERE proof_id = $1`, proofID).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != string(NameMatch) {
		t.Fatalf("the first scan should be kept, got %q", verdict)
	}
}

func TestScanTextIsForgottenOnceTheDocumentIsDeleted(t *testing.T) {
	f := newScanFixture(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Forget Way")
	if err := f.store.Record(f.ctx, Result{ProofID: proofID, FoundName: "Jason Coawette", NameVerdict: NameMatch, Excerpt: "private text", ScannedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.super.Exec(f.ctx, `UPDATE app.property_proofs SET file_deleted_at = now() WHERE id = $1`, proofID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ForgetDeletedText(f.ctx); err != nil {
		t.Fatal(err)
	}
	var found, excerpt *string
	if err := f.super.QueryRow(f.ctx, `SELECT found_name, excerpt FROM app.property_proof_scans WHERE proof_id = $1`, proofID).Scan(&found, &excerpt); err != nil {
		t.Fatal(err)
	}
	if found != nil || excerpt != nil {
		t.Fatal("scan text was kept after the document was deleted")
	}
}

func TestTheAppRoleCannotWriteScansDirectly(t *testing.T) {
	f := newScanFixture(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Locked Way")
	_, err := f.pool.Exec(f.ctx, `INSERT INTO app.property_proof_scans (proof_id, name_verdict, address_matched, scanned_at) VALUES ($1, 'match', true, now())`, proofID)
	if err == nil {
		t.Fatal("the app role wrote a scan without the scan function")
	}
}

func (f scanFixture) admin(t *testing.T) uuid.UUID {
	t.Helper()
	adminID := uuid.New()
	if _, err := f.super.Exec(f.ctx, `INSERT INTO app.accounts (id, clerk_user_id) VALUES ($1, $2)`, adminID, "user_test_"+adminID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.super.Exec(f.ctx, `INSERT INTO app.admin_accounts (account_id, email) VALUES ($1, $2)`, adminID, adminID.String()+"@admin.test"); err != nil {
		t.Fatal(err)
	}
	return adminID
}

func reviewItem(t *testing.T, items []proofs.ReviewItem, proofID uuid.UUID) proofs.ReviewItem {
	t.Helper()
	for _, item := range items {
		if item.ID == proofID {
			return item
		}
	}
	t.Fatalf("proof %s was not in the review list", proofID)
	return proofs.ReviewItem{}
}

func TestTheReviewListCarriesTheScanOnlyOnceItExists(t *testing.T) {
	f := newScanFixture(t)
	adminID := f.admin(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Review Way")
	reviews := proofs.NewPostgresProofStore(f.pool)

	before, err := reviews.ListSubmitted(f.ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if scan := reviewItem(t, before, proofID).Scan; scan != nil {
		t.Fatalf("expected no scan yet, got %+v", scan)
	}

	if err := f.store.Record(f.ctx, Result{ProofID: proofID, FoundName: "JASON COAWETTE", NameVerdict: NameMatch, AddressMatched: true, ScannedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	after, err := reviews.ListSubmitted(f.ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	scan := reviewItem(t, after, proofID).Scan
	if scan == nil || scan.FoundName != "JASON COAWETTE" || scan.NameVerdict != string(NameMatch) || !scan.AddressMatched {
		t.Fatalf("unexpected scan %+v", scan)
	}
}
