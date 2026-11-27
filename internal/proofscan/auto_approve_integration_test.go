package proofscan

import (
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/proofs"
	"github.com/google/uuid"
)

func (f scanFixture) verifyIdentity(t *testing.T, hostID uuid.UUID, reviewState *string) {
	t.Helper()
	verificationID := uuid.New()
	if _, err := f.super.Exec(f.ctx, `
		INSERT INTO app.identity_verifications (id, account_id, persona_inquiry_id, status, review_state)
		VALUES ($1, $2, $3, 'completed', $4)`, verificationID, hostID, "inq_"+verificationID.String(), reviewState); err != nil {
		t.Fatal(err)
	}
	if _, err := f.super.Exec(f.ctx, `
		INSERT INTO app.identity_names (verification_id, account_id, first_name, middle_name, last_name, suffix, verified_at)
		VALUES ($1, $2, 'Jason', '', 'Coawette', '', now())`, verificationID, hostID); err != nil {
		t.Fatal(err)
	}
}

func (f scanFixture) hostOf(t *testing.T, proofID uuid.UUID) uuid.UUID {
	t.Helper()
	var hostID uuid.UUID
	if err := f.super.QueryRow(f.ctx, `SELECT host_id FROM app.property_proofs WHERE id = $1`, proofID).Scan(&hostID); err != nil {
		t.Fatal(err)
	}
	return hostID
}

type proofState struct {
	status       string
	reviewedBy   *uuid.UUID
	autoApproved bool
	scanned      bool
}

func (f scanFixture) state(t *testing.T, proofID uuid.UUID) proofState {
	t.Helper()
	var state proofState
	if err := f.super.QueryRow(f.ctx, `
		SELECT p.status, p.reviewed_by, p.auto_approved_at IS NOT NULL,
		       EXISTS (SELECT 1 FROM app.property_proof_scans s WHERE s.proof_id = p.id)
		FROM app.property_proofs p WHERE p.id = $1`, proofID).Scan(&state.status, &state.reviewedBy, &state.autoApproved, &state.scanned); err != nil {
		t.Fatal(err)
	}
	return state
}

func clearMatch(proofID uuid.UUID) Result {
	billDate := time.Now().UTC().AddDate(0, 0, -20).Truncate(24 * time.Hour)
	return Result{
		ProofID: proofID, FoundName: "Jason Coawette", NameVerdict: NameMatch, AddressMatched: true,
		Excerpt: "bill text", ScannedAt: time.Now(), DocumentDate: &billDate,
	}
}

