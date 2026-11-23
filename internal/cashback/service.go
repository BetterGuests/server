package cashback

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotVerified        = errors.New("identity verification required")
	ErrBookingNotEarning  = errors.New("booking is not paid and confirmed")
	ErrBookingNotFound    = errors.New("booking not found")
	ErrUnknownTask        = errors.New("unknown task")
	ErrInvalidPhotoKey    = errors.New("photo key is not valid for this task")
	ErrPhotoNotUploaded   = errors.New("photo was not uploaded")
	ErrPhotoRejected      = errors.New("photo must be a JPEG, PNG, WebP or HEIC image up to 10 MB")
	ErrUploadsDisabled    = errors.New("photo upload is not set up")
	ErrSubmissionLocked   = errors.New("task photo is already waiting or approved")
	ErrSubmissionNotFound = errors.New("submission not found")
	ErrNotHost            = errors.New("only the listing host can review")
	ErrNotAwaitingReview  = errors.New("submission is not awaiting review")
	ErrInvalidDecision    = errors.New("decision must be approve or retake")
	ErrRetakeNeedsNote    = errors.New("a retake needs a note")
)

const (
	maxNoteLength       = 500
	maxPhotoBytes       = 10 << 20
	uploadURLLifetime   = 10 * time.Minute
	downloadURLLifetime = time.Hour
)

var allowedPhotoTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/heic": ".heic",
}

var photoKeyPattern = regexp.MustCompile(`^[0-9a-f-]{36}\.(jpg|png|webp|heic)$`)

func photoKeyPrefix(bookingID uuid.UUID, taskKey string) string {
	return "cashback/" + bookingID.String() + "/" + taskKey + "/"
}

func validPhotoKey(key string, bookingID uuid.UUID, taskKey string) bool {
	prefix := photoKeyPrefix(bookingID, taskKey)
	return strings.HasPrefix(key, prefix) && photoKeyPattern.MatchString(strings.TrimPrefix(key, prefix))
}

var catalog = []Bundle{
	{Key: "kitchen", Label: "Kitchen", Tasks: []Task{
		{Key: "run_dishwasher", Label: "Run the dishwasher", Hint: "Show the loaded racks or the running display."},
		{Key: "empty_fridge", Label: "Empty the fridge", Hint: "Open the fridge door, turn on the kitchen light, and show every shelf."},
		{Key: "wipe_counters", Label: "Wipe the counters", Hint: "Show the kitchen counters, clear and wiped."},
	}},
	{Key: "beds_towels", Label: "Beds & towels", Tasks: []Task{
		{Key: "strip_beds", Label: "Strip the beds", Hint: "Show a bare mattress and the linens in the hamper."},
		{Key: "towels_in_hamper", Label: "Towels in the hamper", Hint: "Show the used towels in the hamper."},
	}},
	{Key: "lock_up", Label: "Lock-up", Tasks: []Task{
		{Key: "take_out_trash", Label: "Take out the trash", Hint: "Show the bag inside the outdoor bin, lid open."},
		{Key: "lights_thermostat", Label: "Lights off, thermostat 72°", Hint: "Show the thermostat set to 72°."},
	}},
}

func Catalog() []Bundle {
	return catalog
}

func bundleForTask(taskKey string) (Bundle, bool) {
	for _, bundle := range catalog {
		for _, task := range bundle.Tasks {
			if task.Key == taskKey {
				return bundle, true
			}
		}
	}
	return Bundle{}, false
}

type CashbackService struct {
	store    Store
	verified VerificationChecker
	refunds  Refunder
	objects  ObjectStore
	now      func() time.Time
	newID    func() uuid.UUID
}

func NewCashbackService(store Store, verified VerificationChecker, refunds Refunder, objects ObjectStore) *CashbackService {
	return &CashbackService{store: store, verified: verified, refunds: refunds, objects: objects, now: time.Now, newID: uuid.New}
}

