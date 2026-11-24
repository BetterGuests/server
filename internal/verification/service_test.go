package verification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testSecret = "whsec_sandbox_test"

var testNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

type fakeInquiries struct {
	inquiry Inquiry
	err     error
	calls   []string
}

func (fake *fakeInquiries) CreateInquiry(_ context.Context, templateID, referenceID string) (Inquiry, error) {
	fake.calls = append(fake.calls, templateID+"|"+referenceID)
	return fake.inquiry, fake.err
}

func (fake *fakeInquiries) CreateHandoffInquiry(_ context.Context, templateID, referenceID string) (Inquiry, error) {
	fake.calls = append(fake.calls, "handoff|"+templateID+"|"+referenceID)
	return fake.inquiry, fake.err
}

type fakeStore struct {
	rows      map[string]*Verification
	completed bool
	createErr error
	applied   int
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]*Verification{}} }

func (store *fakeStore) HasCompleted(context.Context, uuid.UUID) (bool, error) {
	return store.completed, nil
}

func (store *fakeStore) Create(_ context.Context, verification Verification) error {
	if store.createErr != nil {
		return store.createErr
	}
	copied := verification
	store.rows[verification.PersonaInquiryID] = &copied
	return nil
}

func (store *fakeStore) ApplyStatus(_ context.Context, accountID uuid.UUID, inquiryID string, status Status, occurredAt time.Time) error {
	row, ok := store.rows[inquiryID]
	if !ok || row.AccountID != accountID {
		return ErrInquiryNotFound
	}
	if !row.LastEventAt.IsZero() && row.LastEventAt.After(occurredAt) {
		return nil
	}
	row.Status = status
	row.LastEventAt = occurredAt
	store.applied++
	return nil
}

func newTestService(store *fakeStore, inquiries *fakeInquiries) *VerificationService {
	service := NewVerificationService(store, inquiries, Config{TemplateID: "itmpl_sandbox", WebhookSecret: testSecret})
	service.now = func() time.Time { return testNow }
	return service
}

