package cancellations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var checkIn = time.Date(2026, 12, 20, 23, 0, 0, 0, time.UTC)

func paidBooking(approvedAt time.Time) Booking {
	return Booking{
		BookingID: uuid.New(), GuestID: uuid.New(), HostID: uuid.New(), Status: statusConfirmed,
		StartsOn: time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC), CheckInTime: "16:00", TimeZone: "America/Phoenix",
		ApprovedAt: &approvedAt, AmountCents: 30001, PaymentStatus: paymentSucceeded, PaymentIntentID: "pi_1",
	}
}

func TestWhatAGuestGetsBack(t *testing.T) {
	longAgo := checkIn.Add(-30 * 24 * time.Hour)
	cases := map[string]struct {
		approved, now time.Time
		cancel        bool
		refund        int
	}{
		"more than five days out":             {longAgo, checkIn.Add(-FullRefundNotice - time.Minute), true, 30001},
		"exactly five days out is late":       {longAgo, checkIn.Add(-FullRefundNotice), true, 15001},
		"the odd cent goes to the guest":      {longAgo, checkIn.Add(-time.Hour), true, 15001},
		"next day booking, within four hours": {checkIn.Add(-20 * time.Hour), checkIn.Add(-17 * time.Hour), true, 30001},
		"next day booking, after four hours":  {checkIn.Add(-20 * time.Hour), checkIn.Add(-16 * time.Hour), true, 15001},
		"free window ends at check-in":        {checkIn.Add(-time.Hour), checkIn, false, 0},
		"after check-in":                      {longAgo, checkIn.Add(time.Hour), false, 0},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			booking := paidBooking(test.approved)
			options, err := optionsFor(booking, booking.GuestID, test.now)
			if err != nil {
				t.Fatal(err)
			}
			if options.CanCancel != test.cancel || options.RefundCents != test.refund {
				t.Fatalf("options = %+v", options)
			}
		})
	}
}

func TestAHostCancellationAlwaysRefundsEverythingAndOnlyBeforeCheckIn(t *testing.T) {
	booking := paidBooking(checkIn.Add(-30 * 24 * time.Hour))
	before, err := optionsFor(booking, booking.HostID, checkIn.Add(-time.Minute))
	if err != nil || !before.CanCancel || before.RefundCents != booking.AmountCents {
		t.Fatalf("before = %+v %v", before, err)
	}
	after, err := optionsFor(booking, booking.HostID, checkIn)
	if err != nil || after.CanCancel || after.CanReport {
		t.Fatalf("after = %+v %v", after, err)
	}
}

func TestProblemsCanBeReportedForADayAfterCheckIn(t *testing.T) {
	booking := paidBooking(checkIn.Add(-30 * 24 * time.Hour))
	for name, test := range map[string]struct {
		now  time.Time
		open bool
	}{
		"before check-in": {checkIn.Add(-time.Second), false},
		"at check-in":     {checkIn, true},
		"last minute":     {checkIn.Add(ReportWindow - time.Second), true},
		"a day later":     {checkIn.Add(ReportWindow), false},
	} {
		t.Run(name, func(t *testing.T) {
			options, err := optionsFor(booking, booking.GuestID, test.now)
			if err != nil || options.CanReport != test.open {
				t.Fatalf("options = %+v %v", options, err)
			}
		})
	}
}

type storeStub struct {
	Store
	booking   Booking
	cancelled []Cancellation
	expired   []ExpiredRequest
	marked    []PendingRefund
	stranded  []StrandedHold
}

func (stub *storeStub) StrandedHolds(context.Context, int) ([]StrandedHold, error) {
	return stub.stranded, nil
}

func (stub *storeStub) Booking(context.Context, uuid.UUID, uuid.UUID) (Booking, error) {
	return stub.booking, nil
}

func (stub *storeStub) Cancel(_ context.Context, _ uuid.UUID, cancellation Cancellation, _ time.Time) error {
	stub.cancelled = append(stub.cancelled, cancellation)
	return nil
}

func (stub *storeStub) ExpiredRequests(context.Context, int) ([]ExpiredRequest, error) {
	return []ExpiredRequest{{BookingID: uuid.New()}}, nil
}

func (stub *storeStub) Expire(_ context.Context, request ExpiredRequest) error {
	stub.expired = append(stub.expired, request)
	return nil
}

func (stub *storeStub) MarkRefunded(_ context.Context, refund PendingRefund, _ string) error {
	stub.marked = append(stub.marked, refund)
	return nil
}