func (service *CashbackService) RequestUpload(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, contentType string, sizeBytes int64) (UploadTicket, error) {
	if service.objects == nil {
		return UploadTicket{}, ErrUploadsDisabled
	}
	if _, ok := bundleForTask(taskKey); !ok {
		return UploadTicket{}, ErrUnknownTask
	}
	extension, allowed := allowedPhotoTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxPhotoBytes {
		return UploadTicket{}, ErrPhotoRejected
	}
	if err := service.requireEarning(ctx, guestID, bookingID); err != nil {
		return UploadTicket{}, err
	}
	key := photoKeyPrefix(bookingID, taskKey) + service.newID().String() + extension
	url, err := service.objects.PresignUpload(ctx, key, contentType, uploadURLLifetime)
	if err != nil {
		return UploadTicket{}, fmt.Errorf("presign upload: %w", err)
	}
	return UploadTicket{
		UploadURL:   url,
		PhotoKey:    key,
		ContentType: contentType,
		ExpiresIn:   int(uploadURLLifetime.Seconds()),
		MaxBytes:    maxPhotoBytes,
	}, nil
}

func (service *CashbackService) requireEarning(ctx context.Context, guestID, bookingID uuid.UUID) error {
	verified, err := service.verified.HasCompleted(ctx, guestID)
	if err != nil {
		return fmt.Errorf("check verification: %w", err)
	}
	if !verified {
		return ErrNotVerified
	}
	state, err := service.store.BookingState(ctx, guestID, bookingID)
	if err != nil {
		return err
	}
	if !isEarning(state) {
		return ErrBookingNotEarning
	}
	return nil
}

