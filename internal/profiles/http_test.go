package profiles

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

func (service stubService) Banner(context.Context, uuid.UUID) (Banner, error) {
	return Banner{URL: "https://view.test/a"}, service.err
}

func (service stubService) RequestBannerUpload(context.Context, uuid.UUID, string, int64) (UploadTicket, error) {
	return UploadTicket{UploadURL: "https://upload.test/a"}, service.err
}

func (service stubService) SaveBanner(context.Context, uuid.UUID, string) (Banner, error) {
	return Banner{URL: "https://view.test/a"}, service.err
}

func (service stubService) RemoveBanner(context.Context, uuid.UUID) error {
	return service.err
}

func serve(err error, authenticated bool, method, path, body string) *httptest.ResponseRecorder {
	service := stubService{err: err}
	handler := NewHTTPHandler(service, service, service, service)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /profile/banner", handler.GetBanner)
	mux.HandleFunc("POST /profile/banner/upload", handler.RequestUpload)
	mux.HandleFunc("PUT /profile/banner", handler.SaveBanner)
	mux.HandleFunc("DELETE /profile/banner", handler.RemoveBanner)
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestHandlersRequireAuthentication(t *testing.T) {
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/profile/banner", ""},
		{http.MethodPost, "/profile/banner/upload", `{"content_type":"image/jpeg","size_bytes":10}`},
		{http.MethodPut, "/profile/banner", `{"banner_key":"a"}`},
		{http.MethodDelete, "/profile/banner", ""},
	}
	for _, testCase := range cases {
		response := serve(nil, false, testCase.method, testCase.path, testCase.body)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status = %d", testCase.method, testCase.path, response.Code)
		}
	}
}

func TestHandlersSucceed(t *testing.T) {
	cases := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodGet, "/profile/banner", "", http.StatusOK},
		{http.MethodPost, "/profile/banner/upload", `{"content_type":"image/jpeg","size_bytes":10}`, http.StatusCreated},
		{http.MethodPut, "/profile/banner", `{"banner_key":"a"}`, http.StatusOK},
		{http.MethodDelete, "/profile/banner", "", http.StatusNoContent},
	}
	for _, testCase := range cases {
		response := serve(nil, true, testCase.method, testCase.path, testCase.body)
		if response.Code != testCase.status {
			t.Fatalf("%s %s: status = %d, want %d", testCase.method, testCase.path, response.Code, testCase.status)
		}
	}
}

func TestHandlersRejectMalformedBodies(t *testing.T) {
	bodies := []string{"", "{", `{"unknown":1}`, `{"banner_key":"a"}{"banner_key":"b"}`}
	for _, body := range bodies {
		response := serve(nil, true, http.MethodPut, "/profile/banner", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d", body, response.Code)
		}
	}
}

func TestServiceErrorsMapToStatusCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{ErrUploadsDisabled, http.StatusServiceUnavailable},
		{ErrBannerRejected, http.StatusBadRequest},
		{ErrInvalidBannerKey, http.StatusBadRequest},
		{ErrBannerNotUploaded, http.StatusBadRequest},
		{errors.New("database down"), http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		response := serve(testCase.err, true, http.MethodPut, "/profile/banner", `{"banner_key":"a"}`)
		if response.Code != testCase.status {
			t.Fatalf("err %v: status = %d, want %d", testCase.err, response.Code, testCase.status)
		}
		if testCase.status == http.StatusInternalServerError && bytes.Contains(response.Body.Bytes(), []byte("database")) {
			t.Fatal("internal error detail leaked")
		}
	}
}
