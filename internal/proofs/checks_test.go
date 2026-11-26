package proofs

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type checkStoreStub struct {
	*storeStub
	latest    *Proof
	name      IDName
	unnamed   bool
	subject   CheckSubject
	inquiry   *CheckInquiry
	inquiries []CheckInquiry
	kept      []Proof
	keepErr   error
	approved  int
	declined  int
}

func (store *checkStoreStub) DeclineFromInquiry(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	store.declined++
	return true, nil
}

func (store *checkStoreStub) Latest(context.Context, uuid.UUID, uuid.UUID) (Proof, bool, error) {
	if store.latest == nil {
		return Proof{}, false, nil
	}
	return *store.latest, true, nil
}

func (store *checkStoreStub) CheckSubject(context.Context, uuid.UUID, uuid.UUID) (CheckSubject, error) {
	return store.subject, nil
}

func (store *checkStoreStub) IDName(context.Context, uuid.UUID) (IDName, bool, error) {
	return store.name, !store.unnamed, nil
}

func (store *checkStoreStub) SaveInquiry(_ context.Context, inquiry CheckInquiry) error {
	store.inquiries = append(store.inquiries, inquiry)
	return nil
}

func (store *checkStoreStub) FindInquiry(context.Context, uuid.UUID, string) (CheckInquiry, bool, error) {
	if store.inquiry == nil {
		return CheckInquiry{}, false, nil
	}
	return *store.inquiry, true, nil
}

func (store *checkStoreStub) HasProofForInquiry(context.Context, uuid.UUID, string) (bool, error) {
	return len(store.kept) > 0, nil
}

func (store *checkStoreStub) SaveFromInquiry(_ context.Context, proof Proof) error {
	if store.keepErr != nil {
		return store.keepErr
	}
	store.kept = append(store.kept, proof)
	return nil
}

func (store *checkStoreStub) ApproveFromInquiry(context.Context, uuid.UUID, string, time.Time) (bool, error) {
	store.approved++
	return true, nil
}

type starterStub struct{ fields map[string]string }

func (starter *starterStub) StartInquiry(_ context.Context, _ string, fields map[string]string) (CheckTicket, error) {
	starter.fields = fields
	return CheckTicket{InquiryID: "inq_doc", SessionToken: "token"}, nil
}

type idStub bool

func (verified idStub) HasCurrentID(context.Context, uuid.UUID) (bool, error) {
	return bool(verified), nil
}

func checkStore(hostID uuid.UUID) *checkStoreStub {
	return &checkStoreStub{
		storeStub: ownedBy(hostID),
		name:      IDName{First: "Jordan", Last: "Rivera"},
		subject:   CheckSubject{Address: PropertyAddress{Line1: "42 North Palm Street", City: "Phoenix", PostalCode: "85004", CountryCode: "US"}},
		inquiry:   &CheckInquiry{InquiryID: "inq_doc", ListingID: uuid.New(), HostID: hostID, DocumentType: Deed},
	}
}

type payload struct {
	status, check, name, address, issued string
}

func goodPayload() payload {
	return payload{status: "passed", check: "passed", name: "JORDAN A RIVERA", address: "42 N Palm St, Phoenix AZ 85004-1234"}
}

