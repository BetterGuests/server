package cashback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BetterGuests/BetterGuests-Server/internal/chores"
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
	ErrInvalidDecision    = errors.New("decision must be approve or reject")
	ErrRejectNeedsNote    = errors.New("a rejection needs a note")
	ErrPhotoWindowClosed  = errors.New("photos are open from checkout day until 24 hours after checkout")
	ErrReviewClosed       = errors.New("the review window for this stay has closed")
	ErrListingNotFound    = errors.New("listing not found")
	ErrInvalidPlan        = errors.New("check your chores")
)

const (
	maxNoteLength       = 500
	maxPhotoBytes       = 10 << 20
	maxChoresPerGroup   = 10
	maxChoreLabelLength = 80
	customChorePrefix   = "custom_"
	customChoreLetters  = 12
	ownChoreHint        = "Show it done."
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

var choreKeyPattern = regexp.MustCompile(`^[a-z_]{3,40}$`)

var examplePhotoTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

func photoKeyPrefix(bookingID uuid.UUID, taskKey string) string {
	return "cashback/" + bookingID.String() + "/" + taskKey + "/"
}

func validPhotoKey(key string, bookingID uuid.UUID, taskKey string) bool {
	prefix := photoKeyPrefix(bookingID, taskKey)
	return strings.HasPrefix(key, prefix) && photoKeyPattern.MatchString(strings.TrimPrefix(key, prefix))
}

type CashbackService struct {
	store    Store
	verified VerificationChecker
	objects  ObjectStore
	now      func() time.Time
	newID    func() uuid.UUID
}

func NewCashbackService(store Store, verified VerificationChecker, objects ObjectStore) *CashbackService {
	return &CashbackService{store: store, verified: verified, objects: objects, now: time.Now, newID: uuid.New}
}

func ScheduleFor(endsOn time.Time) Schedule {
	day := time.Date(endsOn.Year(), endsOn.Month(), endsOn.Day(), 0, 0, 0, 0, time.UTC)
	checkout := day.Add(CheckoutHourUTC * time.Hour)
	return Schedule{PhotosOpenAt: day, PhotosCloseAt: checkout.Add(PhotoWindow), SettlesAt: checkout.Add(SettlementDelay)}
}

func LatestSettleableEndsOn(now time.Time) time.Time {
	last := now.UTC().Add(-(SettlementDelay + CheckoutHourUTC*time.Hour))
	return time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
}

func CapCents(paidCents int) int {
	return paidCents * CapPercent / 100
}

func EarnableCents(plan chores.Plan, acceptedTaskKeys []string, paidCents int) int {
	earned := 0
	for _, group := range plan.Offered() {
		if groupComplete(group, acceptedTaskKeys) {
			earned += group.AmountCents
		}
	}
	return min(earned, CapCents(paidCents))
}

func (service *CashbackService) photosOpen(endsOn time.Time) bool {
	schedule := ScheduleFor(endsOn)
	now := service.now()
	return !now.Before(schedule.PhotosOpenAt) && now.Before(schedule.PhotosCloseAt)
}

func (service *CashbackService) RequestUpload(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, contentType string, sizeBytes int64) (UploadTicket, error) {
	if service.objects == nil {
		return UploadTicket{}, ErrUploadsDisabled
	}
	extension, allowed := allowedPhotoTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxPhotoBytes {
		return UploadTicket{}, ErrPhotoRejected
	}
	if err := service.requireEarning(ctx, guestID, bookingID); err != nil {
		return UploadTicket{}, err
	}
	if err := service.requireChore(ctx, guestID, bookingID, taskKey); err != nil {
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
	if !service.photosOpen(state.EndsOn) {
		return ErrPhotoWindowClosed
	}
	return nil
}

func (service *CashbackService) bookingPlan(ctx context.Context, actingID, bookingID uuid.UUID) (chores.Plan, error) {
	raw, err := service.store.BookingPlan(ctx, actingID, bookingID)
	if err != nil {
		return nil, err
	}
	return chores.PlanFromJSON(raw)
}

func (service *CashbackService) requireChore(ctx context.Context, guestID, bookingID uuid.UUID, taskKey string) error {
	plan, err := service.bookingPlan(ctx, guestID, bookingID)
	if err != nil {
		return err
	}
	if _, ok := plan.Find(taskKey); !ok {
		return ErrUnknownTask
	}
	return nil
}

func (service *CashbackService) SubmitPhoto(ctx context.Context, guestID, bookingID uuid.UUID, taskKey, photoKey string) (Submission, error) {
	if service.objects == nil {
		return Submission{}, ErrUploadsDisabled
	}
	if !validPhotoKey(photoKey, bookingID, taskKey) {
		return Submission{}, ErrInvalidPhotoKey
	}
	if err := service.requireEarning(ctx, guestID, bookingID); err != nil {
		return Submission{}, err
	}
	if err := service.requireChore(ctx, guestID, bookingID, taskKey); err != nil {
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

func (service *CashbackService) Review(ctx context.Context, hostID, submissionID uuid.UUID, decision Decision, note string) (Submission, error) {
	status, err := statusForDecision(decision, note)
	if err != nil {
		return Submission{}, err
	}
	target, err := service.store.LoadReviewTarget(ctx, hostID, submissionID)
	if err != nil {
		return Submission{}, err
	}
	if target.HostID != hostID {
		return Submission{}, ErrNotHost
	}
	if target.Submission.Status != TaskChecking {
		return Submission{}, ErrNotAwaitingReview
	}

	reviewedAt := service.now().UTC()
	if !reviewedAt.Before(ScheduleFor(target.EndsOn).SettlesAt) {
		return Submission{}, ErrReviewClosed
	}
	trimmed := strings.TrimSpace(note)
	if err := service.store.ApplyReview(ctx, hostID, submissionID, status, trimmed, reviewedAt); err != nil {
		return Submission{}, err
	}
	reviewed := service.signed(ctx, target.Submission)
	reviewed.Status = status
	reviewed.Note = trimmed
	reviewed.ReviewedAt = &reviewedAt
	return reviewed, nil
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
	settlement, err := service.store.Settlement(ctx, guestID, bookingID)
	if err != nil {
		return TripView{}, err
	}
	plan, err := service.bookingPlan(ctx, guestID, bookingID)
	if err != nil {
		return TripView{}, err
	}
	canEarn := verified && isEarning(state) && service.photosOpen(state.EndsOn)
	view := buildTrip(bookingID, canEarn, verified, service.withExamples(ctx, plan), service.signedAll(ctx, submissions), refunds)
	view.EarnedCents += settlement.RefundedCents
	view.Money = moneyFor(plan, state.EndsOn, state.AmountCents, submissions, settlement)
	return view, nil
}

func moneyFor(plan chores.Plan, endsOn time.Time, paidCents int, submissions []Submission, settlement SettlementState) Money {
	accepted := []string{}
	for _, submission := range submissions {
		if submission.Status == TaskChecking || submission.Status == TaskApproved {
			accepted = append(accepted, submission.TaskKey)
		}
	}
	return Money{
		Schedule:         ScheduleFor(endsOn),
		CapCents:         CapCents(paidCents),
		ExpectedCents:    EarnableCents(plan, accepted, paidCents),
		SettlementStatus: settlement.Status,
	}
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
	settlement, err := service.store.Settlement(ctx, hostID, bookingID)
	if err != nil {
		return HostTripView{}, err
	}
	plan, err := service.bookingPlan(ctx, hostID, bookingID)
	if err != nil {
		return HostTripView{}, err
	}
	trip := buildTrip(bookingID, false, false, service.withExamples(ctx, plan), service.signedAll(ctx, submissions), refunds)
	return HostTripView{
		BookingID:    bookingID,
		GuestID:      booking.GuestID,
		ListingTitle: booking.ListingTitle,
		EarnedCents:  trip.EarnedCents + settlement.RefundedCents,
		Bundles:      trip.Bundles,
		Money:        moneyFor(plan, booking.EndsOn, booking.AmountCents, submissions, settlement),
	}, nil
}

func (service *CashbackService) PendingForHost(ctx context.Context, hostID uuid.UUID) ([]PendingSubmission, error) {
	earliestOpen := LatestSettleableEndsOn(service.now()).AddDate(0, 0, 1)
	submissions, err := service.store.ListPendingForHost(ctx, hostID, earliestOpen)
	if err != nil {
		return nil, err
	}
	submissions = service.signedAll(ctx, submissions)
	pending := make([]PendingSubmission, 0, len(submissions))
	plans := map[uuid.UUID]chores.Plan{}
	for _, submission := range submissions {
		plan, loaded := plans[submission.BookingID]
		if !loaded {
			plan, err = service.bookingPlan(ctx, hostID, submission.BookingID)
			if err != nil {
				return nil, err
			}
			plans[submission.BookingID] = plan
		}
		pending = append(pending, PendingSubmission{Submission: submission, TaskLabel: taskLabel(plan, submission.TaskKey)})
	}
	return pending, nil
}

func taskLabel(plan chores.Plan, taskKey string) string {
	if chore, ok := plan.Find(taskKey); ok {
		return chore.Label
	}
	return taskKey
}

func buildTrip(bookingID uuid.UUID, canEarn, verified bool, plan chores.Plan, submissions []Submission, refunds []Refund) TripView {
	byTask := map[string]Submission{}
	for _, submission := range submissions {
		byTask[submission.TaskKey] = submission
	}
	refundByBundle := map[string]Refund{}
	for _, refund := range refunds {
		refundByBundle[refund.BundleKey] = refund
	}

	view := TripView{BookingID: bookingID, Verified: verified, CanEarn: canEarn, Bundles: []BundleView{}}
	for _, group := range plan.Offered() {
		bundleView := BundleView{Key: group.Key, Label: group.Label, AmountCents: group.AmountCents}
		statuses := make([]TaskStatus, 0, len(group.Chores))
		for _, chore := range group.Chores {
			taskView := TaskView{Key: chore.Key, Label: chore.Label, Hint: chores.HintFor(chore), Instructions: chore.Note, ExampleURL: chore.PhotoURL, Status: TaskTodo}
			if submission, ok := byTask[chore.Key]; ok {
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
		if refund, ok := refundByBundle[group.Key]; ok {
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
	approved, checking, rejected := 0, 0, 0
	for _, status := range statuses {
		switch status {
		case TaskApproved:
			approved++
		case TaskChecking:
			checking++
		case TaskRejected, TaskRetake:
			rejected++
		}
	}
	switch {
	case approved == len(statuses):
		return TaskApproved
	case rejected > 0:
		return TaskRejected
	case checking > 0:
		return TaskChecking
	}
	return TaskTodo
}

func groupComplete(group chores.Group, approvedKeys []string) bool {
	approved := map[string]bool{}
	for _, key := range approvedKeys {
		approved[key] = true
	}
	for _, chore := range group.Chores {
		if !approved[chore.Key] {
			return false
		}
	}
	return true
}

func isEarning(state BookingState) bool {
	stayPaidFor := state.BookingStatus == "confirmed" || state.BookingStatus == "completed"
	return stayPaidFor && state.PaymentStatus == "succeeded" && state.PaymentIntentID != ""
}

func statusForDecision(decision Decision, note string) (TaskStatus, error) {
	switch decision {
	case DecisionApprove:
		return TaskApproved, nil
	case DecisionReject:
		trimmed := strings.TrimSpace(note)
		if trimmed == "" {
			return "", ErrRejectNeedsNote
		}
		if len(trimmed) > maxNoteLength {
			return "", fmt.Errorf("note must be %d characters or fewer", maxNoteLength)
		}
		return TaskRejected, nil
	}
	return "", ErrInvalidDecision
}

func choreKeyPrefix(listingID uuid.UUID) string {
	return "chores/" + listingID.String() + "/"
}

func validExampleKey(key string, listingID uuid.UUID) bool {
	prefix := choreKeyPrefix(listingID)
	return strings.HasPrefix(key, prefix) && photoKeyPattern.MatchString(strings.TrimPrefix(key, prefix))
}

func (service *CashbackService) withExamples(ctx context.Context, plan chores.Plan) chores.Plan {
	signed := make(chores.Plan, len(plan))
	for index, group := range plan {
		signed[index] = group
		signed[index].Chores = make([]chores.Chore, len(group.Chores))
		for position, chore := range group.Chores {
			if chore.PhotoKey != "" && service.objects != nil {
				if url, err := service.objects.PresignDownload(ctx, chore.PhotoKey, downloadURLLifetime); err == nil {
					chore.PhotoURL = url
				}
			}
			signed[index].Chores[position] = chore
		}
	}
	return signed
}

func (service *CashbackService) ListingPlan(ctx context.Context, hostID, listingID uuid.UUID) (PlanView, error) {
	plan, err := service.storedPlan(ctx, hostID, listingID)
	if err != nil {
		return PlanView{}, err
	}
	return PlanView{Groups: service.withExamples(ctx, plan)}, nil
}

func (service *CashbackService) storedPlan(ctx context.Context, hostID, listingID uuid.UUID) (chores.Plan, error) {
	raw, err := service.store.ListingPlan(ctx, hostID, listingID)
	if err != nil {
		return nil, err
	}
	return chores.PlanFromJSON(raw)
}

func (service *CashbackService) SaveListingPlan(ctx context.Context, hostID, listingID uuid.UUID, requested chores.Plan) (PlanView, error) {
	current, err := service.storedPlan(ctx, hostID, listingID)
	if err != nil {
		return PlanView{}, err
	}
	plan, err := service.cleanPlan(listingID, requested)
	if err != nil {
		return PlanView{}, err
	}
	if err := service.requireUploadedExamples(ctx, current, plan); err != nil {
		return PlanView{}, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return PlanView{}, fmt.Errorf("write chore plan: %w", err)
	}
	if err := service.store.SaveListingPlan(ctx, hostID, listingID, raw); err != nil {
		return PlanView{}, err
	}
	return service.ListingPlan(ctx, hostID, listingID)
}

func (service *CashbackService) cleanPlan(listingID uuid.UUID, requested chores.Plan) (chores.Plan, error) {
	byGroup := map[string]chores.Group{}
	for _, group := range requested {
		if !chores.KnownGroup(group.Key) {
			return nil, invalidPlan("unknown group %q", group.Key)
		}
		if _, repeated := byGroup[group.Key]; repeated {
			return nil, invalidPlan("%s is listed twice", chores.GroupLabel(group.Key))
		}
		byGroup[group.Key] = group
	}
	used := map[string]bool{}
	plan := chores.Plan{}
	for _, standard := range chores.DefaultPlan() {
		group := byGroup[standard.Key]
		if group.AmountCents < 0 || group.AmountCents > chores.MaxAmountCents {
			return nil, invalidPlan("%s cash back must be from $0 to $10,000", standard.Label)
		}
		if group.AmountCents > 0 && len(group.Chores) == 0 {
			return nil, invalidPlan("add a chore to %s or set its cash back to $0", standard.Label)
		}
		if len(group.Chores) > maxChoresPerGroup {
			return nil, invalidPlan("%s can have up to %d chores", standard.Label, maxChoresPerGroup)
		}
		clean := chores.Group{Key: standard.Key, AmountCents: group.AmountCents, Chores: []chores.Chore{}}
		for _, chore := range group.Chores {
			kept, err := service.cleanChore(listingID, chore)
			if err != nil {
				return nil, err
			}
			if used[kept.Key] {
				return nil, invalidPlan("%q is listed twice", kept.Label)
			}
			used[kept.Key] = true
			clean.Chores = append(clean.Chores, kept)
		}
		plan = append(plan, clean)
	}
	return plan, nil
}

func (service *CashbackService) cleanChore(listingID uuid.UUID, chore chores.Chore) (chores.Chore, error) {
	clean := chores.Chore{
		Key:      chore.Key,
		Label:    strings.TrimSpace(chore.Label),
		Note:     strings.TrimSpace(chore.Note),
		PhotoKey: chore.PhotoKey,
	}
	if clean.Label == "" || utf8.RuneCountInString(clean.Label) > maxChoreLabelLength {
		return chores.Chore{}, invalidPlan("name each chore in %d characters or fewer", maxChoreLabelLength)
	}
	if utf8.RuneCountInString(clean.Note) > maxNoteLength {
		return chores.Chore{}, invalidPlan("keep each note to %d characters or fewer", maxNoteLength)
	}
	if clean.Key == "" {
		clean.Key = customChoreKey(service.newID())
	}
	if !choreKeyPattern.MatchString(clean.Key) {
		return chores.Chore{}, invalidPlan("one chore could not be read")
	}
	if clean.PhotoKey != "" && !validExampleKey(clean.PhotoKey, listingID) {
		return chores.Chore{}, ErrInvalidPhotoKey
	}
	return clean, nil
}

func (service *CashbackService) requireUploadedExamples(ctx context.Context, current, next chores.Plan) error {
	known := map[string]bool{}
	for _, group := range current {
		for _, chore := range group.Chores {
			known[chore.PhotoKey] = true
		}
	}
	for _, group := range next {
		for _, chore := range group.Chores {
			if chore.PhotoKey == "" || known[chore.PhotoKey] {
				continue
			}
			if service.objects == nil {
				return ErrUploadsDisabled
			}
			info, err := service.objects.Stat(ctx, chore.PhotoKey)
			if err != nil {
				return ErrPhotoNotUploaded
			}
			if _, allowed := examplePhotoTypes[info.ContentType]; !allowed || info.Size <= 0 || info.Size > maxPhotoBytes {
				return ErrPhotoRejected
			}
		}
	}
	return nil
}

func (service *CashbackService) RequestExampleUpload(ctx context.Context, hostID, listingID uuid.UUID, contentType string, sizeBytes int64) (UploadTicket, error) {
	if service.objects == nil {
		return UploadTicket{}, ErrUploadsDisabled
	}
	extension, allowed := examplePhotoTypes[contentType]
	if !allowed || sizeBytes <= 0 || sizeBytes > maxPhotoBytes {
		return UploadTicket{}, ErrPhotoRejected
	}
	if _, err := service.store.ListingPlan(ctx, hostID, listingID); err != nil {
		return UploadTicket{}, err
	}
	key := choreKeyPrefix(listingID) + service.newID().String() + extension
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

func invalidPlan(format string, values ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPlan, fmt.Sprintf(format, values...))
}

func customChoreKey(id uuid.UUID) string {
	letters := make([]byte, customChoreLetters)
	for index := range letters {
		letters[index] = 'a' + id[index]%26
	}
	return customChorePrefix + string(letters)
}
