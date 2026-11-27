package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type lookupInquiries struct {
	fakeInquiries
	snapshot  InquirySnapshot
	lookupErr error
	resumed   Inquiry
	resumeErr error
	lookups   int
	resumes   []string
}

func (fake *lookupInquiries) GetInquiry(_ context.Context, inquiryID string) (InquirySnapshot, error) {
	fake.lookups++
	snapshot := fake.snapshot
	snapshot.ID = inquiryID
	return snapshot, fake.lookupErr
}

func (fake *lookupInquiries) ResumeInquiry(_ context.Context, inquiryID string) (Inquiry, error) {
	fake.resumes = append(fake.resumes, inquiryID)
	return fake.resumed, fake.resumeErr
}

type openStore struct {
	*fakeStore
	openID string
	since  time.Time
}

func (store *openStore) LatestOpenInquiry(_ context.Context, _ uuid.UUID, since time.Time) (string, bool, error) {
	store.since = since
	return store.openID, store.openID != "", nil
}

func passedIDObject(t *testing.T) includedObject {
	t.Helper()
	attributes, err := json.Marshal(map[string]string{"status": "passed", "name-first": "Ada", "name-last": "Lovelace", "birthdate": "1990-12-10"})
	if err != nil {
		t.Fatal(err)
	}
	return includedObject{Type: governmentIDType, Attributes: attributes}
}

func lookupService(store Store, inquiries InquiryCreator) *VerificationService {
	service := NewVerificationService(store, inquiries, Config{TemplateID: "itmpl_sandbox", WebhookSecret: testSecret})
	service.now = func() time.Time { return testNow }
	return service
}

func TestReconcileAppliesACompletionTheWebhookNeverDelivered(t *testing.T) {
	_, store, accountID := seededService(t)
	inquiries := &lookupInquiries{snapshot: InquirySnapshot{
		Status: "approved", ReferenceID: accountID.String(), ChangedAt: testNow.Add(-time.Minute),
		Included: []includedObject{passedIDObject(t)},
	}}
	service := lookupService(store, inquiries)
	if err := service.Reconcile(context.Background(), accountID, "inq_1"); err != nil {
		t.Fatal(err)
	}
	if store.rows["inq_1"].Status != StatusCompleted || len(store.completions) != 1 {
		t.Fatalf("row = %+v completions = %+v", store.rows["inq_1"], store.completions)
	}
	if store.completions[0].Name.Last != "Lovelace" || !store.completions[0].OccurredAt.Equal(testNow.Add(-time.Minute)) {
		t.Fatalf("completion = %+v", store.completions[0])
	}
}

func TestReconcileAppliesFailuresAndReviews(t *testing.T) {
	for personaStatus, want := range map[string]Status{"failed": StatusFailed, "declined": StatusDeclined, "expired": StatusExpired, "needs_review": StatusNeedsReview} {
		t.Run(personaStatus, func(t *testing.T) {
			_, store, accountID := seededService(t)
			inquiries := &lookupInquiries{snapshot: InquirySnapshot{Status: personaStatus, ReferenceID: accountID.String()}}
			if err := lookupService(store, inquiries).Reconcile(context.Background(), accountID, "inq_1"); err != nil {
				t.Fatal(err)
			}
			if store.rows["inq_1"].Status != want || !store.rows["inq_1"].LastEventAt.Equal(testNow) {
				t.Fatalf("row = %+v", store.rows["inq_1"])
			}
		})
	}
}

func TestReconcileLeavesAnOpenInquiryAlone(t *testing.T) {
	_, store, accountID := seededService(t)
	inquiries := &lookupInquiries{snapshot: InquirySnapshot{Status: "pending", ReferenceID: accountID.String()}}
	if err := lookupService(store, inquiries).Reconcile(context.Background(), accountID, "inq_1"); err != nil {
		t.Fatal(err)
	}
	if store.applied != 0 || store.rows["inq_1"].Status != StatusCreated {
		t.Fatalf("applied = %d row = %+v", store.applied, store.rows["inq_1"])
	}
}