func signedHeader(secret string, signedAt time.Time, body []byte) string {
	timestamp := strconv.FormatInt(signedAt.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return fmt.Sprintf("t=%s,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func eventBody(name, inquiryID, referenceID string, createdAt time.Time) []byte {
	return []byte(fmt.Sprintf(`{"data":{"type":"event","id":"evt_1","attributes":{"name":%q,"created-at":%q,"payload":{"data":{"type":"inquiry","id":%q,"attributes":{"status":"x","reference-id":%q}}}}}}`,
		name, createdAt.Format(time.RFC3339), inquiryID, referenceID))
}

func seededService(t *testing.T) (*VerificationService, *fakeStore, uuid.UUID) {
	t.Helper()
	store := newFakeStore()
	accountID := uuid.New()
	store.rows["inq_1"] = &Verification{AccountID: accountID, PersonaInquiryID: "inq_1", Status: StatusCreated}
	return newTestService(store, &fakeInquiries{}), store, accountID
}

func TestStartVerificationCreatesInquiryAndRecordsIt(t *testing.T) {
	store := newFakeStore()
	inquiries := &fakeInquiries{inquiry: Inquiry{ID: "inq_1", SessionToken: "tok_1"}}
	service := newTestService(store, inquiries)
	accountID := uuid.New()

	result, err := service.StartVerification(context.Background(), accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.InquiryID != "inq_1" || result.SessionToken != "tok_1" || result.Status != StatusCreated {
		t.Fatalf("unexpected result: %+v", result)
	}
	if want := "itmpl_sandbox|" + accountID.String(); len(inquiries.calls) != 1 || inquiries.calls[0] != want {
		t.Fatalf("persona called with %v, want %s", inquiries.calls, want)
	}
	row := store.rows["inq_1"]
	if row == nil || row.AccountID != accountID || row.Status != StatusCreated {
		t.Fatalf("verification not recorded: %+v", row)
	}
}

func TestStartVerificationRejectsAlreadyVerifiedWithoutCallingPersona(t *testing.T) {
	store := newFakeStore()
	store.completed = true
	inquiries := &fakeInquiries{}
	service := newTestService(store, inquiries)

	if _, err := service.StartVerification(context.Background(), uuid.New(), false); !errors.Is(err, ErrAlreadyVerified) {
		t.Fatalf("expected ErrAlreadyVerified, got %v", err)
	}
	if len(inquiries.calls) != 0 {
		t.Fatal("persona must not be called for a verified account")
	}
}

func TestStartVerificationSurfacesPersonaAndStoreFailures(t *testing.T) {
	personaFailure := errors.New("persona down")
	storeFailure := errors.New("db down")
	tests := []struct {
		name      string
		inquiries *fakeInquiries
		store     *fakeStore
		want      error
	}{
		{"persona error", &fakeInquiries{err: personaFailure}, newFakeStore(), personaFailure},
		{"store error", &fakeInquiries{inquiry: Inquiry{ID: "inq_1", SessionToken: "t"}}, &fakeStore{rows: map[string]*Verification{}, createErr: storeFailure}, storeFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newTestService(test.store, test.inquiries)
			if _, err := service.StartVerification(context.Background(), uuid.New(), false); !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestWebhookMapsEveryEventToStatus(t *testing.T) {
	tests := []struct {
		event string
		want  Status
	}{
		{EventCompleted, StatusCompleted},
		{EventFailed, StatusFailed},
		{EventDeclined, StatusDeclined},
		{EventExpired, StatusExpired},
		{EventMarkedForReview, StatusNeedsReview},
		{EventApproved, StatusCompleted},
	}
	for _, test := range tests {
		t.Run(test.event, func(t *testing.T) {
			service, store, accountID := seededService(t)
			body := eventBody(test.event, "inq_1", accountID.String(), testNow)
			if err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, body), body); err != nil {
				t.Fatal(err)
			}
			if got := store.rows["inq_1"].Status; got != test.want {
				t.Fatalf("status = %s, want %s", got, test.want)
			}
		})
	}
}

func TestWebhookRejectsBadSignatures(t *testing.T) {
	service, store, accountID := seededService(t)
	body := eventBody(EventCompleted, "inq_1", accountID.String(), testNow)
	valid := signedHeader(testSecret, testNow, body)
	tests := []struct {
		name   string
		header string
		body   []byte
	}{
		{"missing header", "", body},
		{"wrong secret", signedHeader("other-secret", testNow, body), body},
		{"tampered body", valid, eventBody(EventDeclined, "inq_1", accountID.String(), testNow)},
		{"no signature part", "t=" + strconv.FormatInt(testNow.Unix(), 10), body},
		{"no timestamp part", "v1=abcd", body},
		{"non-hex signature", "t=" + strconv.FormatInt(testNow.Unix(), 10) + ",v1=zzzz", body},
		{"garbage timestamp", "t=abc,v1=abcd", body},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := service.HandleWebhook(context.Background(), test.header, test.body); !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("expected ErrInvalidSignature, got %v", err)
			}
		})
	}
	if store.rows["inq_1"].Status != StatusCreated {
		t.Fatal("rejected webhook must not change status")
	}
}

func TestWebhookRejectsReplayedOldTimestamp(t *testing.T) {
	service, store, accountID := seededService(t)
	body := eventBody(EventCompleted, "inq_1", accountID.String(), testNow)

	for _, offset := range []time.Duration{-(SignatureTolerance + time.Second), SignatureTolerance + time.Second} {
		header := signedHeader(testSecret, testNow.Add(offset), body)
		if err := service.HandleWebhook(context.Background(), header, body); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("offset %s: expected ErrInvalidSignature, got %v", offset, err)
		}
	}
	if store.rows["inq_1"].Status != StatusCreated {
		t.Fatal("replayed webhook must not change status")
	}

	edge := signedHeader(testSecret, testNow.Add(-SignatureTolerance), body)
	if err := service.HandleWebhook(context.Background(), edge, body); err != nil {
		t.Fatalf("timestamp at tolerance boundary should pass: %v", err)
	}
}

func TestWebhookAcceptsRotatedSecondSignature(t *testing.T) {
	service, store, accountID := seededService(t)
	body := eventBody(EventCompleted, "inq_1", accountID.String(), testNow)
	valid := signedHeader(testSecret, testNow, body)
	timestamp, signatures := parseSignatureHeader(valid)
	header := "t=" + timestamp + ",v1=deadbeef v1=" + signatures[0]

	if err := service.HandleWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	if store.rows["inq_1"].Status != StatusCompleted {
		t.Fatal("expected completed status")
	}
}

func TestWebhookDuplicateDeliveryIsIdempotent(t *testing.T) {
	service, store, accountID := seededService(t)
	body := eventBody(EventCompleted, "inq_1", accountID.String(), testNow)
	header := signedHeader(testSecret, testNow, body)

	for range 3 {
		if err := service.HandleWebhook(context.Background(), header, body); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.rows["inq_1"].Status; got != StatusCompleted {
		t.Fatalf("status = %s", got)
	}
}

func TestWebhookOutOfOrderOlderEventDoesNotOverwriteNewer(t *testing.T) {
	service, store, accountID := seededService(t)
	newer := eventBody(EventDeclined, "inq_1", accountID.String(), testNow)
	older := eventBody(EventMarkedForReview, "inq_1", accountID.String(), testNow.Add(-time.Minute))

	if err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, newer), newer); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, older), older); err != nil {
		t.Fatal(err)
	}
	if got := store.rows["inq_1"].Status; got != StatusDeclined {
		t.Fatalf("older event overwrote newer: status = %s", got)
	}
}

