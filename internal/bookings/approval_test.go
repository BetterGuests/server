package bookings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type detailStub struct {
	detail     Detail
	decideErr  error
	cancelErr  error
	decidedTo  Status
	cancelled  bool
	getErr     error
	guestList  []Detail
	hostList   []Detail
	listActors []uuid.UUID

	overlapping []Detail
}

func (stub *detailStub) ListOverlapping(context.Context, uuid.UUID, time.Time, time.Time) ([]Detail, error) {
	return stub.overlapping, nil
}

func (stub *detailStub) Get(context.Context, uuid.UUID, uuid.UUID) (Detail, error) {
	return stub.detail, stub.getErr
}

func (stub *detailStub) ListForGuest(_ context.Context, guestID uuid.UUID) ([]Detail, error) {
	stub.listActors = append(stub.listActors, guestID)
	return stub.guestList, nil
}

func (stub *detailStub) ListForHost(_ context.Context, hostID uuid.UUID) ([]Detail, error) {
	stub.listActors = append(stub.listActors, hostID)
	return stub.hostList, nil
}

func (stub *detailStub) ListRequestsForHost(_ context.Context, hostID uuid.UUID) ([]Detail, error) {
	stub.listActors = append(stub.listActors, hostID)
	return stub.hostList, nil
}

func (stub *detailStub) Decide(_ context.Context, _, _ uuid.UUID, next Status, _ time.Time) error {
	if stub.decideErr == nil {
		stub.decidedTo = next
		stub.detail.Status = next
	}
	return stub.decideErr
}

func (stub *detailStub) CancelByGuest(context.Context, uuid.UUID, uuid.UUID) error {
	if stub.cancelErr == nil {
		stub.cancelled = true
		stub.detail.Status = StatusCancelled
	}
	return stub.cancelErr
}

type publishedEvent struct {
	userID  uuid.UUID
	kind    string
	payload any
}

type publisherStub struct{ events []publishedEvent }

func (stub *publisherStub) Publish(userID uuid.UUID, kind string, payload any) {
	stub.events = append(stub.events, publishedEvent{userID: userID, kind: kind, payload: payload})
}

func approvalFixture() (*BookingService, *detailStub, *publisherStub, Detail) {
	detail := Detail{
		Booking: Booking{ID: uuid.New(), GuestID: uuid.New(), Status: StatusRequested, StartsOn: time.Now().AddDate(0, 0, 7)},
		HostID:  uuid.New(),
		Role:    RoleHost,
	}
	details := &detailStub{detail: detail}
	publisher := &publisherStub{}
	service := newTestService(&storeStub{}).WithDetails(details).WithPublisher(publisher)
	return service, details, publisher, detail
}

