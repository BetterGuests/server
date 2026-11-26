package cancellations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
)

var (
	ErrBookingNotFound = errors.New("booking not found")
	ErrNotCancellable  = errors.New("this booking can no longer be cancelled here")
	ErrRefundChanged   = errors.New("the refund amount changed, so check it and confirm again")
	ErrReportClosed    = errors.New("problems can be reported from check-in until 24 hours after")
	ErrAlreadyReported = errors.New("you already reported a problem with this stay")
	ErrInvalidReport   = errors.New("invalid report")
	ErrNotAdmin        = errors.New("not found")
	ErrAlreadyDecided  = errors.New("this report was already decided")
	ErrListingNotFound = errors.New("listing not found")
	ErrAlreadyPaused   = errors.New("this listing is already paused")
	ErrNotPaused       = errors.New("this listing is not paused")
	ErrInvalidReason   = errors.New("give a reason of 1-200 characters")
)

type CancellationService struct {
	store     Store
	gateway   Gateway
	holds     HoldReleaser
	publisher Publisher
	photos    PhotoSigner
	now       func() time.Time
}

func NewCancellationService(store Store, gateway Gateway, holds HoldReleaser, publisher Publisher) *CancellationService {
	return &CancellationService{store: store, gateway: gateway, holds: holds, publisher: publisher, now: time.Now}
}

func (service *CancellationService) WithListingPhotos(photos PhotoSigner) *CancellationService {
	service.photos = photos
	return service
}

func (service *CancellationService) Options(ctx context.Context, actorID, bookingID uuid.UUID) (Options, error) {
	booking, err := service.store.Booking(ctx, actorID, bookingID)
	if err != nil {
		return Options{}, err
	}
	return optionsFor(booking, actorID, service.now().UTC())
}

func (service *CancellationService) Cancel(ctx context.Context, actorID, bookingID uuid.UUID, expectedRefundCents int) (Options, error) {
	booking, err := service.store.Booking(ctx, actorID, bookingID)
	if err != nil {
		return Options{}, err
	}
	now := service.now().UTC()
	options, err := optionsFor(booking, actorID, now)
	if err != nil {
		return Options{}, err
	}
	if !options.CanCancel {
		return Options{}, ErrNotCancellable
	}
	if options.RefundCents != expectedRefundCents {
		return Options{}, ErrRefundChanged
	}
	cancellation := Cancellation{
		BookingID:       booking.BookingID,
		GuestID:         booking.GuestID,
		HostID:          booking.HostID,
		ListingID:       booking.ListingID,
		CancelledBy:     ByGuest,
		AmountCents:     booking.AmountCents,
		RefundCents:     options.RefundCents,
		PaymentIntentID: booking.PaymentIntentID,
		RefundStatus:    RefundNone,
	}
	other := booking.HostID
	if options.Role == RoleHost {
		cancellation.CancelledBy = ByHost
		other = booking.GuestID
	}
	if cancellation.RefundCents > 0 {
		cancellation.RefundStatus = RefundPending
	}
	if err := service.store.Cancel(ctx, actorID, cancellation, now.Add(-HostCancellationPeriod)); err != nil {
		return Options{}, err
	}
	if cancellation.RefundCents > 0 {
		service.refund(ctx, PendingRefund{
			BookingID: booking.BookingID, GuestID: booking.GuestID, Source: SourceCancellation,
			PaymentIntentID: booking.PaymentIntentID, AmountCents: cancellation.RefundCents,
		})
	}
	service.publisher.Publish(other, bookingEventKind, BookingEvent{BookingID: bookingID, Status: statusCancelled})
	return service.Options(ctx, actorID, bookingID)
}

func (service *CancellationService) Report(ctx context.Context, guestID, bookingID uuid.UUID, details string) (Options, error) {
	details = strings.TrimSpace(details)
	length := utf8.RuneCountInString(details)
	if length == 0 || length > maxReportRunes || !utf8.ValidString(details) {
		return Options{}, fmt.Errorf("%w: describe the problem in 1-%d characters", ErrInvalidReport, maxReportRunes)
	}
	booking, err := service.store.Booking(ctx, guestID, bookingID)
	if err != nil {
		return Options{}, err
	}
	options, err := optionsFor(booking, guestID, service.now().UTC())
	if err != nil {
		return Options{}, err
	}
	if booking.Report != nil {
		return Options{}, ErrAlreadyReported
	}
	if !options.CanReport {
		return Options{}, ErrReportClosed
	}
	err = service.store.CreateReport(ctx, Report{
		BookingID: bookingID, GuestID: guestID, HostID: booking.HostID, ListingTitle: booking.ListingTitle,
		Details: details, AmountCents: booking.AmountCents, PaymentIntentID: booking.PaymentIntentID,
	})
	if err != nil {
		return Options{}, err
	}
	service.publisher.Publish(booking.HostID, bookingEventKind, BookingEvent{BookingID: bookingID, Status: booking.Status})
	return service.Options(ctx, guestID, bookingID)
}