func (p payload) body(t *testing.T) []byte {
	t.Helper()
	extracted := func(field, value string) map[string]any {
		return map[string]any{"field-name": field, "results": []any{map[string]any{"value": value}}}
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"attributes": map[string]any{"payload": map[string]any{
		"included": []any{map[string]any{"type": "verification/document", "attributes": map[string]any{
			"status":               p.status,
			"checks":               []any{map[string]any{"name": "document_type_detection", "status": p.check}},
			"extraction-responses": []any{extracted("owner-name", p.name), extracted("property_address", p.address), extracted("issue-date", p.issued)},
		}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProcessRecordsEveryResultAndApprovesOnlyCleanMatches(t *testing.T) {
	cases := []struct {
		name     string
		event    string
		change   func(*payload)
		approved int
	}{
		{"clean match is approved", "inquiry.completed", func(*payload) {}, 1},
		{"failed inquiry goes to review", "inquiry.failed", func(*payload) {}, 0},
		{"marked for review goes to review", "inquiry.marked-for-review", func(*payload) {}, 0},
		{"tentative pass goes to review", "inquiry.completed", func(p *payload) { p.status = "tentatively_passed" }, 0},
		{"any failed check goes to review", "inquiry.completed", func(p *payload) { p.check = "failed" }, 0},
		{"another person goes to review", "inquiry.completed", func(p *payload) { p.name = "Sam Rivera" }, 0},
		{"missing name goes to review", "inquiry.completed", func(p *payload) { p.name = "" }, 0},
		{"another street number goes to review", "inquiry.completed", func(p *payload) { p.address = "142 N Palm St 85004" }, 0},
		{"another postal code goes to review", "inquiry.completed", func(p *payload) { p.address = "42 N Palm St 85281" }, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hostID := uuid.New()
			store := checkStore(hostID)
			service := NewCheckService(store, &starterStub{}, idStub(true))
			document := goodPayload()
			testCase.change(&document)

			claimed, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, InquiryID: "inq_doc", Name: testCase.event, Body: document.body(t)})

			if err != nil || !claimed {
				t.Fatalf("claimed %v, error %v", claimed, err)
			}
			if len(store.kept) != 1 || store.kept[0].ObjectKey != "" || store.kept[0].InquiryID != "inq_doc" || store.approved != testCase.approved {
				t.Fatalf("kept %+v, approved %d", store.kept, store.approved)
			}
		})
	}
}

func TestProcessLeavesIdentityInquiriesAlone(t *testing.T) {
	store := checkStore(uuid.New())
	store.inquiry = nil
	service := NewCheckService(store, nil, idStub(true))

	claimed, err := service.Process(context.Background(), CheckEvent{Name: "inquiry.completed"})

	if claimed || err != nil {
		t.Fatalf("claimed %v, error %v", claimed, err)
	}
}

func TestProcessAsksPersonaToRetryWhenSavingFails(t *testing.T) {
	hostID := uuid.New()
	store := checkStore(hostID)
	store.keepErr = errors.New("offline")
	service := NewCheckService(store, nil, idStub(true))

	_, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, Name: "inquiry.completed", Body: goodPayload().body(t)})

	if err == nil || store.approved != 0 {
		t.Fatalf("error %v, approved %d", err, store.approved)
	}
}

func TestStart(t *testing.T) {
	hostID := uuid.New()
	cases := []struct {
		name     string
		verified bool
		change   func(*checkStoreStub)
		wantErr  error
	}{
		{"starts with the ID name and listing address", true, func(*checkStoreStub) {}, nil},
		{"unverified host is refused", false, func(*checkStoreStub) {}, ErrIDRequired},
		{"host without an ID name is refused", true, func(s *checkStoreStub) { s.unnamed = true }, ErrIDRequired},
		{"another host's listing is refused", true, func(s *checkStoreStub) { s.owner = uuid.New() }, ErrNotListingOwner},
		{"pending proof is refused", true, func(s *checkStoreStub) { s.latest = &Proof{Status: Submitted} }, ErrProofPending},
		{"approved proof is refused", true, func(s *checkStoreStub) { s.latest = &Proof{Status: Approved} }, ErrAlreadyProven},
		{"rejected proof can try again", true, func(s *checkStoreStub) { s.latest = &Proof{Status: Rejected} }, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := checkStore(hostID)
			testCase.change(store)
			starter := &starterStub{}
			service := NewCheckService(store, starter, idStub(testCase.verified))

			_, err := service.Start(context.Background(), hostID, uuid.New(), UtilityBill)

			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if testCase.wantErr == nil && (starter.fields["name-last"] != "Rivera" || starter.fields["address-postal-code"] != "85004" || len(store.inquiries) != 1) {
				t.Fatalf("fields %v, inquiries %d", starter.fields, len(store.inquiries))
			}
		})
	}
}

func TestMatching(t *testing.T) {
	listing := PropertyAddress{Line1: "10 Elm Road", Line2: "Apt 4", City: "Mesa", PostalCode: "85201"}
	if !addressMatches("10 Elm Rd #4 Mesa 85201", listing) {
		t.Fatal("same unit must match")
	}
	if addressMatches("10 Elm Rd Apt 5 Mesa 85201", listing) {
		t.Fatal("another unit must not match")
	}
	listing.Line2 = ""
	if addressMatches("10 Elm Rd Unit 5 Mesa 85201", listing) {
		t.Fatal("a unit the listing does not name must not match")
	}
}