func (service *CashbackService) SubmitPhoto(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, photoKey string) (Submission, error) {
	if service.objects == nil {
		return Submission{}, ErrUploadsDisabled
	}
	if _, ok := bundleForTask(taskKey); !ok {
		return Submission{}, ErrUnknownTask
	}
	if !validPhotoKey(photoKey, bookingID, taskKey) {
		return Submission{}, ErrInvalidPhotoKey
	}
	if err := service.requireEarning(ctx, guestID, bookingID); err != nil {
		return Submission{}, err
	}
	info, err := service.objects.Stat(ctx, photoKey)
	if err != nil {
		return Submission{}, ErrPhotoNotUploaded
	}
	if _, allowed := allowedPhotoTypes[info.ContentType]; !allowed || info.Size <= 0 || info.Size > maxPhotoBytes {
		return Submission{}, ErrPhotoRejected
	}
	now := service.now().UTC()
	saved, err := service.store.SaveSubmission(ctx, Submission{
		ID:        service.newID(),
		BookingID: bookingID,
		GuestID:   guestID,
		TaskKey:   taskKey,
		PhotoURL:  photoKey,
		Status:    TaskChecking,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return Submission{}, err
	}
	return service.signed(ctx, saved), nil
}

func (service *CashbackService) signed(ctx context.Context, submission Submission) Submission {
	if service.objects == nil || submission.PhotoURL == "" {
		return submission
	}
	url, err := service.objects.PresignDownload(ctx, submission.PhotoURL, downloadURLLifetime)
	if err != nil {
		submission.PhotoURL = ""
		return submission
	}
	submission.PhotoURL = url
	return submission
}

func (service *CashbackService) signedAll(ctx context.Context, submissions []Submission) []Submission {
	signed := make([]Submission, 0, len(submissions))
	for _, submission := range submissions {
		signed = append(signed, service.signed(ctx, submission))
	}
	return signed
}

func (service *CashbackService) Review(ctx context.Context, hostID, submissionID uuid.UUID, decision Decision, note string) (Submission, *Refund, error) {
	status, err := statusForDecision(decision, note)
	if err != nil {
		return Submission{}, nil, err
	}
	target, err := service.store.LoadReviewTarget(ctx, hostID, submissionID)
	if err != nil {
		return Submission{}, nil, err
	}
	if target.HostID != hostID {
		return Submission{}, nil, ErrNotHost
	}
	if target.Submission.Status != TaskChecking {
		return Submission{}, nil, ErrNotAwaitingReview
	}

	reviewedAt := service.now().UTC()
	trimmed := strings.TrimSpace(note)
	if err := service.store.ApplyReview(ctx, hostID, submissionID, status, trimmed, reviewedAt); err != nil {
		return Submission{}, nil, err
	}
	reviewed := service.signed(ctx, target.Submission)
	reviewed.Status = status
	reviewed.Note = trimmed
	reviewed.ReviewedAt = &reviewedAt

	if status != TaskApproved {
		return reviewed, nil, nil
	}
	refund, err := service.refundIfBundleComplete(ctx, hostID, target)
	if err != nil {
		return reviewed, nil, err
	}
	return reviewed, refund, nil
}

func (service *CashbackService) refundIfBundleComplete(ctx context.Context, hostID uuid.UUID, target ReviewTarget) (*Refund, error) {
	bundle, ok := bundleForTask(target.Submission.TaskKey)
	if !ok {
		return nil, ErrUnknownTask
	}
	approved, err := service.store.ApprovedTaskKeys(ctx, hostID, target.Submission.BookingID)
	if err != nil {
		return nil, fmt.Errorf("load approved tasks: %w", err)
	}
	if !bundleComplete(bundle, approved) {
		return nil, nil
	}

	now := service.now().UTC()
	reserved, created, err := service.store.ReserveRefund(ctx, hostID, Refund{
		ID:          service.newID(),
		BookingID:   target.Submission.BookingID,
		GuestID:     target.Submission.GuestID,
		BundleKey:   bundle.Key,
		AmountCents: BundleRefundCents,
		Status:      RefundPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		return nil, fmt.Errorf("reserve refund: %w", err)
	}
	if !created && reserved.Status != RefundFailed {
		return &reserved, nil
	}

	key := fmt.Sprintf("cashback-%s-%s", reserved.BookingID, reserved.BundleKey)
	stripeRefundID, err := service.refunds.RefundPayment(ctx, target.PaymentIntentID, reserved.AmountCents, key)
	if err != nil {
		_ = service.store.SettleRefund(ctx, hostID, reserved.ID, RefundFailed, "", service.now().UTC())
		return nil, fmt.Errorf("issue refund: %w", err)
	}
	if err := service.store.SettleRefund(ctx, hostID, reserved.ID, RefundSucceeded, stripeRefundID, service.now().UTC()); err != nil {
		return nil, fmt.Errorf("record refund: %w", err)
	}
	reserved.Status = RefundSucceeded
	reserved.StripeRefundID = stripeRefundID
	return &reserved, nil
}

func (service *CashbackService) Trip(ctx context.Context, guestID, bookingID uuid.UUID) (TripView, error) {
	state, err := service.store.BookingState(ctx, guestID, bookingID)
	if err != nil {
		return TripView{}, err
	}
	verified, err := service.verified.HasCompleted(ctx, guestID)
	if err != nil {
		return TripView{}, fmt.Errorf("check verification: %w", err)
	}
	submissions, err := service.store.ListSubmissions(ctx, guestID, bookingID)
	if err != nil {
		return TripView{}, err
	}
	refunds, err := service.store.ListRefunds(ctx, guestID, bookingID)
	if err != nil {
		return TripView{}, err
	}
	return buildTrip(bookingID, verified && isEarning(state), verified, service.signedAll(ctx, submissions), refunds), nil
}

func (service *CashbackService) HostTrip(ctx context.Context, hostID, bookingID uuid.UUID) (HostTripView, error) {
	booking, err := service.store.HostBooking(ctx, hostID, bookingID)
	if err != nil {
		return HostTripView{}, err
	}
	submissions, err := service.store.ListSubmissions(ctx, hostID, bookingID)
	if err != nil {
		return HostTripView{}, err
	}
	refunds, err := service.store.ListRefunds(ctx, hostID, bookingID)
	if err != nil {
		return HostTripView{}, err
	}
	trip := buildTrip(bookingID, false, false, service.signedAll(ctx, submissions), refunds)
	return HostTripView{
		BookingID:    bookingID,
		GuestID:      booking.GuestID,
		ListingTitle: booking.ListingTitle,
		EarnedCents:  trip.EarnedCents,
		Bundles:      trip.Bundles,
	}, nil
}

func (service *CashbackService) PendingForHost(ctx context.Context, hostID uuid.UUID) ([]PendingSubmission, error) {
	submissions, err := service.store.ListPendingForHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	submissions = service.signedAll(ctx, submissions)
	pending := make([]PendingSubmission, 0, len(submissions))
	for _, submission := range submissions {
		pending = append(pending, PendingSubmission{Submission: submission, TaskLabel: taskLabel(submission.TaskKey)})
	}
	return pending, nil
}

func taskLabel(taskKey string) string {
	for _, bundle := range catalog {
		for _, task := range bundle.Tasks {
			if task.Key == taskKey {
				return task.Label
			}
		}
	}
	return taskKey
}

func buildTrip(bookingID uuid.UUID, canEarn, verified bool, submissions []Submission, refunds []Refund) TripView {
	byTask := map[string]Submission{}
	for _, submission := range submissions {
		byTask[submission.TaskKey] = submission
	}
	refundByBundle := map[string]Refund{}
	for _, refund := range refunds {
		refundByBundle[refund.BundleKey] = refund
	}

	view := TripView{BookingID: bookingID, Verified: verified, CanEarn: canEarn, Bundles: []BundleView{}}
	for _, bundle := range catalog {
		bundleView := BundleView{Key: bundle.Key, Label: bundle.Label, AmountCents: BundleRefundCents}
		statuses := make([]TaskStatus, 0, len(bundle.Tasks))
		for _, task := range bundle.Tasks {
			taskView := TaskView{Key: task.Key, Label: task.Label, Hint: task.Hint, Status: TaskTodo}
			if submission, ok := byTask[task.Key]; ok {
				submissionID := submission.ID
				taskView.SubmissionID = &submissionID
				taskView.Status = submission.Status
				taskView.Note = submission.Note
				taskView.PhotoURL = submission.PhotoURL
			}
			statuses = append(statuses, taskView.Status)
			bundleView.Tasks = append(bundleView.Tasks, taskView)
		}
		bundleView.Status = bundleStatus(statuses)
		if refund, ok := refundByBundle[bundle.Key]; ok {
			bundleView.RefundStatus = refund.Status
			if refund.Status == RefundSucceeded {
				view.EarnedCents += refund.AmountCents
			}
		}
		view.Bundles = append(view.Bundles, bundleView)
	}
	return view
}

func bundleStatus(statuses []TaskStatus) TaskStatus {
	approved, checking, retake := 0, 0, 0
	for _, status := range statuses {
		switch status {
		case TaskApproved:
			approved++
		case TaskChecking:
			checking++
		case TaskRetake:
			retake++
		}
	}
	switch {
	case approved == len(statuses):
		return TaskApproved
	case checking > 0:
		return TaskChecking
	case retake > 0:
		return TaskRetake
	}
	return TaskTodo
}

func bundleComplete(bundle Bundle, approvedKeys []string) bool {
	approved := map[string]bool{}
	for _, key := range approvedKeys {
		approved[key] = true
	}
	for _, task := range bundle.Tasks {
		if !approved[task.Key] {
			return false
		}
	}
	return true
}

func isEarning(state BookingState) bool {
	return state.BookingStatus == "confirmed" && state.PaymentStatus == "succeeded" && state.PaymentIntentID != ""
}

func statusForDecision(decision Decision, note string) (TaskStatus, error) {
	switch decision {
	case DecisionApprove:
		return TaskApproved, nil
	case DecisionRetake:
		trimmed := strings.TrimSpace(note)
		if trimmed == "" {
			return "", ErrRetakeNeedsNote
		}
		if len(trimmed) > maxNoteLength {
			return "", fmt.Errorf("note must be %d characters or fewer", maxNoteLength)
		}
		return TaskRetake, nil
	}
	return "", ErrInvalidDecision
}