func (service *CancellationService) OpenReports(ctx context.Context, adminID uuid.UUID) ([]Report, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	return service.store.OpenReports(ctx, adminID, maxListedReports)
}

func (service *CancellationService) Resolve(ctx context.Context, adminID, bookingID uuid.UUID, decision Decision) (Report, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return Report{}, err
	}
	decision.Note = strings.TrimSpace(decision.Note)
	if decision.RefundCents < 0 || utf8.RuneCountInString(decision.Note) > maxNoteRunes {
		return Report{}, fmt.Errorf("%w: check the refund and the note", ErrInvalidReport)
	}
	report, err := service.store.Resolve(ctx, adminID, bookingID, decision, service.now().UTC())
	if err != nil {
		return Report{}, err
	}
	if report.RefundCents > 0 {
		service.refund(ctx, PendingRefund{
			BookingID: report.BookingID, GuestID: report.GuestID, Source: SourceReport,
			PaymentIntentID: report.PaymentIntentID, AmountCents: report.RefundCents,
		})
	}
	service.publisher.Publish(report.GuestID, bookingEventKind, BookingEvent{BookingID: bookingID, Status: string(report.Status)})
	return report, nil
}

func (service *CancellationService) AdminListings(ctx context.Context, adminID uuid.UUID) ([]AdminListing, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	since := service.now().UTC().Add(-HostCancellationPeriod)
	listings, err := service.store.AdminListings(ctx, adminID, since, maxListedListings)
	if err != nil {
		return nil, err
	}
	for index := range listings {
		listings[index].CoverPhotoURL = service.signedCoverPhoto(ctx, listings[index].ListingID, listings[index].CoverPhotoKey)
	}
	return listings, nil
}

func (service *CancellationService) PopularListings(ctx context.Context, adminID uuid.UUID) ([]PopularListing, error) {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return nil, err
	}
	since := service.now().UTC().Add(-PopularityPeriod)
	listings, err := service.store.PopularListings(ctx, adminID, since, maxPopularListings)
	if err != nil {
		return nil, err
	}
	for index := range listings {
		listings[index].CoverPhotoURL = service.signedCoverPhoto(ctx, listings[index].ListingID, listings[index].CoverPhotoKey)
	}
	return listings, nil
}

func (service *CancellationService) signedCoverPhoto(ctx context.Context, listingID uuid.UUID, key string) string {
	if service.photos == nil || key == "" {
		return ""
	}
	url, err := service.photos.PresignDownload(ctx, key, coverPhotoURLLifetime)
	if err != nil {
		slog.Error("presign listing cover photo", "listing_id", listingID, "error", err)
		return ""
	}
	return url
}

func (service *CancellationService) PauseListing(ctx context.Context, adminID, listingID uuid.UUID, reason string) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if length := utf8.RuneCountInString(reason); length == 0 || length > maxPauseReasonRunes || !utf8.ValidString(reason) {
		return ErrInvalidReason
	}
	hostID, err := service.store.PauseListing(ctx, adminID, listingID, reason)
	if err != nil {
		return err
	}
	slog.Info("Listing paused by the team", "listing_id", listingID, "admin_id", adminID)
	service.publisher.Publish(hostID, listingEventKind, ListingEvent{ListingID: listingID, Paused: true})
	return nil
}

func (service *CancellationService) UnpauseListing(ctx context.Context, adminID, listingID uuid.UUID) error {
	if err := service.requireAdmin(ctx, adminID); err != nil {
		return err
	}
	hostID, err := service.store.UnpauseListing(ctx, adminID, listingID, service.now().UTC())
	if err != nil {
		return err
	}
	slog.Info("Listing pause lifted", "listing_id", listingID, "admin_id", adminID)
	service.publisher.Publish(hostID, listingEventKind, ListingEvent{ListingID: listingID, Paused: false})
	return nil
}

