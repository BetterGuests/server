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

type filesStub struct {
	data []byte
	err  error
}

func (files filesStub) Fetch(context.Context, string, int64) ([]byte, error) {
	return files.data, files.err
}

var pdfBytes = []byte("%PDF-1.7 test document")

func checkStore(hostID uuid.UUID) *checkStoreStub {
	return &checkStoreStub{
		storeStub: ownedBy(hostID),
		name:      IDName{First: "Jordan", Last: "Rivera"},
		subject:   CheckSubject{Address: PropertyAddress{Line1: "42 North Palm Street", City: "Phoenix", PostalCode: "85004", CountryCode: "US"}},
		inquiry:   &CheckInquiry{InquiryID: "inq_doc", ListingID: uuid.New(), HostID: hostID, DocumentType: UtilityBill},
	}
}

type payload struct {
	status, check, name, address string
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
			"files":                []any{map[string]any{"filename": "bill.pdf", "url": "https://files.withpersona.com/bill.pdf"}},
			"extraction-responses": []any{extracted("owner-name", p.name), extracted("property_address", p.address)},
		}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProcessKeepsEveryDocumentAndApprovesOnlyCleanMatches(t *testing.T) {
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
			service := NewCheckService(store, &starterStub{}, idStub(true), &objectsStub{}, filesStub{data: pdfBytes})
			document := goodPayload()
			testCase.change(&document)

			claimed, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, InquiryID: "inq_doc", Name: testCase.event, Body: document.body(t)})

			if err != nil || !claimed {
				t.Fatalf("claimed %v, error %v", claimed, err)
			}
			if len(store.kept) != 1 || store.kept[0].ContentType != "application/pdf" || store.approved != testCase.approved {
				t.Fatalf("kept %+v, approved %d", store.kept, store.approved)
			}
		})
	}
}

func TestProcessLeavesIdentityInquiriesAlone(t *testing.T) {
	store := checkStore(uuid.New())
	store.inquiry = nil
	service := NewCheckService(store, nil, idStub(true), nil, filesStub{})

	claimed, err := service.Process(context.Background(), CheckEvent{Name: "inquiry.completed"})

	if claimed || err != nil {
		t.Fatalf("claimed %v, error %v", claimed, err)
	}
}

func TestProcessAsksPersonaToRetryWhenTheDownloadFails(t *testing.T) {
	hostID := uuid.New()
	store := checkStore(hostID)
	service := NewCheckService(store, nil, idStub(true), &objectsStub{}, filesStub{err: errors.New("offline")})

	_, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, Name: "inquiry.completed", Body: goodPayload().body(t)})

	if err == nil || len(store.kept) != 0 || store.approved != 0 {
		t.Fatalf("error %v, kept %d", err, len(store.kept))
	}
}

func TestProcessDropsFilesThatAreNotDocuments(t *testing.T) {
	hostID := uuid.New()
	store := checkStore(hostID)
	service := NewCheckService(store, nil, idStub(true), &objectsStub{}, filesStub{data: []byte("<html></html>")})

	_, err := service.Process(context.Background(), CheckEvent{AccountID: hostID, Name: "inquiry.completed", Body: goodPayload().body(t)})

	if err != nil || len(store.kept) != 0 || store.approved != 0 {
		t.Fatalf("error %v, kept %d", err, len(store.kept))
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
			service := NewCheckService(store, starter, idStub(testCase.verified), &objectsStub{}, filesStub{})

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
	if nameMatches("Jordan Rivera Jr", IDName{First: "Jordan", Last: "Rivera"}) {
		t.Fatal("a generation suffix on only one side must not match")
	}
}

func TestFileFetcherRefusesOtherHosts(t *testing.T) {
	for _, address := range []string{"http://files.withpersona.com/a", "https://example.com/a", "https://evilwithpersona.com/a"} {
		if _, err := NewPersonaFileFetcher(nil).Fetch(context.Background(), address, 10); !errors.Is(err, ErrUntrustedFileURL) {
			t.Fatalf("%s: %v", address, err)
		}
	}
}
