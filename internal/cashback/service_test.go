package cashback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	state       BookingState
	stateErr    error
	submissions map[string]Submission
	refunds     map[string]Refund
	hostID      uuid.UUID
	guestID     uuid.UUID
}

func newMemoryStore(hostID uuid.UUID) *memoryStore {
	return &memoryStore{
		state:       BookingState{BookingStatus: "confirmed", PaymentStatus: "succeeded", PaymentIntentID: "pi_1"},
		submissions: map[string]Submission{},
		refunds:     map[string]Refund{},
		hostID:      hostID,
	}
}

func (store *memoryStore) BookingState(context.Context, uuid.UUID, uuid.UUID) (BookingState, error) {
	return store.state, store.stateErr
}

func (store *memoryStore) SaveSubmission(_ context.Context, submission Submission) (Submission, error) {
	existing, found := store.submissions[submission.TaskKey]
	if found && existing.Status != TaskRetake {
		return Submission{}, ErrSubmissionLocked
	}
	store.submissions[submission.TaskKey] = submission
	return submission, nil
}

func (store *memoryStore) ListSubmissions(context.Context, uuid.UUID, uuid.UUID) ([]Submission, error) {
	var all []Submission
	for _, submission := range store.submissions {
		all = append(all, submission)
	}
	return all, nil
}

func (store *memoryStore) ListRefunds(context.Context, uuid.UUID, uuid.UUID) ([]Refund, error) {
	var all []Refund
	for _, refund := range store.refunds {
		all = append(all, refund)
	}
	return all, nil
}

func (store *memoryStore) HostBooking(_ context.Context, hostID uuid.UUID, bookingID uuid.UUID) (HostBooking, error) {
	if hostID != store.hostID {
		return HostBooking{}, ErrBookingNotFound
	}
	return HostBooking{BookingID: bookingID, GuestID: store.guestID, ListingTitle: "Emerald Escape"}, nil
}

func (store *memoryStore) ListPendingForHost(_ context.Context, hostID uuid.UUID) ([]Submission, error) {
	pending := []Submission{}
	if hostID != store.hostID {
		return pending, nil
	}
	for _, submission := range store.submissions {
		if submission.Status == TaskChecking {
			pending = append(pending, submission)
		}
	}
	return pending, nil
}

func (store *memoryStore) LoadReviewTarget(_ context.Context, _ uuid.UUID, submissionID uuid.UUID) (ReviewTarget, error) {
	for _, submission := range store.submissions {
		if submission.ID == submissionID {
			return ReviewTarget{Submission: submission, HostID: store.hostID, PaymentIntentID: store.state.PaymentIntentID}, nil
		}
	}
	return ReviewTarget{}, ErrSubmissionNotFound
}

func (store *memoryStore) ApplyReview(_ context.Context, _ uuid.UUID, submissionID uuid.UUID, status TaskStatus, note string, at time.Time) error {
	for key, submission := range store.submissions {
		if submission.ID == submissionID {
			submission.Status = status
			submission.Note = note
			submission.ReviewedAt = &at
			store.submissions[key] = submission
			return nil
		}
	}
	return ErrSubmissionNotFound
}