func TestReconcileRefusesAnInquiryForAnotherAccount(t *testing.T) {
	_, store, accountID := seededService(t)
	inquiries := &lookupInquiries{snapshot: InquirySnapshot{Status: "approved", ReferenceID: uuid.NewString()}}
	err := lookupService(store, inquiries).Reconcile(context.Background(), accountID, "inq_1")
	if !errors.Is(err, ErrInquiryNotFound) || store.applied != 0 {
		t.Fatalf("err = %v applied = %d", err, store.applied)
	}
}

func TestReconcileAsksPersonaAtMostOncePerInterval(t *testing.T) {
	_, store, accountID := seededService(t)
	inquiries := &lookupInquiries{snapshot: InquirySnapshot{Status: "pending", ReferenceID: accountID.String()}}
	service := lookupService(store, inquiries)
	now := testNow
	service.now = func() time.Time { return now }
	for range 3 {
		_ = service.Reconcile(context.Background(), accountID, "inq_1")
	}
	now = now.Add(LookupInterval)
	_ = service.Reconcile(context.Background(), accountID, "inq_1")
	if inquiries.lookups != 2 {
		t.Fatalf("lookups = %d, want 2", inquiries.lookups)
	}
}

func TestStartResumesTheOpenInquiryInsteadOfCreatingAnother(t *testing.T) {
	store := &openStore{fakeStore: newFakeStore(), openID: "inq_open"}
	accountID := uuid.New()
	inquiries := &lookupInquiries{
		snapshot: InquirySnapshot{Status: "pending", ReferenceID: accountID.String()},
		resumed:  Inquiry{ID: "inq_open", SessionToken: "sess_resumed"},
	}
	result, err := lookupService(store, inquiries).StartVerification(context.Background(), accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.InquiryID != "inq_open" || result.SessionToken != "sess_resumed" || len(inquiries.calls) != 0 {
		t.Fatalf("result = %+v creates = %v", result, inquiries.calls)
	}
	if !store.since.Equal(testNow.Add(-OpenInquiryReuse)) {
		t.Fatalf("since = %v", store.since)
	}
}

func TestStartCreatesANewInquiryWhenResumeFails(t *testing.T) {
	store := &openStore{fakeStore: newFakeStore(), openID: "inq_open"}
	accountID := uuid.New()
	inquiries := &lookupInquiries{
		fakeInquiries: fakeInquiries{inquiry: Inquiry{ID: "inq_new", SessionToken: "sess_new"}},
		snapshot:      InquirySnapshot{Status: "created", ReferenceID: accountID.String()},
		resumeErr:     ErrPersonaRequest,
	}
	result, err := lookupService(store, inquiries).StartVerification(context.Background(), accountID, false)
	if err != nil || result.InquiryID != "inq_new" || len(inquiries.calls) != 1 {
		t.Fatalf("result = %+v err = %v creates = %v", result, err, inquiries.calls)
	}
}

func TestStartRecordsAFinishedOpenInquiryBeforeCreatingANewOne(t *testing.T) {
	store := &openStore{fakeStore: newFakeStore(), openID: "inq_open"}
	accountID := uuid.New()
	store.rows["inq_open"] = &Verification{AccountID: accountID, PersonaInquiryID: "inq_open", Status: StatusCreated}
	inquiries := &lookupInquiries{
		fakeInquiries: fakeInquiries{inquiry: Inquiry{ID: "inq_new", SessionToken: "sess_new"}},
		snapshot:      InquirySnapshot{Status: "failed", ReferenceID: accountID.String()},
	}
	result, err := lookupService(store, inquiries).StartVerification(context.Background(), accountID, false)
	if err != nil || result.InquiryID != "inq_new" || len(inquiries.resumes) != 0 {
		t.Fatalf("result = %+v err = %v resumes = %v", result, err, inquiries.resumes)
	}
	if store.rows["inq_open"].Status != StatusFailed {
		t.Fatalf("open row = %+v", store.rows["inq_open"])
	}
}

func TestStartDoesNotResumeWhenAlreadyVerified(t *testing.T) {
	store := &openStore{fakeStore: newFakeStore(), openID: "inq_open"}
	store.completed = true
	accountID := uuid.New()
	inquiries := &lookupInquiries{snapshot: InquirySnapshot{Status: "pending", ReferenceID: accountID.String()}}
	_, err := lookupService(store, inquiries).StartVerification(context.Background(), accountID, false)
	if !errors.Is(err, ErrAlreadyVerified) || len(inquiries.resumes) != 0 {
		t.Fatalf("err = %v resumes = %v", err, inquiries.resumes)
	}
}

func TestHandoffAlwaysCreatesAFreshOneTimeLink(t *testing.T) {
	store := &openStore{fakeStore: newFakeStore(), openID: "inq_open"}
	accountID := uuid.New()
	inquiries := &lookupInquiries{
		fakeInquiries: fakeInquiries{inquiry: Inquiry{ID: "inq_new", OneTimeLink: "https://link"}},
		snapshot:      InquirySnapshot{Status: "pending", ReferenceID: accountID.String()},
	}
	result, err := lookupService(store, inquiries).StartHandoff(context.Background(), accountID, true)
	if err != nil || result.OneTimeLink != "https://link" || inquiries.lookups != 0 || len(inquiries.resumes) != 0 {
		t.Fatalf("result = %+v err = %v lookups = %d", result, err, inquiries.lookups)
	}
}

type countingReconciler struct {
	calls   []string
	err     error
	onCall  func()
	account uuid.UUID
}

func (stub *countingReconciler) Reconcile(_ context.Context, accountID uuid.UUID, inquiryID string) error {
	stub.calls = append(stub.calls, inquiryID)
	stub.account = accountID
	if stub.onCall != nil {
		stub.onCall()
	}
	return stub.err
}

type mutableStatus struct{ attempts []Attempt }

func (stub *mutableStatus) Attempts(context.Context, uuid.UUID) ([]Attempt, error) {
	return stub.attempts, nil
}

func TestStatusEndpointAsksPersonaWhenTheWebhookIsLate(t *testing.T) {
	status := &mutableStatus{attempts: []Attempt{{InquiryID: "inq_1", Status: StatusCreated, CompletedAt: time.Now()}}}
	reconciler := &countingReconciler{onCall: func() {
		status.attempts = []Attempt{{InquiryID: "inq_1", Status: StatusCompleted, CompletedAt: time.Now()}}
	}}
	handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{}).WithStatusReader(status).WithReconciler(reconciler)
	accountID := uuid.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/verification/status?inquiry_id=inq_1", nil)
	request = request.WithContext(accounts.WithUserID(request.Context(), accountID))
	response := httptest.NewRecorder()
	handler.GetStatus(response, request)
	var decoded StatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded.Status != "completed" || !decoded.Verified {
		t.Fatalf("decoded = %+v err = %v", decoded, err)
	}
	if len(reconciler.calls) != 1 || reconciler.account != accountID {
		t.Fatalf("calls = %v account = %s", reconciler.calls, reconciler.account)
	}
}

