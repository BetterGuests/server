package cashback

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type stubService struct{ err error }

func (service stubService) HostTrip(context.Context, uuid.UUID, uuid.UUID) (HostTripView, error) {
	return HostTripView{}, service.err
}

func (service stubService) PendingForHost(context.Context, uuid.UUID) ([]PendingSubmission, error) {
	return nil, service.err
}

func (service stubService) Trip(context.Context, uuid.UUID, uuid.UUID) (TripView, error) {
	return TripView{}, service.err
}

func (service stubService) SubmitPhoto(context.Context, uuid.UUID, uuid.UUID, string, string) (Submission, error) {
	return Submission{}, service.err
}

func (service stubService) RequestUpload(context.Context, uuid.UUID, uuid.UUID, string, string, int64) (UploadTicket, error) {
	return UploadTicket{}, service.err
}

func (service stubService) Review(context.Context, uuid.UUID, uuid.UUID, Decision, string) (Submission, *Refund, error) {
	return Submission{}, nil, service.err
}

func serve(t *testing.T, err error, authenticated bool, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	service := stubService{err: err}
	handler := NewHTTPHandler(service, service, service, service, service)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bookings/{booking_id}/cashback", handler.GetTrip)
	mux.HandleFunc("POST /bookings/{booking_id}/cashback/tasks/{task_key}/photo-upload", handler.RequestUpload)
	mux.HandleFunc("POST /bookings/{booking_id}/cashback/tasks/{task_key}/photo", handler.SubmitPhoto)
	mux.HandleFunc("GET /hosting/bookings/{booking_id}/cashback", handler.GetHostTrip)
	mux.HandleFunc("GET /hosting/cashback/pending", handler.ListPending)
	mux.HandleFunc("POST /submissions/{submission_id}/review", handler.ReviewSubmission)
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestSubmitPhotoStatuses(t *testing.T) {
	path := "/bookings/" + uuid.NewString() + "/cashback/tasks/run_dishwasher/photo"
	body := `{"photo_key":"cashback/x/run_dishwasher/a.jpg"}`
	cases := []struct {
		name string
		err  error
		auth bool
		body string
		want int
	}{
		{"created", nil, true, body, http.StatusCreated},
		{"unauthenticated", nil, false, body, http.StatusUnauthorized},
		{"unknown field", nil, true, `{"photo_key":"k","status":"approved"}`, http.StatusBadRequest},
		{"not verified", ErrNotVerified, true, body, http.StatusForbidden},
		{"not earning", ErrBookingNotEarning, true, body, http.StatusConflict},
		{"locked", ErrSubmissionLocked, true, body, http.StatusConflict},
		{"bad task", ErrUnknownTask, true, body, http.StatusBadRequest},
		{"bad key", ErrInvalidPhotoKey, true, body, http.StatusBadRequest},
		{"not uploaded", ErrPhotoNotUploaded, true, body, http.StatusBadRequest},
		{"rejected file", ErrPhotoRejected, true, body, http.StatusBadRequest},
		{"uploads off", ErrUploadsDisabled, true, body, http.StatusServiceUnavailable},
		{"missing booking", ErrBookingNotFound, true, body, http.StatusNotFound},
		{"internal", errors.New("boom"), true, body, http.StatusInternalServerError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := serve(t, test.err, test.auth, http.MethodPost, path, test.body).Code; got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func TestReviewStatuses(t *testing.T) {
	path := "/submissions/" + uuid.NewString() + "/review"
	body := `{"decision":"approve"}`
	cases := []struct {
		name string
		err  error
		auth bool
		want int
	}{
		{"ok", nil, true, http.StatusOK},
		{"unauthenticated", nil, false, http.StatusUnauthorized},
		{"not host", ErrNotHost, true, http.StatusForbidden},
		{"already reviewed", ErrNotAwaitingReview, true, http.StatusConflict},
		{"missing", ErrSubmissionNotFound, true, http.StatusNotFound},
		{"retake needs note", ErrRetakeNeedsNote, true, http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := serve(t, test.err, test.auth, http.MethodPost, path, body).Code; got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func TestInvalidPathIDIsBadRequest(t *testing.T) {
	if got := serve(t, nil, true, http.MethodGet, "/bookings/nope/cashback", "").Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d", got)
	}
}

func TestErrorsNeverLeakInternalDetail(t *testing.T) {
	response := serve(t, errors.New("pq: secret detail"), true, http.MethodGet, "/bookings/"+uuid.NewString()+"/cashback", "")
	if bytes.Contains(response.Body.Bytes(), []byte("secret detail")) {
		t.Fatalf("leaked: %s", response.Body)
	}
}

func TestHostEndpointsRequireAuthenticationAndMapErrors(t *testing.T) {
	tripPath := "/hosting/bookings/" + uuid.NewString() + "/cashback"
	if got := serve(t, nil, false, http.MethodGet, tripPath, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated trip = %d", got)
	}
	if got := serve(t, nil, true, http.MethodGet, tripPath, "").Code; got != http.StatusOK {
		t.Fatalf("host trip = %d", got)
	}
	if got := serve(t, ErrBookingNotFound, true, http.MethodGet, tripPath, "").Code; got != http.StatusNotFound {
		t.Fatalf("missing trip = %d", got)
	}
	if got := serve(t, nil, false, http.MethodGet, "/hosting/cashback/pending", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pending = %d", got)
	}
	response := serve(t, nil, true, http.MethodGet, "/hosting/cashback/pending", "")
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"submissions"`)) {
		t.Fatalf("pending = %d %s", response.Code, response.Body)
	}
}

func TestRequestUploadStatuses(t *testing.T) {
	path := "/bookings/" + uuid.NewString() + "/cashback/tasks/run_dishwasher/photo-upload"
	body := `{"content_type":"image/jpeg","size_bytes":2048}`
	cases := []struct {
		name string
		err  error
		auth bool
		body string
		want int
	}{
		{"created", nil, true, body, http.StatusCreated},
		{"unauthenticated", nil, false, body, http.StatusUnauthorized},
		{"unknown field", nil, true, `{"content_type":"image/jpeg","size_bytes":1,"key":"x"}`, http.StatusBadRequest},
		{"rejected", ErrPhotoRejected, true, body, http.StatusBadRequest},
		{"not verified", ErrNotVerified, true, body, http.StatusForbidden},
		{"not earning", ErrBookingNotEarning, true, body, http.StatusConflict},
		{"uploads off", ErrUploadsDisabled, true, body, http.StatusServiceUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := serve(t, test.err, test.auth, http.MethodPost, path, test.body).Code; got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}
