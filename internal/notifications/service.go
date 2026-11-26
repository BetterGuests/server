package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/reviews"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
)

var ErrNotificationNotFound = errors.New("notification not found")

type NotificationService struct {
	store InboxStore
	now   func() time.Time
}

func NewNotificationService(store InboxStore) *NotificationService {
	return &NotificationService{store: store, now: time.Now}
}

func (service *NotificationService) List(ctx context.Context, actorID uuid.UUID) (Inbox, error) {
	return service.store.List(ctx, actorID, maxListed)
}

func (service *NotificationService) MarkRead(ctx context.Context, actorID, notificationID uuid.UUID) error {
	return service.store.MarkRead(ctx, actorID, notificationID, service.now().UTC())
}

func (service *NotificationService) MarkAllRead(ctx context.Context, actorID uuid.UUID) error {
	return service.store.MarkAllRead(ctx, actorID, service.now().UTC())
}

type JourneyService struct {
	store     JourneyStore
	stays     StayCompleter
	reviews   ReviewPublisher
	publisher Publisher
	upkeep    BookingUpkeep
	team      TeamAlertQueue
	mail      OutboundMail
	nudges    chan struct{}
	now       func() time.Time
	newID     func() uuid.UUID
}

func NewJourneyService(store JourneyStore, publisher Publisher) *JourneyService {
	return &JourneyService{
		store:     store,
		publisher: publisher,
		nudges:    make(chan struct{}, 1),
		now:       time.Now,
		newID:     uuid.New,
	}
}

func (service *JourneyService) WithLifecycle(stays StayCompleter, reviews ReviewPublisher) *JourneyService {
	service.stays = stays
	service.reviews = reviews
	return service
}

func (service *JourneyService) WithUpkeep(upkeep BookingUpkeep) *JourneyService {
	service.upkeep = upkeep
	return service
}

func (service *JourneyService) WithTeamAlerts(team TeamAlertQueue) *JourneyService {
	service.team = team
	return service
}

func (service *JourneyService) WithMail(mail OutboundMail) *JourneyService {
	service.mail = mail
	return service
}

func (service *JourneyService) Nudge() {
	select {
	case service.nudges <- struct{}{}:
	default:
	}
}

func (service *JourneyService) Run(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		sweep, cancel := context.WithTimeout(ctx, sweepTimeout)
		if err := service.Sweep(sweep); err != nil && ctx.Err() == nil {
			slog.Error("Trip updates", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-service.nudges:
		}
	}
}

