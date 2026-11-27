package automessages

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
)

var (
	ErrListingNotFound = errors.New("listing not found")
	ErrMessageNotFound = errors.New("automatic message not found")
	ErrInvalidMessage  = errors.New("invalid automatic message")
	ErrTooMany         = errors.New("a listing can have 10 automatic messages, so delete one first")
)

var (
	triggers     = []Trigger{TriggerBookingConfirmed, TriggerBeforeCheckIn, TriggerCheckInDay, TriggerCheckOutEve, TriggerCheckOutDay}
	arrivalFills = []string{FillEntryInstructions, FillEntryCode, FillWifiName, FillWifiPassword, FillCheckoutInstructions}
)

type AutoMessageService struct {
	store     Store
	outbox    Outbox
	vault     Vault
	publisher Publisher
	nudger    Nudger
	now       func() time.Time
	newID     func() uuid.UUID
}

func NewAutoMessageService(store Store, outbox Outbox, vault Vault) *AutoMessageService {
	return &AutoMessageService{store: store, outbox: outbox, vault: vault, now: time.Now, newID: uuid.New}
}

func (service *AutoMessageService) WithPublisher(publisher Publisher) *AutoMessageService {
	service.publisher = publisher
	return service
}

func (service *AutoMessageService) WithNudger(nudger Nudger) *AutoMessageService {
	service.nudger = nudger
	return service
}

func (service *AutoMessageService) Overview(ctx context.Context, hostID, listingID uuid.UUID) (Overview, error) {
	overview, err := service.store.Overview(ctx, hostID, listingID)
	if err != nil {
		return Overview{}, err
	}
	if overview.Messages == nil {
		overview.Messages = []AutoMessage{}
	}
	overview.Suggestions = unusedSuggestions(overview.Messages)
	return overview, nil
}

func (service *AutoMessageService) Create(ctx context.Context, hostID, listingID uuid.UUID, input Input) (AutoMessage, error) {
	input, err := cleanInput(input)
	if err != nil {
		return AutoMessage{}, err
	}
	at := service.timestamp()
	message := AutoMessage{ID: service.newID(), ListingID: listingID, Input: input, UpdatedAt: at}
	if err := service.store.Create(ctx, hostID, message, at); err != nil {
		return AutoMessage{}, err
	}
	service.nudge()
	return message, nil
}

func (service *AutoMessageService) Update(ctx context.Context, hostID, listingID, messageID uuid.UUID, input Input) (AutoMessage, error) {
	input, err := cleanInput(input)
	if err != nil {
		return AutoMessage{}, err
	}
	at := service.timestamp()
	message := AutoMessage{ID: messageID, ListingID: listingID, Input: input, UpdatedAt: at}
	if err := service.store.Update(ctx, hostID, message, at); err != nil {
		return AutoMessage{}, err
	}
	service.nudge()
	return message, nil
}

func (service *AutoMessageService) Delete(ctx context.Context, hostID, listingID, messageID uuid.UUID) error {
	return service.store.Delete(ctx, hostID, listingID, messageID)
}

func (service *AutoMessageService) timestamp() time.Time {
	return service.now().UTC().Truncate(time.Microsecond)
}

func (service *AutoMessageService) nudge() {
	if service.nudger != nil {
		service.nudger.Nudge()
	}
}

type scheduled struct {
	candidate Candidate
	sendAt    time.Time
}

func (service *AutoMessageService) SendDue(ctx context.Context) (int, error) {
	due, failures := service.dueNow(ctx)
	slices.SortStableFunc(due, func(first, second scheduled) int {
		if order := first.sendAt.Compare(second.sendAt); order != 0 {
			return order
		}
		return slices.Index(triggers, first.candidate.Trigger) - slices.Index(triggers, second.candidate.Trigger)
	})
	sent := 0
	for _, item := range due {
		posted, err := service.post(ctx, item.candidate)
		if err != nil {
			failures = append(failures, fmt.Errorf("automatic message %s for booking %s: %w",
				item.candidate.AutoMessageID, item.candidate.BookingID, err))
			continue
		}
		if posted {
			sent++
		}
	}
	return sent, errors.Join(failures...)
}

func (service *AutoMessageService) dueNow(ctx context.Context) ([]scheduled, []error) {
	var (
		due          []scheduled
		failures     []error
		afterBooking uuid.UUID
		afterMessage uuid.UUID
	)
	now := service.now().UTC()
	for {
		candidates, err := service.outbox.Candidates(ctx, afterBooking, afterMessage, candidatePageSize)
		if err != nil {
			return due, append(failures, fmt.Errorf("list automatic messages: %w", err))
		}
		for _, candidate := range candidates {
			sendAt, ready, err := schedule(candidate, now)
			if err != nil {
				failures = append(failures, fmt.Errorf("schedule booking %s: %w", candidate.BookingID, err))
				continue
			}
			if ready {
				due = append(due, scheduled{candidate: candidate, sendAt: sendAt})
			}
		}
		if len(candidates) < candidatePageSize {
			return due, failures
		}
		last := candidates[len(candidates)-1]
		afterBooking, afterMessage = last.BookingID, last.AutoMessageID
	}
}