func (store *memoryStore) ApprovedTaskKeys(context.Context, uuid.UUID, uuid.UUID) ([]string, error) {
	var keys []string
	for key, submission := range store.submissions {
		if submission.Status == TaskApproved {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (store *memoryStore) ReserveRefund(_ context.Context, _ uuid.UUID, refund Refund) (Refund, bool, error) {
	if existing, found := store.refunds[refund.BundleKey]; found {
		return existing, false, nil
	}
	store.refunds[refund.BundleKey] = refund
	return refund, true, nil
}

func (store *memoryStore) SettleRefund(_ context.Context, _ uuid.UUID, refundID uuid.UUID, status RefundStatus, stripeRefundID string, _ time.Time) error {
	for key, refund := range store.refunds {
		if refund.ID == refundID {
			refund.Status = status
			refund.StripeRefundID = stripeRefundID
			store.refunds[key] = refund
		}
	}
	return nil
}

type fakeObjects struct {
	info       ObjectInfo
	missing    bool
	presignErr error
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{info: ObjectInfo{Size: 2048, ContentType: "image/jpeg"}}
}

func (objects *fakeObjects) PresignUpload(_ context.Context, key, contentType string, _ time.Duration) (string, error) {
	if objects.presignErr != nil {
		return "", objects.presignErr
	}
	return "https://bucket.example.com/" + key + "?type=" + contentType + "&signature=abc", nil
}

func (objects *fakeObjects) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://bucket.example.com/" + key + "?signature=download", nil
}

func (objects *fakeObjects) Stat(context.Context, string) (ObjectInfo, error) {
	if objects.missing {
		return ObjectInfo{}, errors.New("not found")
	}
	return objects.info, nil
}

type fakeVerifier struct{ verified bool }

func (verifier fakeVerifier) HasCompleted(context.Context, uuid.UUID) (bool, error) {
	return verifier.verified, nil
}

type fakeRefunder struct {
	calls []refundCall
	err   error
}

type refundCall struct {
	intentID string
	amount   int
	key      string
}

func (refunder *fakeRefunder) RefundPayment(_ context.Context, intentID string, amount int, key string) (string, error) {
	refunder.calls = append(refunder.calls, refundCall{intentID, amount, key})
	if refunder.err != nil {
		return "", refunder.err
	}
	return "re_" + key, nil
}

type harness struct {
	service  *CashbackService
	store    *memoryStore
	refunder *fakeRefunder
	objects  *fakeObjects
	hostID   uuid.UUID
	guestID  uuid.UUID
	booking  uuid.UUID
}

func newHarness(verified bool) harness {
	hostID := uuid.New()
	store := newMemoryStore(hostID)
	refunder := &fakeRefunder{}
	objects := newFakeObjects()
	return harness{
		service:  NewCashbackService(store, fakeVerifier{verified: verified}, refunder, objects),
		objects:  objects,
		store:    store,
		refunder: refunder,
		hostID:   hostID,
		guestID:  uuid.New(),
		booking:  uuid.New(),
	}
}

func (h harness) key(taskKey string) string {
	return photoKeyPrefix(h.booking, taskKey) + uuid.NewString() + ".jpg"
}

func (h harness) submit(t *testing.T, taskKey string) Submission {
	t.Helper()
	submission, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, taskKey, h.key(taskKey))
	if err != nil {
		t.Fatalf("SubmitPhoto(%s) error = %v", taskKey, err)
	}
	return submission
}

func (h harness) approve(t *testing.T, submission Submission) *Refund {
	t.Helper()
	_, refund, err := h.service.Review(context.Background(), h.hostID, submission.ID, DecisionApprove, "")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	return refund
}

func TestUnverifiedGuestCannotSubmitPhotos(t *testing.T) {
	h := newHarness(false)
	_, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher"))
	if !errors.Is(err, ErrNotVerified) {
		t.Fatalf("error = %v", err)
	}
	if len(h.store.submissions) != 0 {
		t.Fatal("submission saved for unverified guest")
	}
}

func TestVerifiedGuestWithUnpaidBookingCannotSubmit(t *testing.T) {
	cases := map[string]BookingState{
		"payment pending":   {BookingStatus: "pending", PaymentStatus: "pending", PaymentIntentID: "pi_1"},
		"payment failed":    {BookingStatus: "pending", PaymentStatus: "failed", PaymentIntentID: "pi_1"},
		"booking cancelled": {BookingStatus: "cancelled", PaymentStatus: "succeeded", PaymentIntentID: "pi_1"},
		"no payment":        {BookingStatus: "confirmed"},
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(true)
			h.store.state = state
			_, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher"))
			if !errors.Is(err, ErrBookingNotEarning) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSubmitPhotoRejectsBadInput(t *testing.T) {
	h := newHarness(true)
	if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "not_a_task", h.key("run_dishwasher")); !errors.Is(err, ErrUnknownTask) {
		t.Fatalf("unknown task error = %v", err)
	}
	otherBooking := photoKeyPrefix(uuid.New(), "run_dishwasher") + uuid.NewString() + ".jpg"
	otherTask := photoKeyPrefix(h.booking, "empty_fridge") + uuid.NewString() + ".jpg"
	traversal := photoKeyPrefix(h.booking, "run_dishwasher") + "../../secret.jpg"
	for name, key := range map[string]string{
		"another booking's key": otherBooking,
		"another task's key":    otherTask,
		"path traversal":        traversal,
		"external url":          "https://evil.example.com/a.jpg",
		"empty":                 "",
		"wrong extension":       photoKeyPrefix(h.booking, "run_dishwasher") + uuid.NewString() + ".exe",
	} {
		if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", key); !errors.Is(err, ErrInvalidPhotoKey) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestSubmitPhotoRequiresTheFileToExistInStorage(t *testing.T) {
	h := newHarness(true)
	h.objects.missing = true
	if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher")); !errors.Is(err, ErrPhotoNotUploaded) {
		t.Fatalf("error = %v", err)
	}
	if len(h.store.submissions) != 0 {
		t.Fatal("submission saved without an uploaded file")
	}
}

func TestSubmitPhotoRejectsUploadedFilesThatBreakTheRules(t *testing.T) {
	cases := map[string]ObjectInfo{
		"too large":      {Size: maxPhotoBytes + 1, ContentType: "image/jpeg"},
		"empty":          {Size: 0, ContentType: "image/jpeg"},
		"not an image":   {Size: 100, ContentType: "application/pdf"},
		"html disguised": {Size: 100, ContentType: "text/html"},
	}
	for name, info := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(true)
			h.objects.info = info
			if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher")); !errors.Is(err, ErrPhotoRejected) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestSubmitPhotoReturnsASignedViewingURLNotTheStorageKey(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	if !strings.Contains(submission.PhotoURL, "signature=download") {
		t.Fatalf("photo url = %q", submission.PhotoURL)
	}
	trip, _ := h.service.Trip(context.Background(), h.guestID, h.booking)
	if !strings.Contains(trip.Bundles[0].Tasks[0].PhotoURL, "signature=download") {
		t.Fatalf("trip photo url = %q", trip.Bundles[0].Tasks[0].PhotoURL)
	}
}

func TestRequestUploadIssuesATicketScopedToTheBookingAndTask(t *testing.T) {
	h := newHarness(true)
	ticket, err := h.service.RequestUpload(context.Background(), h.guestID, h.booking, "run_dishwasher", "image/png", 4096)
	if err != nil {
		t.Fatalf("RequestUpload() error = %v", err)
	}
	if !validPhotoKey(ticket.PhotoKey, h.booking, "run_dishwasher") || !strings.HasSuffix(ticket.PhotoKey, ".png") {
		t.Fatalf("key = %q", ticket.PhotoKey)
	}
	if ticket.ExpiresIn != 600 || ticket.MaxBytes != maxPhotoBytes || !strings.Contains(ticket.UploadURL, "type=image/png") {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestRequestUploadValidatesTypeSizeTaskAndEligibility(t *testing.T) {
	ctx := context.Background()
	h := newHarness(true)
	for name, test := range map[string]struct {
		task, contentType string
		size              int64
		want              error
	}{
		"pdf":      {"run_dishwasher", "application/pdf", 100, ErrPhotoRejected},
		"svg":      {"run_dishwasher", "image/svg+xml", 100, ErrPhotoRejected},
		"zero":     {"run_dishwasher", "image/jpeg", 0, ErrPhotoRejected},
		"negative": {"run_dishwasher", "image/jpeg", -5, ErrPhotoRejected},
		"huge":     {"run_dishwasher", "image/jpeg", maxPhotoBytes + 1, ErrPhotoRejected},
		"bad task": {"nope", "image/jpeg", 100, ErrUnknownTask},
	} {
		if _, err := h.service.RequestUpload(ctx, h.guestID, h.booking, test.task, test.contentType, test.size); !errors.Is(err, test.want) {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	unverified := newHarness(false)
	if _, err := unverified.service.RequestUpload(ctx, unverified.guestID, unverified.booking, "run_dishwasher", "image/jpeg", 100); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("unverified error = %v", err)
	}
	unpaid := newHarness(true)
	unpaid.store.state = BookingState{BookingStatus: "pending", PaymentStatus: "pending", PaymentIntentID: "pi_1"}
	if _, err := unpaid.service.RequestUpload(ctx, unpaid.guestID, unpaid.booking, "run_dishwasher", "image/jpeg", 100); !errors.Is(err, ErrBookingNotEarning) {
		t.Fatalf("unpaid error = %v", err)
	}
}

func TestUploadsDisabledWhenNoStorageIsConfigured(t *testing.T) {
	h := newHarness(true)
	h.service.objects = nil
	if _, err := h.service.RequestUpload(context.Background(), h.guestID, h.booking, "run_dishwasher", "image/jpeg", 100); !errors.Is(err, ErrUploadsDisabled) {
		t.Fatalf("request error = %v", err)
	}
	if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher")); !errors.Is(err, ErrUploadsDisabled) {
		t.Fatalf("submit error = %v", err)
	}
}

func TestStorageConfigFromLookup(t *testing.T) {
	lookup := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if _, enabled, err := StorageConfigFromLookup(lookup(map[string]string{})); enabled || err != nil {
		t.Fatalf("empty: enabled = %v, err = %v", enabled, err)
	}
	if _, _, err := StorageConfigFromLookup(lookup(map[string]string{"S3_BUCKET": "b"})); err == nil {
		t.Fatal("partial config must be an error")
	}
	config, enabled, err := StorageConfigFromLookup(lookup(map[string]string{
		"S3_ENDPOINT": "https://storage.example.com", "S3_BUCKET": "b", "S3_ACCESS_KEY_ID": "id", "S3_SECRET_ACCESS_KEY": "secret",
	}))
	if err != nil || !enabled || config.Endpoint != "storage.example.com" || !config.UseSSL || config.Region != "auto" {
		t.Fatalf("config = %+v, enabled = %v, err = %v", config, enabled, err)
	}
}

func TestPartialBundleApprovalPaysNothing(t *testing.T) {
	h := newHarness(true)
	refund := h.approve(t, h.submit(t, "run_dishwasher"))
	h.approve(t, h.submit(t, "empty_fridge"))
	if refund != nil || len(h.refunder.calls) != 0 {
		t.Fatalf("refund = %+v, calls = %d", refund, len(h.refunder.calls))
	}
}

func TestCompletingBundleRefundsFiftyDollarsOnce(t *testing.T) {
	h := newHarness(true)
	h.approve(t, h.submit(t, "run_dishwasher"))
	h.approve(t, h.submit(t, "empty_fridge"))
	refund := h.approve(t, h.submit(t, "wipe_counters"))
	if refund == nil || refund.Status != RefundSucceeded || refund.AmountCents != 5000 {
		t.Fatalf("refund = %+v", refund)
	}
	if len(h.refunder.calls) != 1 || h.refunder.calls[0].amount != 5000 || h.refunder.calls[0].intentID != "pi_1" {
		t.Fatalf("calls = %+v", h.refunder.calls)
	}
}

func TestTwoTaskBundleRefundsAfterSecondApproval(t *testing.T) {
	h := newHarness(true)
	h.approve(t, h.submit(t, "strip_beds"))
	refund := h.approve(t, h.submit(t, "towels_in_hamper"))
	if refund == nil || refund.BundleKey != "beds_towels" {
		t.Fatalf("refund = %+v", refund)
	}
}

func TestEachCompletedBundlePaysSeparately(t *testing.T) {
	h := newHarness(true)
	for _, key := range []string{"strip_beds", "towels_in_hamper", "take_out_trash", "lights_thermostat"} {
		h.approve(t, h.submit(t, key))
	}
	if len(h.refunder.calls) != 2 {
		t.Fatalf("calls = %d", len(h.refunder.calls))
	}
	if h.refunder.calls[0].key == h.refunder.calls[1].key {
		t.Fatal("idempotency keys must differ per bundle")
	}
	trip, err := h.service.Trip(context.Background(), h.guestID, h.booking)
	if err != nil || trip.EarnedCents != 10000 {
		t.Fatalf("trip = %+v, err = %v", trip, err)
	}
}

func TestRetakeThenResubmitThenApprovePaysOut(t *testing.T) {
	h := newHarness(true)
	h.approve(t, h.submit(t, "strip_beds"))
	dark := h.submit(t, "towels_in_hamper")
	reviewed, refund, err := h.service.Review(context.Background(), h.hostID, dark.ID, DecisionRetake, "Too dark")
	if err != nil || reviewed.Status != TaskRetake || refund != nil {
		t.Fatalf("reviewed = %+v, refund = %+v, err = %v", reviewed, refund, err)
	}
	retaken := h.submit(t, "towels_in_hamper")
	if refund := h.approve(t, retaken); refund == nil || refund.Status != RefundSucceeded {
		t.Fatalf("refund = %+v", refund)
	}
}

func TestPhotoCannotBeReplacedWhileWaitingOrApproved(t *testing.T) {
	h := newHarness(true)
	waiting := h.submit(t, "run_dishwasher")
	if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher")); !errors.Is(err, ErrSubmissionLocked) {
		t.Fatalf("waiting error = %v", err)
	}
	h.approve(t, waiting)
	if _, err := h.service.SubmitPhoto(context.Background(), h.guestID, h.booking, "run_dishwasher", h.key("run_dishwasher")); !errors.Is(err, ErrSubmissionLocked) {
		t.Fatalf("approved error = %v", err)
	}
}

func TestOnlyTheListingHostCanReview(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	_, _, err := h.service.Review(context.Background(), uuid.New(), submission.ID, DecisionApprove, "")
	if !errors.Is(err, ErrNotHost) {
		t.Fatalf("error = %v", err)
	}
}

func TestGuestCannotApproveTheirOwnPhoto(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	_, _, err := h.service.Review(context.Background(), h.guestID, submission.ID, DecisionApprove, "")
	if !errors.Is(err, ErrNotHost) {
		t.Fatalf("error = %v", err)
	}
	if len(h.refunder.calls) != 0 {
		t.Fatal("refund issued from guest self-approval")
	}
}

func TestReviewRejectsAlreadyReviewedSubmission(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	h.approve(t, submission)
	_, _, err := h.service.Review(context.Background(), h.hostID, submission.ID, DecisionApprove, "")
	if !errors.Is(err, ErrNotAwaitingReview) {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewValidatesDecision(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	if _, _, err := h.service.Review(context.Background(), h.hostID, submission.ID, "maybe", ""); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("decision error = %v", err)
	}
	if _, _, err := h.service.Review(context.Background(), h.hostID, submission.ID, DecisionRetake, "  "); !errors.Is(err, ErrRetakeNeedsNote) {
		t.Fatalf("note error = %v", err)
	}
}

func TestStripeRefundFailureIsRecordedAndSurfaced(t *testing.T) {
	h := newHarness(true)
	h.refunder.err = errors.New("charge already refunded")
	h.approve(t, h.submit(t, "strip_beds"))
	_, _, err := h.service.Review(context.Background(), h.hostID, h.submit(t, "towels_in_hamper").ID, DecisionApprove, "")
	if err == nil {
		t.Fatal("expected refund error")
	}
	if h.store.refunds["beds_towels"].Status != RefundFailed {
		t.Fatalf("refund status = %s", h.store.refunds["beds_towels"].Status)
	}
	trip, _ := h.service.Trip(context.Background(), h.guestID, h.booking)
	if trip.EarnedCents != 0 {
		t.Fatalf("earned = %d", trip.EarnedCents)
	}
}

func TestTripShowsStatusesAndGatesEarning(t *testing.T) {
	h := newHarness(false)
	trip, err := h.service.Trip(context.Background(), h.guestID, h.booking)
	if err != nil || trip.Verified || trip.CanEarn || len(trip.Bundles) != 3 {
		t.Fatalf("trip = %+v, err = %v", trip, err)
	}
	for _, bundle := range trip.Bundles {
		if bundle.Status != TaskTodo || bundle.AmountCents != 5000 {
			t.Fatalf("bundle = %+v", bundle)
		}
	}

	h = newHarness(true)
	h.submit(t, "run_dishwasher")
	trip, _ = h.service.Trip(context.Background(), h.guestID, h.booking)
	if !trip.CanEarn || trip.Bundles[0].Status != TaskChecking {
		t.Fatalf("trip = %+v", trip)
	}
}

func TestTripExposesSubmissionIDsSoHostsCanReview(t *testing.T) {
	h := newHarness(true)
	submission := h.submit(t, "run_dishwasher")
	trip, err := h.service.Trip(context.Background(), h.guestID, h.booking)
	if err != nil {
		t.Fatal(err)
	}
	task := trip.Bundles[0].Tasks[0]
	if task.SubmissionID == nil || *task.SubmissionID != submission.ID {
		t.Fatalf("submission id = %v, want %v", task.SubmissionID, submission.ID)
	}
	if trip.Bundles[0].Tasks[1].SubmissionID != nil {
		t.Fatal("task without a photo must not expose a submission id")
	}
}

func TestHostTripShowsPhotosToTheListingHostOnly(t *testing.T) {
	h := newHarness(true)
	h.store.guestID = h.guestID
	h.submit(t, "run_dishwasher")
	trip, err := h.service.HostTrip(context.Background(), h.hostID, h.booking)
	if err != nil || trip.GuestID != h.guestID || trip.ListingTitle != "Emerald Escape" || trip.Bundles[0].Tasks[0].Status != TaskChecking {
		t.Fatalf("trip = %+v, err = %v", trip, err)
	}
	if _, err := h.service.HostTrip(context.Background(), uuid.New(), h.booking); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("stranger error = %v", err)
	}
}

func TestPendingForHostListsOnlyPhotosWaitingForReview(t *testing.T) {
	h := newHarness(true)
	waiting := h.submit(t, "run_dishwasher")
	approved := h.submit(t, "empty_fridge")
	h.approve(t, approved)
	pending, err := h.service.PendingForHost(context.Background(), h.hostID)
	if err != nil || len(pending) != 1 || pending[0].Submission.ID != waiting.ID || pending[0].TaskLabel != "Run the dishwasher" {
		t.Fatalf("pending = %+v, err = %v", pending, err)
	}
	other, err := h.service.PendingForHost(context.Background(), uuid.New())
	if err != nil || len(other) != 0 {
		t.Fatalf("other host pending = %+v, err = %v", other, err)
	}
}