func (service *JourneyService) Sweep(ctx context.Context) error {
	var failures []error
	if service.upkeep != nil {
		if _, err := service.upkeep.ExpireRequests(ctx); err != nil {
			failures = append(failures, err)
		}
		if _, err := service.upkeep.ReleaseStrandedHolds(ctx); err != nil {
			failures = append(failures, err)
		}
		if _, err := service.upkeep.SendPendingRefunds(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if service.stays != nil {
		if _, err := service.stays.CompleteFinished(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if service.reviews != nil {
		if _, err := service.reviews.PublishDue(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	after := uuid.Nil
	for {
		journeys, err := service.store.Journeys(ctx, after, journeyPageSize)
		if err != nil {
			return errors.Join(append(failures, fmt.Errorf("list journeys: %w", err))...)
		}
		for _, journey := range journeys {
			if err := service.advance(ctx, journey); err != nil {
				failures = append(failures, fmt.Errorf("booking %s: %w", journey.BookingID, err))
			}
		}
		if len(journeys) < journeyPageSize {
			return errors.Join(append(failures, service.announce(ctx)...)...)
		}
		after = journeys[len(journeys)-1].BookingID
	}
}

func (service *JourneyService) announce(ctx context.Context) []error {
	var failures []error
	if service.team != nil {
		alerts, err := service.team.QueueTeamAlerts(ctx)
		if err != nil {
			failures = append(failures, err)
		}
		for _, alert := range alerts {
			event := AlertEvent{BookingID: alert.BookingID, Kind: alert.Kind, Title: alert.Title, Body: alert.Body}
			if alert.ForTeam {
				event.Role = RoleTeam
			}
			service.publisher.Publish(alert.Recipient, NotificationEventKind, event)
		}
	}
	if service.mail != nil {
		if _, err := service.mail.SendDue(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
}

type MailService struct {
	queue     EmailQueue
	sender    EmailSender
	addresses EmailAddresses
	webURL    string
}

func NewMailService(queue EmailQueue, sender EmailSender, addresses EmailAddresses, webURL string) *MailService {
	return &MailService{queue: queue, sender: sender, addresses: addresses, webURL: strings.TrimRight(webURL, "/")}
}

func (service *MailService) SendDue(ctx context.Context) (int, error) {
	pending, err := service.queue.AwaitingEmail(ctx, emailBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list waiting email: %w", err)
	}
	sent := 0
	var failures []error
	for _, item := range pending {
		if ctx.Err() != nil {
			return sent, errors.Join(append(failures, ctx.Err())...)
		}
		sendErr := service.send(ctx, item)
		if sendErr != nil {
			failures = append(failures, fmt.Errorf("email notification %s: %w", item.NotificationID, sendErr))
		} else {
			sent++
		}
		if err := service.queue.RecordEmailAttempt(ctx, item.NotificationID, sendErr == nil); err != nil {
			failures = append(failures, err)
		}
	}
	return sent, errors.Join(failures...)
}

func (service *MailService) send(ctx context.Context, item PendingEmail) error {
	address := item.TeamEmail
	if !item.ForTeam {
		found, err := service.addresses.VerifiedEmail(ctx, item.AccountID)
		if err != nil {
			return fmt.Errorf("find address: %w", err)
		}
		address = found
	}
	return service.sender.Send(ctx, Email{
		To:             address,
		Subject:        item.Title,
		Text:           item.Body + "\n\n" + service.webURL + emailPath(item),
		IdempotencyKey: "notification/" + item.NotificationID.String(),
	})
}

func emailPath(item PendingEmail) string {
	switch item.Kind {
	case kindReportFiled:
		return reportsPath
	case kindListingPaused, kindListingUnpaused:
		if item.ForTeam {
			return adminListingsPath
		}
		return hostingPath
	case kindIDExpiring, kindIdentityInReview:
		return verifyPath
	case kindIdentityReview:
		return identityReviewsPath
	}
	return tripPathPrefix + item.BookingID.String()
}

func (service *JourneyService) advance(ctx context.Context, journey Journey) error {
	plan, err := planFor(journey, service.now().UTC())
	if err != nil {
		return err
	}
	for _, note := range plan.Notes {
		messageID := service.newID()
		posted, fresh, err := service.store.PostNote(ctx, journey.BookingID, messageID, service.newID(), note)
		if err != nil {
			return fmt.Errorf("post %s: %w", note.Kind, err)
		}
		if !fresh {
			continue
		}
		event := ThreadEvent{ConversationID: posted.ConversationID, Message: ThreadMessage{
			ID: messageID, ConversationID: posted.ConversationID, SenderRole: systemSenderRole,
			Body: note.Body, BookingID: journey.BookingID, CreatedAt: posted.CreatedAt,
		}}
		service.publisher.Publish(posted.GuestID, messageEventKind, event)
		service.publisher.Publish(posted.HostID, messageEventKind, event)
	}
	for _, alert := range plan.Alerts {
		fresh, err := service.store.DeliverAlert(ctx, journey.BookingID, service.newID(), alert)
		if err != nil {
			return fmt.Errorf("deliver %s: %w", alert.Kind, err)
		}
		if fresh {
			service.publisher.Publish(alert.Recipient, NotificationEventKind, AlertEvent{
				BookingID: journey.BookingID, Kind: alert.Kind, Title: alert.Title, Body: alert.Body,
			})
		}
	}
	return nil
}

func planFor(journey Journey, now time.Time) (Plan, error) {
	checkInAt, err := stays.Moment(journey.StartsOn, journey.CheckInTime, journey.TimeZone)
	if err != nil {
		return Plan{}, err
	}
	checkOutAt, err := stays.Moment(journey.EndsOn, journey.CheckOutTime, journey.TimeZone)
	if err != nil {
		return Plan{}, err
	}
	planner := &planner{journey: journey}
	switch journey.Status {
	case statusRequested:
		planner.requested()
	case statusDeclined:
		planner.declined()
	case statusCancelled:
		planner.cancelled()
	case statusConfirmed:
		planner.confirmed(now, checkInAt, checkOutAt)
		planner.problem()
	case statusCompleted:
		planner.completed(now)
		planner.problem()
	}
	return planner.plan, nil
}

func (p *planner) alert(kind Kind, recipient uuid.UUID, title, body string) {
	if slices.Contains(p.journey.SentNotifications, sentKey(kind, recipient)) {
		return
	}
	p.plan.Alerts = append(p.plan.Alerts, Alert{Kind: kind, Recipient: recipient, Title: title, Body: body})
}

func (p *planner) note(kind Kind, body string) {
	if slices.Contains(p.journey.PostedEvents, string(kind)) {
		return
	}
	p.plan.Notes = append(p.plan.Notes, ThreadNote{Kind: kind, Body: body})
}

func sentKey(kind Kind, recipient uuid.UUID) string {
	return string(kind) + ":" + recipient.String()
}

func (p *planner) dates() string {
	return p.journey.StartsOn.Format(dayLayout) + " to " + p.journey.EndsOn.Format(dayLayout)
}

func (p *planner) guestName() string {
	if p.journey.GuestName == "" {
		return missingGuestName
	}
	return p.journey.GuestName
}

func (p *planner) requested() {
	if p.journey.PaymentStatus != paymentAuthorized {
		return
	}
	p.alert(KindBookingRequested, p.journey.HostID, "New booking request",
		fmt.Sprintf("%s wants to stay at %s, %s.", p.guestName(), p.journey.ListingTitle, p.dates()))
}

func (p *planner) declined() {
	p.alert(KindBookingDeclined, p.journey.GuestID, "Request declined",
		fmt.Sprintf("The host could not take your stay at %s, %s. You were not charged.", p.journey.ListingTitle, p.dates()))
}

func (p *planner) cancelled() {
	switch p.journey.CancelledBy {
	case cancelledExpired:
		p.expired()
		return
	case cancelledByHost:
		p.alert(KindBookingCancelled, p.journey.GuestID, "Your host cancelled",
			fmt.Sprintf("The stay at %s, %s, is off. You are being refunded %s.", p.journey.ListingTitle, p.dates(), dollars(p.journey.CancelRefundCents)))
		p.note(KindBookingCancelled, "The host cancelled this booking.")
		return
	case cancelledByGuest:
		p.alert(KindBookingCancelled, p.journey.GuestID, "Booking cancelled",
			fmt.Sprintf("You cancelled %s, %s. Your refund is %s.", p.journey.ListingTitle, p.dates(), dollars(p.journey.CancelRefundCents)))
		p.alert(KindBookingCancelled, p.journey.HostID, "Booking cancelled",
			fmt.Sprintf("%s cancelled the stay at %s, %s.", p.guestName(), p.journey.ListingTitle, p.dates()))
		p.note(KindBookingCancelled, "The guest cancelled this booking.")
		return
	}
	if !slices.Contains(p.journey.SentNotifications, sentKey(KindBookingRequested, p.journey.HostID)) {
		return
	}
	p.alert(KindBookingCancelled, p.journey.HostID, "Booking cancelled",
		fmt.Sprintf("%s cancelled the stay at %s, %s.", p.guestName(), p.journey.ListingTitle, p.dates()))
}

func (p *planner) expired() {
	p.alert(KindBookingExpired, p.journey.GuestID, "Request expired",
		fmt.Sprintf("The host did not answer in time for %s, %s. You were not charged.", p.journey.ListingTitle, p.dates()))
	if slices.Contains(p.journey.SentNotifications, sentKey(KindBookingRequested, p.journey.HostID)) {
		p.alert(KindBookingExpired, p.journey.HostID, "Request expired",
			fmt.Sprintf("The request from %s for %s, %s, ran out of time.", p.guestName(), p.journey.ListingTitle, p.dates()))
	}
}

func (p *planner) problem() {
	journey := p.journey
	switch journey.ReportStatus {
	case reportOpen:
		p.alert(KindProblemReported, journey.HostID, "A problem was reported",
			fmt.Sprintf("%s reported a problem at %s. Our team will review it, and your payout waits until then.", p.guestName(), journey.ListingTitle))
	case reportResolved:
		p.alert(KindProblemResolved, journey.GuestID, "Your report was reviewed",
			fmt.Sprintf("Refund for %s: %s.", journey.ListingTitle, dollars(journey.ReportRefundCents)))
		p.alert(KindProblemResolved, journey.HostID, "A report was reviewed",
			fmt.Sprintf("Guest refund for %s: %s.", journey.ListingTitle, dollars(journey.ReportRefundCents)))
	}
}

func dollars(cents int) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

func (p *planner) confirmed(now, checkInAt, checkOutAt time.Time) {
	if !now.Before(checkOutAt) {
		return
	}
	journey := p.journey
	p.alert(KindBookingConfirmed, journey.GuestID, "You are booked",
		fmt.Sprintf("Your stay at %s is confirmed for %s.", journey.ListingTitle, p.dates()))
	p.alert(KindBookingConfirmed, journey.HostID, "Booking confirmed",
		fmt.Sprintf("%s is booked at %s for %s.", p.guestName(), journey.ListingTitle, p.dates()))
	p.note(KindBookingConfirmed, fmt.Sprintf("Booking confirmed for %s.", p.dates()))

	if !now.Before(checkInAt.Add(-stays.ArrivalLeadTime)) {
		if journey.HasArrivalGuide {
			p.alert(KindArrivalReady, journey.GuestID, "Check-in details are ready",
				fmt.Sprintf("See how to get into %s on your trip page.", journey.ListingTitle))
			p.note(KindArrivalReady, "Check-in details are ready on the trip page.")
		} else {
			p.alert(KindArrivalGuideNeeded, journey.HostID, "Add check-in details",
				fmt.Sprintf("%s arrives soon and %s has no entry instructions yet.", p.guestName(), journey.ListingTitle))
		}
	}

	if journey.CheckedInAt != nil {
		p.alert(KindGuestCheckedIn, journey.HostID, "Your guest checked in",
			fmt.Sprintf("%s checked in at %s.", p.guestName(), journey.ListingTitle))
		p.note(KindGuestCheckedIn, p.guestName()+" checked in.")
	} else if !now.Before(checkInAt) {
		p.alert(KindCheckInOpen, journey.GuestID, "Check-in is open",
			fmt.Sprintf("Tap check in when you arrive at %s.", journey.ListingTitle))
	}

	if journey.CheckedOutAt != nil {
		p.checkedOut()
	} else if !now.Before(checkInAt) && !now.Before(checkOutAt.Add(-checkOutReminderLead)) {
		p.alert(KindCheckOutReminder, journey.GuestID, "Checkout is coming up",
			fmt.Sprintf("Checkout at %s is %s on %s.", journey.ListingTitle, clockLabel(journey.CheckOutTime), journey.EndsOn.Format(dayLayout)))
	}
}

func (p *planner) checkedOut() {
	p.alert(KindGuestCheckedOut, p.journey.HostID, "Your guest checked out",
		fmt.Sprintf("%s checked out of %s.", p.guestName(), p.journey.ListingTitle))
	p.note(KindGuestCheckedOut, p.guestName()+" checked out.")
}

func (p *planner) completed(now time.Time) {
	journey := p.journey
	if journey.CheckedOutAt != nil {
		p.checkedOut()
	}
	if journey.HostReviewPublished {
		p.alert(KindReviewPublished, journey.GuestID, "You have a new review",
			fmt.Sprintf("Your host at %s reviewed you.", journey.ListingTitle))
	}
	if journey.GuestReviewPublished {
		p.alert(KindReviewPublished, journey.HostID, "You have a new review",
			fmt.Sprintf("%s reviewed %s.", p.guestName(), journey.ListingTitle))
	}
	if journey.CompletedAt == nil || !now.Before(journey.CompletedAt.Add(reviews.ReviewWindow)) {
		return
	}
	if !journey.GuestReviewed {
		p.alert(KindReviewInvite, journey.GuestID, "How was your stay?",
			fmt.Sprintf("Review %s within 14 days. Reviews show once you both write one, or when the 14 days end.", journey.ListingTitle))
	}
	if !journey.HostReviewed {
		p.alert(KindReviewInvite, journey.HostID, "Review your guest",
			fmt.Sprintf("Tell other hosts how %s was. You have 14 days.", p.guestName()))
	}
}

func clockLabel(timeOfDay string) string {
	parsed, err := time.Parse("15:04", timeOfDay)
	if err != nil {
		return timeOfDay
	}
	return parsed.Format("3:04 PM")
}
