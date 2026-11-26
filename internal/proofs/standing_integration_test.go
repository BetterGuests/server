package proofs

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/db/dbtest"
	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
	"github.com/BetterGuests/BetterGuests-Server/internal/notifications"
	"github.com/BetterGuests/BetterGuests-Server/internal/verification"
	"github.com/google/uuid"
)

const day = 24 * time.Hour

func (f fixture) pass(t *testing.T, accountID uuid.UUID, name verification.IDName, mark []byte, at time.Time) (string, []verification.ReviewReason) {
	t.Helper()
	store := verification.NewPostgresVerificationStore(f.pool)
	inquiryID := f.open(t, accountID, at)
	completion := verification.Completion{AccountID: accountID, InquiryID: inquiryID, OccurredAt: at, Name: name, Fingerprint: mark}
	held, err := store.Complete(f.ctx, completion, func(history verification.History) []verification.ReviewReason {
		var reasons []verification.ReviewReason
		if history.DuplicateIdentity {
			reasons = append(reasons, verification.ReasonDuplicateIdentity)
		}
		if history.HasDecline && !history.HasEarlierPass {
			reasons = append(reasons, verification.ReasonPreviouslyDeclined)
		}
		if history.PreviousName.First != "" && history.PreviousName.First != name.First {
			reasons = append(reasons, verification.ReasonNameChanged)
		}
		return reasons
	})
	if err != nil {
		t.Fatal(err)
	}
	return inquiryID, held
}