func TestApproveMovesRequestToPayableAndTellsTheGuest(t *testing.T) {
	service, details, publisher, detail := approvalFixture()

	approved, err := service.Approve(context.Background(), detail.HostID, detail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if details.decidedTo != StatusPending || approved.Status != StatusPending {
		t.Fatalf("approve stored %q and returned %q, want %q", details.decidedTo, approved.Status, StatusPending)
	}
	if len(publisher.events) != 1 || publisher.events[0].userID != detail.GuestID || publisher.events[0].kind != BookingEventKind {
		t.Fatalf("expected one booking event for the guest, got %+v", publisher.events)
	}
}

func TestDeclineTellsTheGuest(t *testing.T) {
	service, details, publisher, detail := approvalFixture()

	if _, err := service.Decline(context.Background(), detail.HostID, detail.ID); err != nil {
		t.Fatal(err)
	}
	if details.decidedTo != StatusDeclined {
		t.Fatalf("decline stored %q", details.decidedTo)
	}
	event, ok := publisher.events[0].payload.(BookingEvent)
	if !ok || event.Status != StatusDeclined || event.BookingID != detail.ID {
		t.Fatalf("unexpected event payload %+v", publisher.events[0].payload)
	}
}

func TestDecisionOnAnAlreadyDecidedBookingPublishesNothing(t *testing.T) {
	service, details, publisher, detail := approvalFixture()
	details.decideErr = ErrNotAwaitingApproval

	_, err := service.Approve(context.Background(), detail.HostID, detail.ID)
	if !errors.Is(err, ErrNotAwaitingApproval) {
		t.Fatalf("expected ErrNotAwaitingApproval, got %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("a rejected decision must not notify anyone, got %+v", publisher.events)
	}
}

func TestCancelTellsTheHost(t *testing.T) {
	service, details, publisher, detail := approvalFixture()

	cancelled, err := service.Cancel(context.Background(), detail.GuestID, detail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !details.cancelled || cancelled.Status != StatusCancelled {
		t.Fatalf("cancel did not apply: %+v", cancelled)
	}
	if len(publisher.events) != 1 || publisher.events[0].userID != detail.HostID {
		t.Fatalf("expected one event for the host, got %+v", publisher.events)
	}
}

type readerStub struct{ *detailStub }

type responderStub struct {
	err    error
	detail Detail
}

func (stub responderStub) Approve(context.Context, uuid.UUID, uuid.UUID) (Detail, error) {
	return stub.detail, stub.err
}

func (stub responderStub) Decline(context.Context, uuid.UUID, uuid.UUID) (Detail, error) {
	return stub.detail, stub.err
}

func (stub responderStub) Cancel(context.Context, uuid.UUID, uuid.UUID) (Detail, error) {
	return stub.detail, stub.err
}

func bookingRequest(method, id string, authenticated bool) *http.Request {
	request := httptest.NewRequest(method, "/api/v1/bookings/"+id, nil)
	request.SetPathValue("booking_id", id)
	if authenticated {
		request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
	}
	return request
}

func TestApproveRouteStatusCodes(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		authenticated bool
		err           error
		want          int
	}{
		{"approved", uuid.NewString(), true, nil, http.StatusOK},
		{"not signed in", uuid.NewString(), false, nil, http.StatusUnauthorized},
		{"bad id", "nope", true, nil, http.StatusBadRequest},
		{"unknown booking", uuid.NewString(), true, ErrBookingNotFound, http.StatusNotFound},
		{"already decided", uuid.NewString(), true, ErrNotAwaitingApproval, http.StatusConflict},
		{"unexpected failure", uuid.NewString(), true, errors.New("boom"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(creatorStub{}).WithResponder(responderStub{err: test.err})
			response := httptest.NewRecorder()

			handler.ApproveBooking(response, bookingRequest(http.MethodPost, test.id, test.authenticated))

			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestCancelRouteReportsUncancellableBookingAsConflict(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{}).WithResponder(responderStub{err: ErrNotCancellable})
	response := httptest.NewRecorder()

	handler.CancelBooking(response, bookingRequest(http.MethodPost, uuid.NewString(), true))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestListRoutesReturnAnEmptyArrayNotNull(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{}).WithReader(readerStub{&detailStub{}})

	for name, serve := range map[string]http.HandlerFunc{
		"guest":         handler.ListMyBookings,
		"host":          handler.ListHostRequests,
		"host bookings": handler.ListHostBookings,
	} {
		response := httptest.NewRecorder()
		serve(response, bookingRequest(http.MethodGet, "", true))
		if response.Code != http.StatusOK {
			t.Fatalf("%s list status = %d", name, response.Code)
		}
		if body := response.Body.String(); body != "{\"bookings\":[]}\n" {
			t.Fatalf("%s list body = %q", name, body)
		}
	}
}

func TestGetBookingMapsMissingToNotFound(t *testing.T) {
	handler := NewHTTPHandler(creatorStub{}).WithReader(readerStub{&detailStub{getErr: ErrBookingNotFound}})
	response := httptest.NewRecorder()

	handler.GetBooking(response, bookingRequest(http.MethodGet, uuid.NewString(), true))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

type holdStub struct {
	captured   int
	released   int
	captureErr error
	releaseErr error
}

func (stub *holdStub) Capture(context.Context, uuid.UUID, uuid.UUID) error {
	stub.captured++
	return stub.captureErr
}

func (stub *holdStub) Release(context.Context, uuid.UUID, uuid.UUID) error {
	stub.released++
	return stub.releaseErr
}

func holdFixture(paymentStatus string) (*BookingService, *detailStub, *holdStub, Detail) {
	service, details, _, _ := approvalFixture()
	details.detail.PaymentStatus = paymentStatus
	hold := &holdStub{}
	return service.WithPayments(hold), details, hold, details.detail
}

func TestApproveCapturesTheHeldCard(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")

	if _, err := service.Approve(context.Background(), detail.HostID, detail.ID); err != nil {
		t.Fatal(err)
	}
	if details.decidedTo != StatusPending || hold.captured != 1 {
		t.Fatalf("decided = %q, captured = %d", details.decidedTo, hold.captured)
	}
}

func TestApproveRefusesAHostWithoutACurrentID(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")
	service.WithVerification(verificationStub{current: false})

	_, err := service.Approve(context.Background(), detail.HostID, detail.ID)

	if !errors.Is(err, ErrHostIDRequired) || details.decidedTo != "" || hold.captured != 0 {
		t.Fatalf("error = %v, decided = %q, captured = %d", err, details.decidedTo, hold.captured)
	}
}

func TestApproveRefusesARequestWithoutAHeldCard(t *testing.T) {
	for _, status := range []string{"", "pending", "canceled", "failed"} {
		service, details, hold, detail := holdFixture(status)

		_, err := service.Approve(context.Background(), detail.HostID, detail.ID)

		if !errors.Is(err, ErrPaymentNotAuthorized) {
			t.Fatalf("payment %q: expected ErrPaymentNotAuthorized, got %v", status, err)
		}
		if details.decidedTo != "" || hold.captured != 0 {
			t.Fatalf("payment %q: nothing may change without a held card", status)
		}
	}
}

func TestApproveCanBeRetriedAfterACaptureFailure(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")
	hold.captureErr = errors.New("stripe down")

	if _, err := service.Approve(context.Background(), detail.HostID, detail.ID); !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("expected ErrCaptureFailed, got %v", err)
	}
	if details.detail.Status != StatusPending {
		t.Fatalf("booking must stay approved for a retry, is %q", details.detail.Status)
	}

	hold.captureErr = nil
	if _, err := service.Approve(context.Background(), detail.HostID, detail.ID); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if hold.captured != 2 {
		t.Fatalf("captured %d times, want 2", hold.captured)
	}
}

func TestApproveByANonHostIsNotFound(t *testing.T) {
	service, details, _, detail := holdFixture("authorized")
	details.detail.Role = RoleGuest

	if _, err := service.Approve(context.Background(), detail.GuestID, detail.ID); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("expected ErrBookingNotFound, got %v", err)
	}
}

func TestDeclineAndCancelReleaseTheHeldCard(t *testing.T) {
	declining, _, declined, request := holdFixture("authorized")
	if _, err := declining.Decline(context.Background(), request.HostID, request.ID); err != nil {
		t.Fatal(err)
	}
	cancelling, _, cancelled, other := holdFixture("authorized")
	if _, err := cancelling.Cancel(context.Background(), other.GuestID, other.ID); err != nil {
		t.Fatal(err)
	}
	if declined.released != 1 || cancelled.released != 1 {
		t.Fatalf("released %d and %d times, want 1 each", declined.released, cancelled.released)
	}
}

func TestApproveRefusesAStayWhoseCheckInDateHasPassed(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")
	details.detail.StartsOn = time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)

	if _, err := service.Approve(context.Background(), detail.HostID, detail.ID); !errors.Is(err, ErrStayAlreadyStarted) {
		t.Fatalf("want already started, got %v", err)
	}
	if hold.captured != 0 || details.decidedTo != "" {
		t.Fatalf("a past stay was charged or approved: %+v", hold)
	}
}

func TestAChargedBookingCannotBeCancelled(t *testing.T) {
	service, details, hold, detail := holdFixture("succeeded")
	details.detail.Status = StatusPending

	if _, err := service.Cancel(context.Background(), detail.GuestID, detail.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("want not cancellable, got %v", err)
	}
	if details.cancelled || hold.released != 0 {
		t.Fatal("a paid booking was cancelled without a refund")
	}
}

func TestABookingStaysOpenWhenTheCardCannotBeReleased(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")
	hold.releaseErr = errors.New("already captured")

	if _, err := service.Cancel(context.Background(), detail.GuestID, detail.ID); !errors.Is(err, ErrReleaseFailed) {
		t.Fatalf("want release failed, got %v", err)
	}
	if details.cancelled {
		t.Fatal("cancelled while the guest may still be charged")
	}
}

func TestSomeoneElseCannotCancelOrReleaseABooking(t *testing.T) {
	service, details, hold, detail := holdFixture("authorized")

	if _, err := service.Cancel(context.Background(), detail.HostID, detail.ID); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if details.cancelled || hold.released != 0 {
		t.Fatal("the host released the guest's card through cancel")
	}
}

type suspendedGuests map[uuid.UUID]bool

func (banned suspendedGuests) IsBanned(_ context.Context, accountID uuid.UUID) (bool, error) {
	return banned[accountID], nil
}

func TestARequestFromABannedGuestCannotBeApprovedEvenOnARetry(t *testing.T) {
	for _, status := range []Status{StatusRequested, StatusPending} {
		service, details, publisher, detail := approvalFixture()
		details.detail.Status = status
		service.WithBans(suspendedGuests{detail.GuestID: true})

		_, err := service.Approve(context.Background(), detail.HostID, detail.ID)
		if !errors.Is(err, ErrGuestBanned) || details.decidedTo != "" || len(publisher.events) != 0 {
			t.Fatalf("%s: err=%v decided=%q events=%d", status, err, details.decidedTo, len(publisher.events))
		}
	}
}
