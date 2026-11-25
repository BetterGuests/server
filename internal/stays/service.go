package stays

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrStayNotFound     = errors.New("stay not found")
	ErrListingNotFound  = errors.New("listing not found")
	ErrInvalidGuide     = errors.New("invalid arrival guide")
	ErrGuestOnly        = errors.New("only the guest can do that")
	ErrCheckInClosed    = errors.New("check-in is not open for this stay")
	ErrCheckOutClosed   = errors.New("check-out is not open for this stay")
	ErrScheduleLocked   = errors.New("a guest is staying, so check-in times and time zone cannot change until they leave")
	supportedTimeZones  = []string{"America/New_York", "America/Chicago", "America/Denver", "America/Phoenix", "America/Los_Angeles", "America/Anchorage", "Pacific/Honolulu"}
	errUnknownTimeOfDay = errors.New("time must look like 16:00")
)

type StayService struct {
	store  Store
	guides GuideStore
	nudger Nudger
	now    func() time.Time
}

func NewStayService(store Store, guides GuideStore) *StayService {
	return &StayService{store: store, guides: guides, now: time.Now}
}

func (service *StayService) WithNudger(nudger Nudger) *StayService {
	service.nudger = nudger
	return service
}

func Moment(date time.Time, timeOfDay, timeZone string) (time.Time, error) {
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load time zone %q: %w", timeZone, err)
	}
	clock, err := time.Parse(timeOfDayLayout, timeOfDay)
	if err != nil {
		return time.Time{}, errUnknownTimeOfDay
	}
	return time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, location).UTC(), nil
}

func (service *StayService) Stay(ctx context.Context, actorID, bookingID uuid.UUID) (View, error) {
	record, err := service.store.Stay(ctx, actorID, bookingID)
	if err != nil {
		return View{}, err
	}
	return viewOf(record, actorID, service.now().UTC())
}

func (service *StayService) CheckIn(ctx context.Context, actorID, bookingID uuid.UUID) (View, error) {
	view, err := service.Stay(ctx, actorID, bookingID)
	if err != nil {
		return View{}, err
	}
	if view.Role != RoleGuest {
		return View{}, ErrGuestOnly
	}
	if view.CheckedInAt != nil {
		return view, nil
	}
	if !view.CanCheckIn {
		return View{}, ErrCheckInClosed
	}
	if err := service.store.CheckIn(ctx, actorID, bookingID, service.now().UTC()); err != nil {
		return View{}, err
	}
	service.nudge()
	return service.Stay(ctx, actorID, bookingID)
}

func (service *StayService) CheckOut(ctx context.Context, actorID, bookingID uuid.UUID) (View, error) {
	view, err := service.Stay(ctx, actorID, bookingID)
	if err != nil {
		return View{}, err
	}
	if view.Role != RoleGuest {
		return View{}, ErrGuestOnly
	}
	if view.CheckedOutAt != nil {
		return view, nil
	}
	if !view.CanCheckOut {
		return View{}, ErrCheckOutClosed
	}
	if err := service.store.CheckOut(ctx, actorID, bookingID, service.now().UTC()); err != nil {
		return View{}, err
	}
	if _, err := service.CompleteFinished(ctx); err != nil {
		return View{}, err
	}
	service.nudge()
	return service.Stay(ctx, actorID, bookingID)
}

func (service *StayService) CompleteFinished(ctx context.Context) (int, error) {
	total := 0
	for range completionBatchAttempts {
		completed, err := service.store.CompleteFinished(ctx, completionBatchSize)
		if err != nil {
			return total, fmt.Errorf("complete finished stays: %w", err)
		}
		total += completed
		if completed < completionBatchSize {
			break
		}
	}
	return total, nil
}

func (service *StayService) Guide(ctx context.Context, hostID, listingID uuid.UUID) (ArrivalGuide, error) {
	return service.guides.Guide(ctx, hostID, listingID)
}

func (service *StayService) SaveGuide(ctx context.Context, hostID, listingID uuid.UUID, input GuideInput) (ArrivalGuide, error) {
	input, err := cleanGuide(input)
	if err != nil {
		return ArrivalGuide{}, err
	}
	if err := service.guides.SaveGuide(ctx, hostID, listingID, input, service.now().UTC()); err != nil {
		return ArrivalGuide{}, err
	}
	service.nudge()
	return service.guides.Guide(ctx, hostID, listingID)
}

func (service *StayService) nudge() {
	if service.nudger != nil {
		service.nudger.Nudge()
	}
}