func schedule(candidate Candidate, now time.Time) (time.Time, bool, error) {
	checkInAt, err := stays.Moment(candidate.StartsOn, candidate.Schedule.CheckInTime, candidate.Schedule.TimeZone)
	if err != nil {
		return time.Time{}, false, err
	}
	checkOutAt, err := stays.Moment(candidate.EndsOn, candidate.Schedule.CheckOutTime, candidate.Schedule.TimeZone)
	if err != nil {
		return time.Time{}, false, err
	}
	sendAt, err := triggerTime(candidate, checkInAt, checkOutAt)
	if err != nil {
		return time.Time{}, false, err
	}
	sendAt = latest(sendAt, candidate.ConfirmedAt)
	if sendAt.Before(candidate.ActiveSince) {
		return sendAt, false, nil
	}
	if isPrivate(candidate) {
		sendAt = latest(sendAt, checkInAt.Add(-stays.ArrivalLeadTime))
	}
	if usesArrivalDetails(candidate.Body) && !candidate.HasArrivalGuide {
		return sendAt, false, nil
	}
	return sendAt, !now.Before(sendAt) && now.Before(checkOutAt), nil
}

func triggerTime(candidate Candidate, checkInAt, checkOutAt time.Time) (time.Time, error) {
	switch candidate.Trigger {
	case TriggerBookingConfirmed:
		return candidate.ConfirmedAt, nil
	case TriggerBeforeCheckIn:
		return checkInAt.Add(-stays.ArrivalLeadTime), nil
	case TriggerCheckInDay:
		return stays.Moment(candidate.StartsOn, checkInDayHour, candidate.Schedule.TimeZone)
	case TriggerCheckOutEve:
		return stays.Moment(candidate.EndsOn.AddDate(0, 0, -1), checkOutEveHour, candidate.Schedule.TimeZone)
	case TriggerCheckOutDay:
		return checkOutAt.Add(-checkOutDayLead), nil
	}
	return time.Time{}, fmt.Errorf("%w: unknown trigger %q", ErrInvalidMessage, candidate.Trigger)
}

func latest(first, second time.Time) time.Time {
	if second.After(first) {
		return second
	}
	return first
}

func isPrivate(candidate Candidate) bool {
	return candidate.Private || candidate.QuotesSecret || usesArrivalDetails(candidate.Body)
}

func usesArrivalDetails(body string) bool {
	return slices.ContainsFunc(arrivalFills, func(fill string) bool { return strings.Contains(body, fill) })
}

func (service *AutoMessageService) post(ctx context.Context, candidate Candidate) (bool, error) {
	outgoing := Outgoing{MessageID: service.newID(), ConversationID: service.newID()}
	if isPrivate(candidate) {
		outgoing.Body, outgoing.PrivateBody = PrivateNotice, candidate.Body
	} else {
		values, err := stayValues(candidate.GuestName, candidate.ListingTitle, candidate.Schedule)
		if err != nil {
			return false, err
		}
		outgoing.Body = truncate(render(candidate.Body, values), messages.MaxBodyRunes)
		if outgoing.Body == "" {
			return false, nil
		}
	}
	posted, fresh, err := service.outbox.Post(ctx, candidate, outgoing)
	if err != nil || !fresh {
		return false, err
	}
	service.publish(posted, outgoing)
	return true, nil
}

func (service *AutoMessageService) publish(posted Posted, outgoing Outgoing) {
	if service.publisher == nil {
		return
	}
	event := messages.MessageEvent{ConversationID: posted.ConversationID, Message: messages.Message{
		ID: outgoing.MessageID, ConversationID: posted.ConversationID, SenderRole: messages.RoleHost,
		Body: outgoing.Body, CreatedAt: posted.CreatedAt, Reactions: []messages.Reaction{},
		Automatic: true, Private: outgoing.PrivateBody != "",
	}}
	service.publisher.Publish(posted.GuestID, messages.MessageEventKind, event)
	service.publisher.Publish(posted.HostID, messages.MessageEventKind, event)
}

func (service *AutoMessageService) Unseal(ctx context.Context, viewerID uuid.UUID, messageIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	sealed, err := service.vault.Sealed(ctx, viewerID, messageIDs)
	if err != nil {
		return nil, err
	}
	now := service.now().UTC()
	details := make(map[uuid.UUID]string, len(sealed))
	for _, item := range sealed {
		open, err := readable(item, now)
		if err != nil {
			return nil, err
		}
		if !open {
			continue
		}
		values, err := stayValues(item.GuestName, item.ListingTitle, item.Schedule)
		if err != nil {
			return nil, err
		}
		values[FillEntryInstructions] = item.Arrival.EntryInstructions
		values[FillEntryCode] = item.Arrival.EntryCode
		values[FillWifiName] = item.Arrival.WifiName
		values[FillWifiPassword] = item.Arrival.WifiPassword
		values[FillCheckoutInstructions] = item.Arrival.CheckoutInstructions
		body := render(item.Template, values)
		if body == "" {
			body = emptyPrivateDetails
		}
		details[item.MessageID] = body
	}
	return details, nil
}