func TestWebhookUnknownInquiry(t *testing.T) {
	service, _, accountID := seededService(t)
	tests := []struct {
		name        string
		inquiryID   string
		referenceID string
	}{
		{"unknown inquiry id", "inq_missing", accountID.String()},
		{"inquiry belongs to another account", "inq_1", uuid.NewString()},
		{"reference id is not an account id", "inq_1", "not-a-uuid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := eventBody(EventCompleted, test.inquiryID, test.referenceID, testNow)
			err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, body), body)
			if !errors.Is(err, ErrInquiryNotFound) {
				t.Fatalf("expected ErrInquiryNotFound, got %v", err)
			}
		})
	}
}

func TestWebhookIgnoresUnmappedEventsAndRejectsMalformedPayloads(t *testing.T) {
	service, store, accountID := seededService(t)

	created := eventBody("inquiry.created", "inq_1", accountID.String(), testNow)
	if err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, created), created); err != nil {
		t.Fatalf("unmapped event should be ignored: %v", err)
	}
	if store.applied != 0 {
		t.Fatal("unmapped event must not touch the store")
	}

	for name, body := range map[string][]byte{
		"not json":        []byte("nope"),
		"missing event":   []byte(`{"data":{"attributes":{}}}`),
		"missing inquiry": []byte(`{"data":{"attributes":{"name":"inquiry.completed"}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, body), body)
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("expected ErrInvalidPayload, got %v", err)
			}
		})
	}
}

func TestConfigFromLookupRequiresPersonaSettings(t *testing.T) {
	full := map[string]string{"PERSONA_API_KEY": "persona_sandbox_k", "PERSONA_TEMPLATE_ID": "itmpl_x", "PERSONA_WEBHOOK_SECRET": "s"}
	config, err := configFromLookup(func(key string) string { return full[key] })
	if err != nil || config.BaseURL != DefaultPersonaBaseURL {
		t.Fatalf("config = %+v, err = %v", config, err)
	}
	for key := range full {
		partial := map[string]string{}
		for k, v := range full {
			if k != key {
				partial[k] = v
			}
		}
		if _, err := configFromLookup(func(name string) string { return partial[name] }); err == nil {
			t.Fatalf("expected error when %s is missing", key)
		}
	}
}

type fakeConsents struct{ granted bool }

func (consents fakeConsents) HasConsent(context.Context, uuid.UUID, string) (bool, error) {
	return consents.granted, nil
}

func TestStartVerificationWithoutBiometricConsentNeverCallsPersona(t *testing.T) {
	inquiries := &fakeInquiries{}
	service := newTestService(newFakeStore(), inquiries).WithConsents(fakeConsents{granted: false})

	if _, err := service.StartVerification(context.Background(), uuid.New(), false); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("expected ErrConsentRequired, got %v", err)
	}
	if len(inquiries.calls) != 0 {
		t.Fatal("persona must not be called before consent")
	}
}

func TestStartVerificationWithBiometricConsentProceeds(t *testing.T) {
	inquiries := &fakeInquiries{inquiry: Inquiry{ID: "inq_1", SessionToken: "tok_1"}}
	service := newTestService(newFakeStore(), inquiries).WithConsents(fakeConsents{granted: true})

	if _, err := service.StartVerification(context.Background(), uuid.New(), false); err != nil {
		t.Fatal(err)
	}
}

func TestStartVerificationReplaceAllowsVerifiedAccountToStartAgain(t *testing.T) {
	store := newFakeStore()
	store.completed = true
	inquiries := &fakeInquiries{inquiry: Inquiry{ID: "inq_2", SessionToken: "tok_2"}}
	service := newTestService(store, inquiries)

	result, err := service.StartVerification(context.Background(), uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.InquiryID != "inq_2" || len(inquiries.calls) != 1 {
		t.Fatalf("unexpected result %+v calls %v", result, inquiries.calls)
	}
}

func TestStartHandoffReturnsOneTimeLinkAndRecordsInquiry(t *testing.T) {
	inquiries := &fakeInquiries{inquiry: Inquiry{ID: "inq_9", OneTimeLink: "https://inquiry.withpersona.com/verify?code=abc"}}
	store := newFakeStore()
	service := NewVerificationService(store, inquiries, Config{TemplateID: "itmpl_1"})
	accountID := uuid.New()
	result, err := service.StartHandoff(context.Background(), accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.OneTimeLink != "https://inquiry.withpersona.com/verify?code=abc" || result.InquiryID != "inq_9" || result.SessionToken != "" {
		t.Fatalf("result = %+v", result)
	}
	if len(inquiries.calls) != 1 || inquiries.calls[0] != "handoff|itmpl_1|"+accountID.String() {
		t.Fatalf("calls = %v", inquiries.calls)
	}
	if len(store.rows) != 1 {
		t.Fatalf("recorded rows = %d", len(store.rows))
	}
}

type plainInquiries struct{}

func (plainInquiries) CreateInquiry(context.Context, string, string) (Inquiry, error) {
	return Inquiry{}, nil
}

func TestStartHandoffWithoutHandoffCreatorIsUnavailable(t *testing.T) {
	service := NewVerificationService(newFakeStore(), plainInquiries{}, Config{})
	if _, err := service.StartHandoff(context.Background(), uuid.New(), false); !errors.Is(err, ErrHandoffUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