func (f fixture) open(t *testing.T, accountID uuid.UUID, at time.Time) string {
	t.Helper()
	dbtest.SignUp(t, f.ctx, f.pool, accountID)
	inquiryID := "inq_" + uuid.NewString()
	err := verification.NewPostgresVerificationStore(f.pool).Create(f.ctx, verification.Verification{
		ID: uuid.New(), AccountID: accountID, PersonaInquiryID: inquiryID, Status: verification.StatusCreated, CreatedAt: at, UpdatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return inquiryID
}

func (f fixture) verified(t *testing.T, accountID uuid.UUID) bool {
	t.Helper()
	_, current, err := verification.NewCurrentVerifier(verification.NewPostgresVerificationStore(f.pool)).CurrentVerificationID(f.ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func (f fixture) published(t *testing.T) (uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	listing := f.draft(t, uuid.New())
	adminID := f.makeAdmin(t)
	proof := f.submit(t, listing)
	if err := f.proofs.Decide(f.ctx, adminID, proof.ID, Approve, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.listings.PublishListing(f.ctx, listing.HostID, listing.ID); err != nil {
		t.Fatal(err)
	}
	return listing.HostID, listing.ID, proof.ID, adminID
}

func (f fixture) applyPauses(t *testing.T) {
	t.Helper()
	if err := verification.NewVerificationPauseService(verification.NewPostgresVerificationStore(f.pool)).RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) queueAlerts(t *testing.T) {
	t.Helper()
	if _, err := notifications.NewPostgresNotificationStore(f.pool).QueueTeamAlerts(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) sweep(t *testing.T) {
	t.Helper()
	f.applyPauses(t)
	f.queueAlerts(t)
}

func (f fixture) paused(t *testing.T, listingID uuid.UUID) bool {
	t.Helper()
	var paused bool
	if err := f.super.QueryRow(f.ctx, `SELECT EXISTS (SELECT 1 FROM app.listing_suspensions WHERE listing_id = $1 AND lifted_at IS NULL)`, listingID).Scan(&paused); err != nil {
		t.Fatal(err)
	}
	return paused
}

func (f fixture) kinds(t *testing.T, accountID uuid.UUID) []string {
	t.Helper()
	var kinds []string
	if err := f.super.QueryRow(f.ctx, `SELECT coalesce(array_agg(kind ORDER BY kind), '{}') FROM app.notifications WHERE account_id = $1`, accountID).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	return kinds
}

func TestExpiredIDPausesAfterTheGracePeriodAndResumesOnReverification(t *testing.T) {
	f := newFixture(t)
	hostID, listingID, _, _ := f.published(t)
	name := verification.IDName{First: "John", Last: "Smith"}
	now := time.Now().UTC().Truncate(time.Second)

	f.pass(t, hostID, name, nil, now.Add(-340*day))
	f.sweep(t)
	if f.paused(t, listingID) || !slices.Contains(f.kinds(t, hostID), "id_expiring") {
		t.Fatalf("expected only a reminder: %v", f.kinds(t, hostID))
	}
	age := func(days int) {
		if _, err := f.super.Exec(f.ctx, `UPDATE app.identity_verifications SET last_event_at = now() - make_interval(days => $2) WHERE account_id = $1`, hostID, days); err != nil {
			t.Fatal(err)
		}
	}
	age(394)
	f.sweep(t)
	if f.paused(t, listingID) {
		t.Fatal("a listing must stay live during the grace period")
	}
	age(396)
	f.sweep(t)
	if !f.paused(t, listingID) {
		t.Fatal("a listing must pause after the grace period")
	}
	f.pass(t, hostID, name, nil, now)
	f.sweep(t)
	if f.paused(t, listingID) {
		t.Fatal("re-verifying must resume the listing")
	}
}

func TestDeclineRevokesEarlierPassesAndHoldsTheNextOne(t *testing.T) {
	f := newFixture(t)
	hostID, listingID, _, adminID := f.published(t)
	store := verification.NewPostgresVerificationStore(f.pool)
	name := verification.IDName{First: "John", Last: "Smith"}
	now := time.Now().UTC().Truncate(time.Second)

	f.pass(t, hostID, name, nil, now.Add(-time.Hour))
	declined := f.open(t, hostID, now.Add(-time.Minute))
	if err := store.ApplyStatus(f.ctx, hostID, declined, verification.StatusDeclined, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.sweep(t)
	if f.verified(t, hostID) || !f.paused(t, listingID) {
		t.Fatal("a decline must remove verification and pause the listing")
	}
	_, held := f.pass(t, hostID, name, nil, now)
	if !slices.Equal(held, []verification.ReviewReason{verification.ReasonPreviouslyDeclined}) || f.verified(t, hostID) {
		t.Fatalf("the next pass must wait for review: %v", held)
	}
	pending, err := store.PendingReviews(f.ctx, adminID, 200)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(pending, func(review verification.PendingReview) bool { return review.AccountID == hostID })
	if index < 0 {
		t.Fatalf("review missing from the queue: %+v", pending)
	}
	if _, err := store.PendingReviews(f.ctx, hostID, 200); !errors.Is(err, verification.ErrNotAdmin) {
		t.Fatalf("only admins may list reviews, got %v", err)
	}
	if err := store.DecideReview(f.ctx, hostID, pending[index].VerificationID, true, now); !errors.Is(err, verification.ErrNotAdmin) {
		t.Fatalf("a host must not clear their own check, got %v", err)
	}
	if err := store.DecideReview(f.ctx, adminID, pending[index].VerificationID, true, now); err != nil {
		t.Fatal(err)
	}
	f.sweep(t)
	if !f.verified(t, hostID) || f.paused(t, listingID) {
		t.Fatal("clearing the review must verify the host and resume the listing")
	}
}

func TestApprovedNameChangeSendsPropertyProofsBack(t *testing.T) {
	f := newFixture(t)
	hostID, listingID, _, adminID := f.published(t)
	store := verification.NewPostgresVerificationStore(f.pool)
	now := time.Now().UTC().Truncate(time.Second)

	f.pass(t, hostID, verification.IDName{First: "John", Last: "Smith"}, nil, now.Add(-time.Hour))
	changed, held := f.pass(t, hostID, verification.IDName{First: "Mary", Last: "Jones"}, nil, now)
	if len(held) != 1 || !f.verified(t, hostID) {
		t.Fatalf("a name change waits while the old ID stays valid: %v", held)
	}
	pending, err := store.PendingReviews(f.ctx, adminID, 200)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(pending, func(review verification.PendingReview) bool { return review.InquiryID == changed })
	if index < 0 || pending[index].NewName != "Mary Jones" || pending[index].PreviousName != "John Smith" {
		t.Fatalf("review = %+v", pending)
	}
	if err := store.DecideReview(f.ctx, adminID, pending[index].VerificationID, true, now); err != nil {
		t.Fatal(err)
	}
	f.sweep(t)
	latest, _, err := NewPostgresProofStore(f.pool).Latest(f.ctx, hostID, listingID)
	if err != nil || latest.Status != Rejected || !f.paused(t, listingID) {
		t.Fatalf("proof = %+v, err = %v", latest.Status, err)
	}
}

func TestOneIdentityOnTwoAccountsIsHeld(t *testing.T) {
	f := newFixture(t)
	mark := []byte(uuid.NewString()[:32])
	name := verification.IDName{First: "John", Last: "Smith"}
	now := time.Now().UTC().Truncate(time.Second)
	first, second := uuid.New(), uuid.New()

	if _, held := f.pass(t, first, name, mark, now); len(held) != 0 {
		t.Fatalf("first account held: %v", held)
	}
	if _, held := f.pass(t, second, name, mark, now); !slices.Equal(held, []verification.ReviewReason{verification.ReasonDuplicateIdentity}) {
		t.Fatalf("second account: %v", held)
	}
	if _, held := f.pass(t, first, name, mark, now.Add(time.Second)); len(held) != 0 {
		t.Fatalf("the original account must renew freely: %v", held)
	}
	if _, err := verification.NewPostgresVerificationStore(f.pool).RequestDeletion(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := f.super.QueryRow(f.ctx, `SELECT count(*) FROM app.identity_fingerprints WHERE account_id = $1`, first).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("deleting ID data must remove the fingerprint: %d %v", kept, err)
	}
}

func TestAdminRevokingAProofPausesTheListing(t *testing.T) {
	f := newFixture(t)
	hostID, listingID, proofID, adminID := f.published(t)

	if err := f.proofs.Revoke(f.ctx, hostID, proofID, "mine"); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("expected ErrNotAdmin, got %v", err)
	}
	if err := f.proofs.Revoke(f.ctx, adminID, proofID, "Lease has ended"); err != nil {
		t.Fatal(err)
	}
	if err := f.proofs.Revoke(f.ctx, adminID, proofID, "again"); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("expected ErrNotApproved, got %v", err)
	}
	f.sweep(t)
	if !f.paused(t, listingID) {
		t.Fatal("a revoked proof must pause the listing")
	}
	again := f.submit(t, listings.Listing{ID: listingID, HostID: hostID})
	if err := f.proofs.Decide(f.ctx, adminID, again.ID, Approve, ""); err != nil {
		t.Fatal(err)
	}
	f.sweep(t)
	if f.paused(t, listingID) {
		t.Fatal("a newly approved proof must resume the listing")
	}
}

func TestTheAlertSweepLeavesPausingToTheHourlyJob(t *testing.T) {
	f := newFixture(t)
	hostID, listingID, _, _ := f.published(t)
	f.pass(t, hostID, verification.IDName{First: "John", Last: "Smith"}, nil, time.Now().UTC().Add(-400*day))

	f.queueAlerts(t)
	if f.paused(t, listingID) {
		t.Fatal("the per-minute alert sweep must not pause listings")
	}
	f.applyPauses(t)
	if !f.paused(t, listingID) {
		t.Fatal("the pause job must pause a listing whose host ID expired past the grace period")
	}
	f.applyPauses(t)
	if !f.paused(t, listingID) {
		t.Fatal("running the pause job again must keep the listing paused")
	}
}

func TestDeletingIDDataEndsTheVerificationSoOneIDCannotVerifyTwoAccounts(t *testing.T) {
	f := newFixture(t)
	mark := []byte(uuid.NewString()[:32])
	name := verification.IDName{First: "Jane", Last: "Doe"}
	now := time.Now().UTC().Truncate(time.Second)
	first, second := uuid.New(), uuid.New()

	f.pass(t, first, name, mark, now)
	if !f.verified(t, first) {
		t.Fatal("the first account must be verified")
	}
	if _, err := verification.NewPostgresVerificationStore(f.pool).RequestDeletion(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	if f.verified(t, first) {
		t.Fatal("deleting ID data must end the verification it backed")
	}
	f.pass(t, second, name, mark, now.Add(time.Second))
	if f.verified(t, first) && f.verified(t, second) {
		t.Fatal("one ID must never leave two accounts verified")
	}
}

func TestACheckFinishingAfterADeletionRequestStaysRevoked(t *testing.T) {
	f := newFixture(t)
	accountID := uuid.New()
	store := verification.NewPostgresVerificationStore(f.pool)
	now := time.Now().UTC().Truncate(time.Second)
	inquiryID := f.open(t, accountID, now)

	if _, err := store.RequestDeletion(f.ctx, accountID); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyStatus(f.ctx, accountID, inquiryID, verification.StatusCompleted, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	completion := verification.Completion{AccountID: accountID, InquiryID: inquiryID, OccurredAt: now.Add(2 * time.Second), Fingerprint: []byte(uuid.NewString()[:32])}
	if _, err := store.Complete(f.ctx, completion, func(verification.History) []verification.ReviewReason { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.verified(t, accountID) {
		t.Fatal("a check the person asked to delete must not count as verified when it finishes")
	}
	var kept int
	if err := f.super.QueryRow(f.ctx, `SELECT count(*) FROM app.identity_fingerprints WHERE account_id = $1`, accountID).Scan(&kept); err != nil || kept != 0 {
		t.Fatalf("no fingerprint may be stored after a deletion request: %d %v", kept, err)
	}
}