func TestStatusEndpointOnlyAsksPersonaAboutRecentUnsettledChecks(t *testing.T) {
	cases := map[string]Attempt{
		"settled":   {InquiryID: "inq_1", Status: StatusCompleted, CompletedAt: time.Now()},
		"abandoned": {InquiryID: "inq_1", Status: StatusCreated, CompletedAt: time.Now().Add(-lookupWindow - time.Hour)},
	}
	for name, attempt := range cases {
		t.Run(name, func(t *testing.T) {
			reconciler := &countingReconciler{}
			handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{}).
				WithStatusReader(&mutableStatus{attempts: []Attempt{attempt}}).WithReconciler(reconciler)
			handler.GetStatus(httptest.NewRecorder(), statusRequest(true))
			if len(reconciler.calls) != 0 {
				t.Fatalf("calls = %v", reconciler.calls)
			}
		})
	}
}

func TestStatusEndpointStillAnswersWhenPersonaIsDown(t *testing.T) {
	reconciler := &countingReconciler{err: ErrPersonaRequest}
	handler := NewHTTPHandler(&stubStarter{}, &stubProcessor{}).
		WithStatusReader(&mutableStatus{attempts: []Attempt{{InquiryID: "inq_1", Status: StatusCreated, CompletedAt: time.Now()}}}).
		WithReconciler(reconciler)
	response := httptest.NewRecorder()
	handler.GetStatus(response, statusRequest(true))
	var decoded StatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || response.Code != http.StatusOK || decoded.Status != "created" {
		t.Fatalf("code = %d decoded = %+v", response.Code, decoded)
	}
}

