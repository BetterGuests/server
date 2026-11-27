package reports

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type stubDesk struct {
	err        error
	reporter   uuid.UUID
	submission Submission
	askedOpen  []bool
}

func (stub *stubDesk) File(_ context.Context, reporterID uuid.UUID, submission Submission) (Receipt, error) {
	stub.reporter, stub.submission = reporterID, submission
	return Receipt{ID: uuid.New()}, stub.err
}

func (stub *stubDesk) List(_ context.Context, _ uuid.UUID, open bool) ([]Report, error) {
	stub.askedOpen = append(stub.askedOpen, open)
	return []Report{}, stub.err
}

func (stub *stubDesk) Resolve(context.Context, uuid.UUID, uuid.UUID, Decision) error {
	return stub.err
}

func call(handler http.HandlerFunc, accountID uuid.UUID, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetPathValue("report_id", uuid.NewString())
	if accountID != uuid.Nil {
		request = request.WithContext(accounts.WithUserID(request.Context(), accountID))
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func TestFilingTakesTheReporterFromTheSessionOnly(t *testing.T) {
	stub := &stubDesk{}
	handler := NewHTTPHandler(stub, stub)
	reporter, listing := uuid.New(), uuid.New()
	body := `{"target":"listing","reason":"scam","details":"Fake photos.","listing_id":"` + listing.String() + `"}`

	if got := call(handler.File, uuid.Nil, http.MethodPost, "/api/v1/reports", body); got.Code != http.StatusUnauthorized {
		t.Fatalf("signed out = %d", got.Code)
	}
	response := call(handler.File, reporter, http.MethodPost, "/api/v1/reports", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("filed = %d %s", response.Code, response.Body)
	}
	if stub.reporter != reporter || stub.submission.ListingID != listing || stub.submission.Reason != ReasonScam {
		t.Fatalf("submission = %v %+v", stub.reporter, stub.submission)
	}
	spoofed := `{"target":"listing","reason":"scam","details":"x","reporter_id":"` + uuid.NewString() + `"}`
	if got := call(handler.File, reporter, http.MethodPost, "/api/v1/reports", spoofed); got.Code != http.StatusBadRequest {
		t.Fatalf("a body naming the reporter = %d", got.Code)
	}
}

func TestReportErrorsMapToStatusCodes(t *testing.T) {
	cases := map[error]int{
		ErrInvalidReport:            http.StatusBadRequest,
		ErrInvalidDetails:           http.StatusBadRequest,
		ErrOwnReport:                http.StatusBadRequest,
		ErrTargetNotFound:           http.StatusNotFound,
		ErrAlreadyReported:          http.StatusConflict,
		errors.New("database down"): http.StatusInternalServerError,
	}
	for failure, status := range cases {
		stub := &stubDesk{err: failure}
		response := call(NewHTTPHandler(stub, stub).File, uuid.New(), http.MethodPost, "/api/v1/reports", `{}`)
		if response.Code != status {
			t.Errorf("%v = %d, want %d", failure, response.Code, status)
		}
		if status == http.StatusInternalServerError && strings.Contains(response.Body.String(), "database") {
			t.Errorf("leaked internal detail: %s", response.Body)
		}
	}
}

func TestTheTeamListsOpenOrClosedReportsAndOthersSeeNothing(t *testing.T) {
	stub := &stubDesk{}
	handler := NewHTTPHandler(stub, stub)
	for _, target := range []string{"/api/v1/admin/user-reports", "/api/v1/admin/user-reports?status=closed"} {
		if got := call(handler.List, uuid.New(), http.MethodGet, target, ""); got.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, got.Code)
		}
	}
	if len(stub.askedOpen) != 2 || !stub.askedOpen[0] || stub.askedOpen[1] {
		t.Fatalf("asked open = %v", stub.askedOpen)
	}
	if got := call(handler.List, uuid.New(), http.MethodGet, "/api/v1/admin/user-reports?status=all", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d", got.Code)
	}
	stub.err = ErrNotAdmin
	if got := call(handler.List, uuid.New(), http.MethodGet, "/api/v1/admin/user-reports", ""); got.Code != http.StatusNotFound {
		t.Fatalf("not an admin = %d", got.Code)
	}
}

func TestClosingAReportReturnsNoContent(t *testing.T) {
	stub := &stubDesk{}
	handler := NewHTTPHandler(stub, stub)
	response := call(handler.Resolve, uuid.New(), http.MethodPost, "/", `{"outcome":"dismissed","note":""}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("resolve = %d %s", response.Code, response.Body)
	}
	stub.err = ErrReportNotFound
	if got := call(handler.Resolve, uuid.New(), http.MethodPost, "/", `{"outcome":"dismissed"}`); got.Code != http.StatusNotFound {
		t.Fatalf("already closed = %d", got.Code)
	}
}