func TestNameMustBelongToOnePerson(t *testing.T) {
	smith := IDName{First: "John", Middle: "Michael", Last: "Smith"}
	junior := IDName{First: "John", Last: "Smith", Suffix: "Jr"}
	cases := []struct {
		document string
		id       IDName
		want     bool
	}{
		{"John Smith", smith, true},
		{"JOHN M SMITH", smith, true},
		{"Mr. John Michael Smith", smith, true},
		{"Smith, John M", smith, true},
		{"Mary Jones and John Smith", smith, true},
		{"John Smith & Mary Jones", smith, true},
		{"Mary Smith and John Doe", smith, false},
		{"Mary Smith & John Doe", smith, false},
		{"John Doe, Mary Smith", smith, false},
		{"Mary Smith, John Doe", smith, false},
		{"John Doe Smith", smith, false},
		{"John Smith Properties LLC", smith, false},
		{"Smith John", smith, false},
		{"John Smith Jr", junior, true},
		{"John Smith, Jr.", junior, true},
		{"Smith, John Jr", junior, true},
		{"John Smith", junior, false},
		{"John Smith Jr", smith, false},
	}
	for _, testCase := range cases {
		if got := nameMatches(testCase.document, testCase.id); got != testCase.want {
			t.Errorf("%q: matched = %v, want %v", testCase.document, got, testCase.want)
		}
	}
}

func TestAddressMustAgreeOnStateAndUnit(t *testing.T) {
	listing := PropertyAddress{Line1: "42 Elm Street", City: "Springfield", Region: "Illinois", PostalCode: "62701"}
	for document, want := range map[string]bool{
		"42 Elm St, Springfield, IL 62701":     true,
		"42 Elm St Springfield Illinois 62701": true,
		"42 Elm St, Springfield, MO 62701":     false,
		"42 Elm St, Springfield 62701":         false,
		"42 Elm St #4, Springfield, IL 62701":  false,
	} {
		if got := addressMatches(document, listing); got != want {
			t.Errorf("%q: matched = %v, want %v", document, got, want)
		}
	}
}

func TestStateIsReadFromTheEndOfTheAddress(t *testing.T) {
	cases := []struct {
		region, document string
		want             bool
	}{
		{"IL", "42 Oak Court, Springfield, IL 62701-1234, USA", true},
		{"CT", "42 Oak Court, Springfield, MA 01101", false},
		{"CT", "42 Oak Court, Hartford, CT 01101", true},
		{"North Carolina", "42 Oak Court, Raleigh, North Carolina 01101", true},
		{"NE", "42 Oak Court NE, Albany, NY 01101", false},
		{"Kansas", "42 Oak Court, Kansas City, MO 01101", false},
		{"Virginia", "42 Oak Court, Charleston, West Virginia 01101", false},
	}
	for _, testCase := range cases {
		postal := "01101"
		if testCase.region == "IL" {
			postal = "62701"
		}
		listing := PropertyAddress{Line1: "42 Oak Court", Region: testCase.region, PostalCode: postal}
		if got := addressMatches(testCase.document, listing); got != testCase.want {
			t.Errorf("%s against %q: matched = %v, want %v", testCase.region, testCase.document, got, testCase.want)
		}
	}
}

func TestOnlyDeedsAndFreshTaxBillsApproveThemselves(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		document DocumentType
		issued   string
		approved int
	}{
		{"deed needs no date", Deed, "", 1},
		{"utility bill is reviewed by hand", UtilityBill, "2026-11-01", 0},
		{"lease is reviewed by hand", Lease, "2026-11-01", 0},
		{"fresh tax bill", PropertyTaxBill, "2026-10-01", 1},
		{"old tax bill", PropertyTaxBill, "2026-01-01", 0},
		{"tax bill without a date", PropertyTaxBill, "", 0},
		{"tax bill dated in the future", PropertyTaxBill, "2027-01-01", 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			hostID := uuid.New()
			store := checkStore(hostID)
			store.inquiry.DocumentType = testCase.document
			service := NewCheckService(store, nil, idStub(true))
			service.now = func() time.Time { return now }
			document := goodPayload()
			document.issued = testCase.issued

			_, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, InquiryID: "inq_doc", Name: "inquiry.completed", Body: document.body(t)})

			if err != nil || store.approved != testCase.approved {
				t.Fatalf("approved %d, error %v", store.approved, err)
			}
		})
	}
}

func TestDeclineRevokesEvenWithoutADocumentInThePayload(t *testing.T) {
	hostID := uuid.New()
	store := checkStore(hostID)
	service := NewCheckService(store, nil, idStub(true))

	claimed, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, InquiryID: "inq_doc", Name: "inquiry.declined", Body: []byte(`{}`)})

	if !claimed || err != nil || store.declined != 1 || store.approved != 0 {
		t.Fatalf("claimed %v, error %v, declined %d", claimed, err, store.declined)
	}
}