func TestPersonaClientGetInquiryReadsStatusTimestampAndVerifications(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotMethod = r.URL.Path, r.URL.RawQuery, r.Method
		_, _ = w.Write([]byte(`{"data":{"type":"inquiry","id":"inq_9","attributes":{"status":"approved","reference-id":"acct-1",
			"updated-at":"2026-11-26T10:00:00.000Z","decisioned-at":"2026-11-26T09:30:00.000Z","completed-at":"2026-11-26T09:00:00.000Z"}},
			"included":[{"type":"verification/government-id","id":"ver_1","attributes":{"status":"passed","name-first":"Ada","name-last":"Lovelace"}}]}`))
	}))
	defer server.Close()
	snapshot, err := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil).GetInquiry(context.Background(), "inq_9")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/inquiries/inq_9" || gotQuery != "include=verifications" {
		t.Fatalf("request = %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if snapshot.Status != "approved" || snapshot.ReferenceID != "acct-1" || !snapshot.ChangedAt.Equal(time.Date(2026, 11, 26, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if name, _ := passedGovernmentID(snapshot.Included); name.Last != "Lovelace" {
		t.Fatalf("name = %+v", name)
	}
}

func TestPersonaClientGetInquiryRejectsAMismatchedOrMalformedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"other inquiry":  `{"data":{"type":"inquiry","id":"inq_other","attributes":{"status":"approved"}}}`,
		"missing status": `{"data":{"type":"inquiry","id":"inq_9","attributes":{}}}`,
		"not json":       `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil).GetInquiry(context.Background(), "inq_9")
			if !errors.Is(err, ErrPersonaRequest) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPersonaClientResumeInquiryReturnsAFreshSessionToken(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte(`{"data":{"type":"inquiry","id":"inq_9"},"meta":{"session-token":"sess_again"}}`))
	}))
	defer server.Close()
	inquiry, err := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil).ResumeInquiry(context.Background(), "inq_9")
	if err != nil || inquiry.ID != "inq_9" || inquiry.SessionToken != "sess_again" {
		t.Fatalf("inquiry = %+v err = %v", inquiry, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/inquiries/inq_9/resume" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
}

func TestPersonaClientLookupsRejectMalformedIDsWithoutSendingARequest(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) }))
	defer server.Close()
	client := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil)
	if _, err := client.GetInquiry(context.Background(), "inq_../x"); !errors.Is(err, ErrPersonaRequest) {
		t.Fatalf("get err = %v", err)
	}
	if _, err := client.ResumeInquiry(context.Background(), "inq_a?b"); !errors.Is(err, ErrPersonaRequest) {
		t.Fatalf("resume err = %v", err)
	}
	if called.Load() {
		t.Fatal("a malformed inquiry id must never reach Persona")
	}
}

func TestPersonaClientRetriesABriefOutage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"inq_1"},"meta":{"session-token":"sess"}}`))
	}))
	defer server.Close()
	inquiry, err := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil).CreateInquiry(context.Background(), "t", "r")
	if err != nil || inquiry.ID != "inq_1" || requests.Load() != 2 {
		t.Fatalf("inquiry = %+v err = %v requests = %d", inquiry, err, requests.Load())
	}
}

func TestPersonaClientDoesNotRetryARejectedRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	_, err := NewPersonaClient(Config{APIKey: "k", BaseURL: server.URL}, nil).CreateInquiry(context.Background(), "t", "r")
	if !errors.Is(err, ErrPersonaRequest) || requests.Load() != 1 {
		t.Fatalf("err = %v requests = %d", err, requests.Load())
	}
}