func (service *CancellationService) ExpireRequests(ctx context.Context) (int, error) {
	requests, err := service.store.ExpiredRequests(ctx, jobBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list expired requests: %w", err)
	}
	expired := 0
	var failures []error
	for _, request := range requests {
		if err := service.holds.Release(ctx, request.GuestID, request.BookingID); err != nil {
			failures = append(failures, fmt.Errorf("release hold for booking %s: %w", request.BookingID, err))
			continue
		}
		if err := service.store.Expire(ctx, request); err != nil {
			if !errors.Is(err, ErrNotCancellable) {
				failures = append(failures, fmt.Errorf("expire booking %s: %w", request.BookingID, err))
			}
			continue
		}
		expired++
	}
	return expired, errors.Join(failures...)
}

func (service *CancellationService) ReleaseStrandedHolds(ctx context.Context) (int, error) {
	stranded, err := service.store.StrandedHolds(ctx, jobBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list stranded holds: %w", err)
	}
	released := 0
	var failures []error
	for _, hold := range stranded {
		if err := service.holds.Release(ctx, hold.GuestID, hold.BookingID); err != nil {
			failures = append(failures, fmt.Errorf("release hold for booking %s: %w", hold.BookingID, err))
			continue
		}
		released++
	}
	return released, errors.Join(failures...)
}

func (service *CancellationService) SendPendingRefunds(ctx context.Context) (int, error) {
	refunds, err := service.store.PendingRefunds(ctx, jobBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list pending refunds: %w", err)
	}
	sent := 0
	for _, refund := range refunds {
		if service.refund(ctx, refund) {
			sent++
		}
	}
	return sent, nil
}

func (service *CancellationService) refund(ctx context.Context, refund PendingRefund) bool {
	refundID, err := service.gateway.Refund(ctx, refund)
	if err == nil {
		err = service.store.MarkRefunded(ctx, refund, refundID)
	}
	if err != nil {
		slog.Error("Refund booking", "booking_id", refund.BookingID, "source", refund.Source, "error", err)
		return false
	}
	return true
}

func (service *CancellationService) requireAdmin(ctx context.Context, accountID uuid.UUID) error {
	admin, err := service.store.IsAdmin(ctx, accountID)
	if err != nil {
		return err
	}
	if !admin {
		return ErrNotAdmin
	}
	return nil
}

func optionsFor(booking Booking, actorID uuid.UUID, now time.Time) (Options, error) {
	checkInAt, err := stays.Moment(booking.StartsOn, booking.CheckInTime, booking.TimeZone)
	if err != nil {
		return Options{}, err
	}
	options := Options{
		BookingID:    booking.BookingID,
		Role:         RoleGuest,
		AmountCents:  booking.AmountCents,
		Currency:     booking.Currency,
		Cancellation: booking.Cancellation,
		Report:       booking.Report,
	}
	if booking.HostID == actorID {
		options.Role = RoleHost
	}
	paid := booking.PaymentStatus == paymentSucceeded && booking.AmountCents > 0
	if booking.Status == statusConfirmed && paid && now.Before(checkInAt) {
		options.CanCancel = true
		options.RefundCents = booking.AmountCents
		if options.Role == RoleGuest {
			options.RefundCents, options.FreeUntil = guestRefund(booking, checkInAt, now)
		}
	}
	closes := checkInAt.Add(ReportWindow)
	underway := booking.Status == statusConfirmed || booking.Status == statusCompleted
	if options.Role == RoleGuest && underway && paid && booking.Report == nil && !now.Before(checkInAt) && now.Before(closes) {
		options.CanReport = true
		options.ReportClosesAt = &closes
	}
	return options, nil
}

func guestRefund(booking Booking, checkInAt, now time.Time) (int, *time.Time) {
	freeUntil := checkInAt.Add(-FullRefundNotice)
	if booking.ApprovedAt != nil {
		grace := booking.ApprovedAt.Add(FreeAfterApproval)
		if grace.After(checkInAt) {
			grace = checkInAt
		}
		if grace.After(freeUntil) {
			freeUntil = grace
		}
	}
	if now.Before(freeUntil) {
		return booking.AmountCents, &freeUntil
	}
	return booking.AmountCents - booking.AmountCents/lateRefundDivisor, nil
}