func TestAClearMatchIsApprovedAutomaticallyAndCanBeWithdrawn(t *testing.T) {
	f := newScanFixture(t)
	adminID := f.admin(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Clear Way")
	f.verifyIdentity(t, f.hostOf(t, proofID), nil)

	approved, err := f.store.Record(f.ctx, clearMatch(proofID))
	if err != nil || !approved {
		t.Fatalf("approved = %v, err = %v", approved, err)
	}
	state := f.state(t, proofID)
	if state.status != "approved" || state.reviewedBy != nil || !state.autoApproved || !state.scanned {
		t.Fatalf("unexpected state %+v", state)
	}

	reviews := proofs.NewProofService(proofs.NewPostgresProofStore(f.pool), pdfObjects{})
	listed, err := reviews.ListApproved(f.ctx, adminID)
	if err != nil {
		t.Fatal(err)
	}
	if item := reviewItem(t, listed, proofID); !item.AutoApproved || item.ReviewedBy != nil {
		t.Fatalf("the approved list should mark it automatic, got %+v", item.Proof)
	}
	if err := reviews.Revoke(f.ctx, adminID, proofID, "Spot check: wrong home."); err != nil {
		t.Fatalf("withdraw an automatic approval: %v", err)
	}
	if state := f.state(t, proofID); state.status != "rejected" || state.reviewedBy == nil || *state.reviewedBy != adminID {
		t.Fatalf("unexpected state after withdrawal %+v", state)
	}
}

func TestOnlyAClearMatchIsApprovedAutomatically(t *testing.T) {
	f := newScanFixture(t)
	pending := "pending"
	oldBill := time.Now().UTC().AddDate(0, 0, -120).Truncate(24 * time.Hour)
	cases := []struct {
		name          string
		change        func(*Result)
		setup         func(t *testing.T, proofID uuid.UUID)
		skipIdentity  bool
		identityState *string
		autoApproveOn bool
	}{
		{name: "the name only looks close", change: func(r *Result) { r.NameVerdict = NameLookCloser }, autoApproveOn: true},
		{name: "a different person is named", change: func(r *Result) { r.NameVerdict = NameNoMatch; r.FoundName = "Water SAMPLE" }, autoApproveOn: true},
		{name: "no name was found", change: func(r *Result) { r.NameVerdict = NameNotFound; r.FoundName = "" }, autoApproveOn: true},
		{name: "the address is missing", change: func(r *Result) { r.AddressMatched = false }, autoApproveOn: true},
		{name: "the scan failed", change: func(r *Result) { r.Error = "no text was found in the document" }, autoApproveOn: true},
		{name: "the bill has no date", change: func(r *Result) { r.DocumentDate = nil }, autoApproveOn: true},
		{name: "the bill is more than 90 days old", change: func(r *Result) { r.DocumentDate = &oldBill }, autoApproveOn: true},
		{name: "the host has no verified ID", skipIdentity: true, autoApproveOn: true},
		{name: "the ID check is waiting for review", identityState: &pending, autoApproveOn: true},
		{name: "it is a deed", setup: func(t *testing.T, proofID uuid.UUID) {
			if _, err := f.super.Exec(f.ctx, `UPDATE app.property_proofs SET document_type = 'deed' WHERE id = $1`, proofID); err != nil {
				t.Fatal(err)
			}
		}, autoApproveOn: true},
		{name: "a company owns the home", setup: func(t *testing.T, proofID uuid.UUID) {
			if _, err := f.super.Exec(f.ctx, `UPDATE app.property_proofs SET relationship = 'company_owner', owner_name = 'Jason Coawette' WHERE id = $1`, proofID); err != nil {
				t.Fatal(err)
			}
		}, autoApproveOn: true},
		{name: "auto-approve is switched off", autoApproveOn: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proofID := f.submittedProof(t, uuid.NewString()[:6]+" Wait Way")
			if !c.skipIdentity {
				f.verifyIdentity(t, f.hostOf(t, proofID), c.identityState)
			}
			if c.setup != nil {
				c.setup(t, proofID)
			}
			result := clearMatch(proofID)
			if c.change != nil {
				c.change(&result)
			}
			store := NewPostgresStore(f.pool, c.autoApproveOn)
			approved, err := store.Record(f.ctx, result)
			if err != nil || approved {
				t.Fatalf("approved = %v, err = %v", approved, err)
			}
			if state := f.state(t, proofID); state.status != "submitted" || state.autoApproved || !state.scanned {
				t.Fatalf("expected it to wait for a person, got %+v", state)
			}
		})
	}
}

func TestAProofDecidedBeforeItsScanIsLeftAlone(t *testing.T) {
	f := newScanFixture(t)
	adminID := f.admin(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Early Way")
	f.verifyIdentity(t, f.hostOf(t, proofID), nil)
	reviews := proofs.NewProofService(proofs.NewPostgresProofStore(f.pool), pdfObjects{})
	if err := reviews.Decide(f.ctx, adminID, proofID, proofs.Reject, "The document is hard to read or cut off."); err != nil {
		t.Fatal(err)
	}

	approved, err := f.store.Record(f.ctx, clearMatch(proofID))
	if err != nil || approved {
		t.Fatalf("approved = %v, err = %v", approved, err)
	}
	if state := f.state(t, proofID); state.status != "rejected" || state.autoApproved || state.scanned {
		t.Fatalf("a decided proof was changed by its scan: %+v", state)
	}
}

func TestTheAppRoleCannotMarkAProofApprovedAutomatically(t *testing.T) {
	f := newScanFixture(t)
	adminID := f.admin(t)
	proofID := f.submittedProof(t, uuid.NewString()[:6]+" Forge Way")
	_, err := f.pool.Exec(f.ctx, `UPDATE app.property_proofs SET auto_approved_at = now() WHERE id = $1`, proofID)
	if err == nil {
		t.Fatalf("the app role set auto_approved_at directly (admin %s)", adminID)
	}
}
