package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type stubStarter struct {
	result CheckoutResult
	err    error
}

func (starter stubStarter) StartCheckout(context.Context, uuid.UUID, uuid.UUID) (CheckoutResult, error) {
	return starter.result, starter.err
}

type stubProcessor struct{ err error }

func (processor stubProcessor) HandleWebhook(context.Context, string, []byte) error {
	return processor.err
}

func checkoutRequest(t *testing.T, body string, authenticated bool) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/payments/checkout", bytes.NewBufferString(body))
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	}
	return request
}

func TestStartCheckoutHTTPStatuses(t *testing.T) {
	validBody := `{"booking_id":"` + uuid.NewString() + `"}`
	cases := []struct {
		name          string
		body          string
		authenticated bool
		err           error
		want          int
	}{
		{"created", validBody, true, nil, http.StatusCreated},
		{"unauthenticated", validBody, false, nil, http.StatusUnauthorized},
		{"missing booking id", `{}`, true, nil, http.StatusBadRequest},
		{"unknown field", `{"booking_id":"` + uuid.NewString() + `","amount":1}`, true, nil, http.StatusBadRequest},
		{"booking not found", validBody, true, ErrBookingNotFound, http.StatusNotFound},
		{"not payable", validBody, true, ErrBookingNotPayable, http.StatusConflict},
		{"already paid", validBody, true, ErrAlreadyPaid, http.StatusConflict},
		{"unpriced", validBody, true, ErrListingUnpriced, http.StatusUnprocessableEntity},
		{"internal", validBody, true, errors.New("boom"), http.StatusInternalServerError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(stubStarter{err: test.err}, stubProcessor{})
			response := httptest.NewRecorder()
			handler.StartCheckout(response, checkoutRequest(t, test.body, test.authenticated))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.want, response.Body)
			}
		})
	}
}

func TestStartCheckoutNeverLeaksInternalErrors(t *testing.T) {
	handler := NewHTTPHandler(stubStarter{err: errors.New("pq: secret detail")}, stubProcessor{})
	response := httptest.NewRecorder()
	handler.StartCheckout(response, checkoutRequest(t, `{"booking_id":"`+uuid.NewString()+`"}`, true))
	if bytes.Contains(response.Body.Bytes(), []byte("secret detail")) {
		t.Fatalf("body leaked: %s", response.Body)
	}
}

func TestStartCheckoutReturnsClientSecretWithoutCaching(t *testing.T) {
	handler := NewHTTPHandler(stubStarter{result: CheckoutResult{ClientSecret: "cs", AmountCents: 100}}, stubProcessor{})
	response := httptest.NewRecorder()
	handler.StartCheckout(response, checkoutRequest(t, `{"booking_id":"`+uuid.NewString()+`"}`, true))
	var decoded CheckoutResult
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded.ClientSecret != "cs" {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func TestStripeWebhookHTTPStatuses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"ok", nil, http.StatusOK},
		{"bad signature", ErrInvalidSignature, http.StatusUnauthorized},
		{"bad payload", ErrInvalidPayload, http.StatusBadRequest},
		{"unknown payment", ErrPaymentNotFound, http.StatusNotFound},
		{"internal", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(stubStarter{}, stubProcessor{err: test.err})
			response := httptest.NewRecorder()
			handler.StripeWebhook(response, httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/stripe", bytes.NewBufferString("{}")))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}
