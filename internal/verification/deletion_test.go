package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type fakeDeletionStore struct {
	requested  []RequestedDeletion
	requestErr error
	markErr    error
	marked     []uuid.UUID
	accountID  uuid.UUID
}

func (fake *fakeDeletionStore) RequestDeletion(_ context.Context, accountID uuid.UUID) ([]RequestedDeletion, error) {
	fake.accountID = accountID
	return fake.requested, fake.requestErr
}

func (fake *fakeDeletionStore) MarkRedacted(_ context.Context, id uuid.UUID) error {
	if fake.markErr != nil {
		return fake.markErr
	}
	fake.marked = append(fake.marked, id)
	return nil
}

func requestedItems(held ...bool) []RequestedDeletion {
	items := make([]RequestedDeletion, len(held))
	for index, isHeld := range held {
		items[index] = RequestedDeletion{ID: uuid.New(), InquiryID: "inq_" + uuid.NewString()[:8], Held: isHeld}
	}
	return items
}

func TestDeleteIDDataRedactsEverythingNotHeld(t *testing.T) {
	accountID := uuid.New()
	store := &fakeDeletionStore{requested: requestedItems(false, true, false)}
	redactor := &fakeRedactor{}
	result, err := NewDeletionService(store, redactor).DeleteIDData(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if result != (DeletionResult{Deleted: 2, Waiting: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if store.accountID != accountID {
		t.Fatalf("account = %s", store.accountID)
	}
	for _, call := range redactor.calls {
		if call == store.requested[1].InquiryID {
			t.Fatal("a held inquiry must never be sent to the provider")
		}
	}
	if len(store.marked) != 2 {
		t.Fatalf("marked = %d", len(store.marked))
	}
}

func TestDeleteIDDataWithNothingToDeleteSucceeds(t *testing.T) {
	result, err := NewDeletionService(&fakeDeletionStore{}, &fakeRedactor{}).DeleteIDData(context.Background(), uuid.New())
	if err != nil || result != (DeletionResult{}) {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestDeleteIDDataProviderFailureIsRetriedNotLost(t *testing.T) {
	items := requestedItems(false, false)
	store := &fakeDeletionStore{requested: items}
	redactor := &fakeRedactor{failFor: map[string]error{items[0].InquiryID: ErrPersonaRequest}}
	result, err := NewDeletionService(store, redactor).DeleteIDData(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if result != (DeletionResult{Deleted: 1, Retrying: 1}) {
		t.Fatalf("result = %+v", result)
	}
	if len(store.marked) != 1 || store.marked[0] != items[1].ID {
		t.Fatal("only the inquiry the provider deleted may be marked")
	}
}

func TestDeleteIDDataRecordFailureIsRetried(t *testing.T) {
	store := &fakeDeletionStore{requested: requestedItems(false), markErr: errors.New("db down")}
	result, err := NewDeletionService(store, &fakeRedactor{}).DeleteIDData(context.Background(), uuid.New())
	if err != nil || result != (DeletionResult{Retrying: 1}) {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestDeleteIDDataStoreFailureIsAnError(t *testing.T) {
	store := &fakeDeletionStore{requestErr: errors.New("db down")}
	redactor := &fakeRedactor{}
	if _, err := NewDeletionService(store, redactor).DeleteIDData(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected an error")
	}
	if len(redactor.calls) != 0 {
		t.Fatal("nothing may be sent to the provider when the request could not be recorded")
	}
}

type stubDeleter struct {
	result  DeletionResult
	err     error
	account uuid.UUID
	calls   int
}

func (stub *stubDeleter) DeleteIDData(_ context.Context, accountID uuid.UUID) (DeletionResult, error) {
	stub.calls++
	stub.account = accountID
	return stub.result, stub.err
}

func deleteRequest(accountID uuid.UUID, authenticated bool) *http.Request {
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/verification/data", nil)
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), accountID))
	}
	return request
}

func TestDeleteIDDataEndpoint(t *testing.T) {
	accountID := uuid.New()
	tests := []struct {
		name          string
		authenticated bool
		withDeleter   bool
		err           error
		want          int
	}{
		{"unauthenticated", false, true, nil, http.StatusUnauthorized},
		{"not configured", true, false, nil, http.StatusNotFound},
		{"deleted", true, true, nil, http.StatusOK},
		{"failure", true, true, errors.New("pq: secret detail"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &stubDeleter{result: DeletionResult{Deleted: 2, Waiting: 1}, err: test.err}
			handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{})
			if test.withDeleter {
				handler.WithDeletion(stub)
			}
			recorder := httptest.NewRecorder()
			handler.DeleteIDData(recorder, deleteRequest(accountID, test.authenticated))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
			if test.want == http.StatusOK {
				var body DeletionResult
				if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil || body != (DeletionResult{Deleted: 2, Waiting: 1}) {
					t.Fatalf("body = %+v err = %v", body, err)
				}
				if recorder.Header().Get("Cache-Control") != "no-store" || stub.account != accountID {
					t.Fatalf("cache = %q account = %s", recorder.Header().Get("Cache-Control"), stub.account)
				}
			}
			if !test.authenticated && stub.calls != 0 {
				t.Fatal("an unauthenticated request must never reach the service")
			}
			if test.err != nil && recorder.Body.String() != `{"error":"could not delete ID data"}`+"\n" {
				t.Fatalf("error text leaked: %s", recorder.Body.String())
			}
		})
	}
}