type gatewayStub struct{ err error }

func (stub gatewayStub) Refund(context.Context, PendingRefund) (string, error) {
	return "re_1", stub.err
}

type holdStub struct{ err error }

func (stub holdStub) Release(context.Context, uuid.UUID, uuid.UUID) error { return stub.err }

type silent struct{}

func (silent) Publish(uuid.UUID, string, any) {}

func serviceWith(store *storeStub, gateway Gateway, holds HoldReleaser) *CancellationService {
	service := NewCancellationService(store, gateway, holds, silent{})
	service.now = func() time.Time { return checkIn.Add(-24 * time.Hour) }
	return service
}

func TestTheGuestConfirmsTheAmountTheyWereShown(t *testing.T) {
	store := &storeStub{booking: paidBooking(checkIn.Add(-30 * 24 * time.Hour))}
	service := serviceWith(store, gatewayStub{}, holdStub{})

	_, err := service.Cancel(context.Background(), store.booking.GuestID, store.booking.BookingID, store.booking.AmountCents)
	if !errors.Is(err, ErrRefundChanged) || len(store.cancelled) != 0 {
		t.Fatalf("cancelled on a stale amount: %v", err)
	}
	if _, err := service.Cancel(context.Background(), store.booking.GuestID, store.booking.BookingID, 15001); err != nil {
		t.Fatal(err)
	}
	if len(store.cancelled) != 1 || store.cancelled[0].CancelledBy != ByGuest || len(store.marked) != 1 {
		t.Fatalf("cancelled = %+v marked = %+v", store.cancelled, store.marked)
	}
}

func TestAFailedRefundStaysPendingForTheJob(t *testing.T) {
	store := &storeStub{booking: paidBooking(checkIn.Add(-30 * 24 * time.Hour))}
	service := serviceWith(store, gatewayStub{err: errors.New("stripe down")}, holdStub{})
	if _, err := service.Cancel(context.Background(), store.booking.HostID, store.booking.BookingID, store.booking.AmountCents); err != nil {
		t.Fatal(err)
	}
	if len(store.marked) != 0 || store.cancelled[0].RefundStatus != RefundPending {
		t.Fatalf("refund recorded despite failing: %+v", store.marked)
	}
}

func TestARequestDoesNotExpireWhileItsCardCannotBeReleased(t *testing.T) {
	store := &storeStub{}
	if _, err := serviceWith(store, gatewayStub{}, holdStub{err: errors.New("already captured")}).ExpireRequests(context.Background()); err == nil {
		t.Fatal("expected the failure to be reported")
	}
	if len(store.expired) != 0 {
		t.Fatal("expired a booking that may still be charged")
	}
}

type releaseLog struct {
	failing  uuid.UUID
	released []StrandedHold
}

func (log *releaseLog) Release(_ context.Context, guestID, bookingID uuid.UUID) error {
	if bookingID == log.failing {
		return errors.New("stripe down")
	}
	log.released = append(log.released, StrandedHold{BookingID: bookingID, GuestID: guestID})
	return nil
}

func TestStrandedHoldsAreReleasedAsTheGuestEvenWhenOneFails(t *testing.T) {
	first := StrandedHold{BookingID: uuid.New(), GuestID: uuid.New()}
	second := StrandedHold{BookingID: uuid.New(), GuestID: uuid.New()}
	holds := &releaseLog{failing: first.BookingID}

	released, err := serviceWith(&storeStub{stranded: []StrandedHold{first, second}}, gatewayStub{}, holds).ReleaseStrandedHolds(context.Background())

	if err == nil || released != 1 || len(holds.released) != 1 || holds.released[0] != second {
		t.Fatalf("released = %d %+v, err = %v", released, holds.released, err)
	}
}

func TestPopularCitiesTrimLowercaseAndDropRepeats(t *testing.T) {
	got := PopularCities([]string{" Phoenix ", "phoenix", "", "  ", "Tempe", strings.Repeat("a", maxPopularCityRunes+1)})
	if want := []string{"phoenix", "tempe"}; !slices.Equal(got, want) {
		t.Fatalf("cities = %v, want %v", got, want)
	}
}

func TestPopularCitiesStopAtTheCap(t *testing.T) {
	many := make([]string, maxPopularCities+5)
	for index := range many {
		many[index] = uuid.NewString()
	}
	if got := PopularCities(many); len(got) != maxPopularCities {
		t.Fatalf("kept %d cities, want %d", len(got), maxPopularCities)
	}
}