func viewOf(record Record, actorID uuid.UUID, now time.Time) (View, error) {
	checkInAt, err := Moment(record.StartsOn, record.Schedule.CheckInTime, record.Schedule.TimeZone)
	if err != nil {
		return View{}, err
	}
	checkOutAt, err := Moment(record.EndsOn, record.Schedule.CheckOutTime, record.Schedule.TimeZone)
	if err != nil {
		return View{}, err
	}
	view := View{
		BookingID:      record.BookingID,
		ListingID:      record.ListingID,
		ListingTitle:   record.ListingTitle,
		Role:           RoleGuest,
		StartsOn:       record.StartsOn.Format(time.DateOnly),
		EndsOn:         record.EndsOn.Format(time.DateOnly),
		CheckInAt:      checkInAt,
		CheckOutAt:     checkOutAt,
		ArrivalOpensAt: checkInAt.Add(-ArrivalLeadTime),
		Schedule:       record.Schedule,
		CheckedInAt:    record.CheckedInAt,
		CheckedOutAt:   record.CheckedOutAt,
	}
	if record.HostID == actorID {
		view.Role = RoleHost
	}
	view.Phase = phaseOf(record, view, now)
	isGuest := view.Role == RoleGuest
	view.CanCheckIn = isGuest && view.Phase == PhaseCheckInOpen
	view.CanCheckOut = isGuest && view.Phase == PhaseCheckedIn
	if !isGuest || arrivalVisible(record, view, now) {
		view.Arrival = record.Arrival
	}
	return view, nil
}

func phaseOf(record Record, view View, now time.Time) Phase {
	switch record.Status {
	case statusRequested, statusPending:
		return PhaseAwaitingConfirmation
	case statusCompleted:
		return PhaseCompleted
	case statusConfirmed:
	default:
		return PhaseCancelled
	}
	switch {
	case record.CheckedOutAt != nil || !now.Before(view.CheckOutAt):
		return PhaseCheckedOut
	case record.CheckedInAt != nil:
		return PhaseCheckedIn
	case !now.Before(view.CheckInAt):
		return PhaseCheckInOpen
	case !now.Before(view.ArrivalOpensAt):
		return PhaseArrivalReady
	}
	return PhaseUpcoming
}

func arrivalVisible(record Record, view View, now time.Time) bool {
	return record.Status == statusConfirmed &&
		record.CheckedOutAt == nil &&
		!now.Before(view.ArrivalOpensAt) &&
		now.Before(view.CheckOutAt)
}

func cleanGuide(input GuideInput) (GuideInput, error) {
	input.CheckInTime = strings.TrimSpace(input.CheckInTime)
	input.CheckOutTime = strings.TrimSpace(input.CheckOutTime)
	for _, value := range []string{input.CheckInTime, input.CheckOutTime} {
		if parsed, err := time.Parse(timeOfDayLayout, value); err != nil || parsed.Format(timeOfDayLayout) != value {
			return input, fmt.Errorf("%w: check-in and check-out times must look like 16:00", ErrInvalidGuide)
		}
	}
	if input.CheckOutTime >= input.CheckInTime {
		return input, fmt.Errorf("%w: checkout must be earlier in the day than check-in, so one guest leaves before the next arrives", ErrInvalidGuide)
	}
	if !slices.Contains(supportedTimeZones, input.TimeZone) {
		return input, fmt.Errorf("%w: choose a US time zone", ErrInvalidGuide)
	}
	fields := []struct {
		value *string
		label string
		limit int
	}{
		{&input.EntryInstructions, "entry instructions", maxInstructionRunes},
		{&input.EntryCode, "entry code", maxEntryCodeRunes},
		{&input.WifiName, "Wi-Fi name", maxWifiRunes},
		{&input.WifiPassword, "Wi-Fi password", maxWifiRunes},
		{&input.CheckoutInstructions, "checkout instructions", maxCheckoutRunes},
	}
	for _, field := range fields {
		*field.value = strings.TrimSpace(*field.value)
		if !utf8.ValidString(*field.value) || utf8.RuneCountInString(*field.value) > field.limit {
			return input, fmt.Errorf("%w: %s must be at most %d characters", ErrInvalidGuide, field.label, field.limit)
		}
	}
	if input.EntryInstructions == "" {
		return input, fmt.Errorf("%w: tell your guest how to get in", ErrInvalidGuide)
	}
	return input, nil
}