func readable(sealed Sealed, now time.Time) (bool, error) {
	if sealed.ViewerIsHost {
		return true, nil
	}
	if sealed.BookingStatus != statusConfirmed || sealed.PaymentStatus != paymentSucceeded || sealed.CheckedOutAt != nil {
		return false, nil
	}
	checkInAt, err := stays.Moment(sealed.StartsOn, sealed.Schedule.CheckInTime, sealed.Schedule.TimeZone)
	if err != nil {
		return false, err
	}
	checkOutAt, err := stays.Moment(sealed.EndsOn, sealed.Schedule.CheckOutTime, sealed.Schedule.TimeZone)
	if err != nil {
		return false, err
	}
	return !now.Before(checkInAt.Add(-stays.ArrivalLeadTime)) && now.Before(checkOutAt), nil
}

func stayValues(guestName, listingTitle string, schedule stays.Schedule) (map[string]string, error) {
	checkIn, err := clockLabel(schedule.CheckInTime)
	if err != nil {
		return nil, err
	}
	checkOut, err := clockLabel(schedule.CheckOutTime)
	if err != nil {
		return nil, err
	}
	if guestName == "" {
		guestName = missingGuestName
	}
	return map[string]string{
		FillGuestName:    guestName,
		FillListingTitle: listingTitle,
		FillCheckInTime:  checkIn,
		FillCheckOutTime: checkOut,
	}, nil
}

func clockLabel(timeOfDay string) (string, error) {
	parsed, err := time.Parse(timeOfDayLayout, timeOfDay)
	if err != nil {
		return "", fmt.Errorf("read time of day %q: %w", timeOfDay, err)
	}
	return parsed.Format(clockLayout), nil
}

func render(template string, values map[string]string) string {
	pairs := make([]string, 0, len(values)*2)
	for fill, value := range values {
		pairs = append(pairs, fill, value)
	}
	replacer := strings.NewReplacer(pairs...)
	var lines []string
	previousBlank := true
	for line := range strings.SplitSeq(template, "\n") {
		if onlyEmptyFills(line, values) {
			continue
		}
		filled := replacer.Replace(line)
		blank := strings.TrimSpace(filled) == ""
		if blank && previousBlank {
			continue
		}
		previousBlank = blank
		lines = append(lines, filled)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func onlyEmptyFills(line string, values map[string]string) bool {
	found := false
	for fill, value := range values {
		if !strings.Contains(line, fill) {
			continue
		}
		if value != "" {
			return false
		}
		found = true
	}
	return found
}

func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

func cleanInput(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Body = strings.TrimSpace(strings.ReplaceAll(input.Body, "\r\n", "\n"))
	if !slices.Contains(triggers, input.Trigger) {
		return input, fmt.Errorf("%w: choose when to send it", ErrInvalidMessage)
	}
	if err := checkText(input.Name, "name", maxNameRunes); err != nil {
		return input, err
	}
	if err := checkText(input.Body, "message", maxBodyRunes); err != nil {
		return input, err
	}
	input.Private = input.Private || usesArrivalDetails(input.Body)
	return input, nil
}

func checkText(value, label string, limit int) error {
	if value == "" {
		return fmt.Errorf("%w: write a %s", ErrInvalidMessage, label)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("%w: the %s must be at most %d characters", ErrInvalidMessage, label, limit)
	}
	return nil
}

func unusedSuggestions(existing []AutoMessage) []Input {
	unused := []Input{}
	for _, suggestion := range suggestions() {
		if !slices.ContainsFunc(existing, func(message AutoMessage) bool { return message.Trigger == suggestion.Trigger }) {
			unused = append(unused, suggestion)
		}
	}
	return unused
}

func suggestions() []Input {
	return []Input{
		{
			Name: "Welcome", Trigger: TriggerBookingConfirmed, Enabled: true,
			Body: "Hi {guest_name}, thanks for booking {listing_title}.\n" +
				"Check-in is from {check_in_time} and checkout is by {check_out_time}.\n" +
				"I will send the check-in steps before you arrive.",
		},
		{
			Name: "How to get in", Trigger: TriggerBeforeCheckIn, Private: true, Enabled: true,
			Body: "Hi {guest_name}, here is how to get in.\n\n{entry_instructions}\nEntry code: {entry_code}",
		},
		{
			Name: "Arrival day", Trigger: TriggerCheckInDay, Private: true, Enabled: true,
			Body: "Check-in is open from {check_in_time} today.\nWi-Fi: {wifi_name}\nWi-Fi password: {wifi_password}",
		},
		{
			Name: "Checkout", Trigger: TriggerCheckOutEve, Private: true, Enabled: true,
			Body: "Checkout is by {check_out_time} tomorrow.\n\n{checkout_instructions}\n\nThank you for staying.",
		},
	}
}
