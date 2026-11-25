package stays

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stayStoreStub struct {
	record    Record
	completed int
}

func (stub *stayStoreStub) Stay(context.Context, uuid.UUID, uuid.UUID) (Record, error) {
	return stub.record, nil
}

func (stub *stayStoreStub) CheckIn(_ context.Context, _, _ uuid.UUID, at time.Time) error {
	stub.record.CheckedInAt = &at
	return nil
}

func (stub *stayStoreStub) CheckOut(_ context.Context, _, _ uuid.UUID, at time.Time) error {
	stub.record.CheckedOutAt = &at
	return nil
}

func (stub *stayStoreStub) CompleteFinished(context.Context, int) (int, error) {
	stub.completed++
	return 0, nil
}

func phoenixStay() *stayStoreStub {
	return &stayStoreStub{record: Record{
		BookingID: uuid.New(), GuestID: uuid.New(), HostID: uuid.New(), Status: statusConfirmed,
		StartsOn: time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC),
		EndsOn:   time.Date(2026, 12, 13, 0, 0, 0, 0, time.UTC),
		Schedule: Schedule{CheckInTime: "16:00", CheckOutTime: "11:00", TimeZone: "America/Phoenix"},
		Arrival:  &ArrivalDetails{EntryInstructions: "Blue door", EntryCode: "4821"},
	}}
}

func serviceAt(store *stayStoreStub, now time.Time) *StayService {
	service := NewStayService(store, nil)
	service.now = func() time.Time { return now }
	return service
}

var checkInUTC = time.Date(2026, 12, 10, 23, 0, 0, 0, time.UTC)
var checkOutUTC = time.Date(2026, 12, 13, 18, 0, 0, 0, time.UTC)

func TestMomentUsesTheListingsTimeZoneAcrossDaylightSaving(t *testing.T) {
	cases := map[string]struct {
		date time.Time
		want int
	}{
		"winter": {time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC), 21},
		"summer": {time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), 20},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			moment, err := Moment(test.date, "16:00", "America/New_York")
			if err != nil {
				t.Fatal(err)
			}
			if moment.Hour() != test.want {
				t.Fatalf("hour = %d, want %d", moment.Hour(), test.want)
			}
		})
	}
}

func TestGuestStayThroughItsPhases(t *testing.T) {
	cases := map[string]struct {
		now         time.Time
		checkedIn   bool
		phase       Phase
		seesCode    bool
		canCheckIn  bool
		canCheckOut bool
	}{
		"a week out":               {now: checkInUTC.Add(-7 * 24 * time.Hour), phase: PhaseUpcoming},
		"one second before reveal": {now: checkInUTC.Add(-ArrivalLeadTime - time.Second), phase: PhaseUpcoming},
		"reveal opens":             {now: checkInUTC.Add(-ArrivalLeadTime), phase: PhaseArrivalReady, seesCode: true},
		"check-in time":            {now: checkInUTC, phase: PhaseCheckInOpen, seesCode: true, canCheckIn: true},
		"in the house":             {now: checkInUTC.Add(time.Hour), checkedIn: true, phase: PhaseCheckedIn, seesCode: true, canCheckOut: true},
		"checkout time passed":     {now: checkOutUTC, checkedIn: true, phase: PhaseCheckedOut},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store := phoenixStay()
			if test.checkedIn {
				at := checkInUTC
				store.record.CheckedInAt = &at
			}
			view, err := serviceAt(store, test.now).Stay(context.Background(), store.record.GuestID, store.record.BookingID)
			if err != nil {
				t.Fatal(err)
			}
			if view.Phase != test.phase || (view.Arrival != nil) != test.seesCode ||
				view.CanCheckIn != test.canCheckIn || view.CanCheckOut != test.canCheckOut {
				t.Fatalf("view = %+v", view)
			}
		})
	}
}

func TestUnconfirmedAndCancelledStaysNeverShowTheEntryCode(t *testing.T) {
	for _, status := range []string{"requested", "pending", "declined", "cancelled", "completed"} {
		store := phoenixStay()
		store.record.Status = status
		view, err := serviceAt(store, checkInUTC).Stay(context.Background(), store.record.GuestID, store.record.BookingID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Arrival != nil || view.CanCheckIn {
			t.Fatalf("%s stay exposed arrival details or check-in: %+v", status, view)
		}
	}
}

func TestCheckInAndOut(t *testing.T) {
	store := phoenixStay()
	service := serviceAt(store, checkInUTC.Add(time.Minute))
	guest, booking := store.record.GuestID, store.record.BookingID

	if _, err := service.CheckOut(context.Background(), guest, booking); !errors.Is(err, ErrCheckOutClosed) {
		t.Fatalf("checked out before checking in: %v", err)
	}
	if view, err := service.CheckIn(context.Background(), guest, booking); err != nil || view.Phase != PhaseCheckedIn {
		t.Fatalf("check in: %+v %v", view, err)
	}
	if _, err := service.CheckIn(context.Background(), guest, booking); err != nil {
		t.Fatalf("a second tap on check in must succeed quietly: %v", err)
	}
	view, err := service.CheckOut(context.Background(), guest, booking)
	if err != nil || view.Phase != PhaseCheckedOut || store.completed != 1 {
		t.Fatalf("check out: %+v %v", view, err)
	}
}

func TestOnlyTheGuestChecksIn(t *testing.T) {
	store := phoenixStay()
	_, err := serviceAt(store, checkInUTC).CheckIn(context.Background(), store.record.HostID, store.record.BookingID)
	if !errors.Is(err, ErrGuestOnly) {
		t.Fatalf("want guest only, got %v", err)
	}
}

func TestArrivalGuideValidation(t *testing.T) {
	valid := GuideInput{
		Schedule:       Schedule{CheckInTime: "15:00", CheckOutTime: "10:00", TimeZone: "America/Denver"},
		ArrivalDetails: ArrivalDetails{EntryInstructions: "Side gate"},
	}
	if _, err := cleanGuide(valid); err != nil {
		t.Fatal(err)
	}
	broken := map[string]func(*GuideInput){
		"unpadded time":            func(g *GuideInput) { g.CheckInTime = "9:00" },
		"not a time":               func(g *GuideInput) { g.CheckOutTime = "noon" },
		"unknown zone":             func(g *GuideInput) { g.TimeZone = "Europe/Paris" },
		"checkout after check-in":  func(g *GuideInput) { g.CheckOutTime = "16:00" },
		"checkout equals check-in": func(g *GuideInput) { g.CheckOutTime = g.CheckInTime },
		"no instructions":          func(g *GuideInput) { g.EntryInstructions = "  " },
		"code is too long":         func(g *GuideInput) { g.EntryCode = string(make([]byte, 61)) },
	}
	for name, change := range broken {
		t.Run(name, func(t *testing.T) {
			input := valid
			change(&input)
			if _, err := cleanGuide(input); !errors.Is(err, ErrInvalidGuide) {
				t.Fatalf("want invalid guide, got %v", err)
			}
		})
	}
}

func TestTheEntryCodeHidesOnceTheGuestChecksOut(t *testing.T) {
	store := phoenixStay()
	in, out := checkInUTC, checkInUTC.Add(time.Hour)
	store.record.CheckedInAt, store.record.CheckedOutAt = &in, &out
	view, err := serviceAt(store, out.Add(time.Minute)).Stay(context.Background(), store.record.GuestID, store.record.BookingID)
	if err != nil || view.Arrival != nil {
		t.Fatalf("code still visible after check-out: %+v %v", view, err)
	}
}
