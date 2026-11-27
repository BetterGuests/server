package automessages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/messages"
	"github.com/BetterGuests/BetterGuests-Server/internal/stays"
	"github.com/google/uuid"
)

type outboxStub struct {
	candidates []Candidate
	posted     []Candidate
	outgoing   []Outgoing
	stale      bool
	postErr    error
}

func (stub *outboxStub) Candidates(_ context.Context, afterBooking, _ uuid.UUID, _ int) ([]Candidate, error) {
	if afterBooking != uuid.Nil {
		return nil, nil
	}
	return stub.candidates, nil
}

func (stub *outboxStub) Post(_ context.Context, candidate Candidate, outgoing Outgoing) (Posted, bool, error) {
	if stub.postErr != nil {
		return Posted{}, false, stub.postErr
	}
	if stub.stale {
		return Posted{}, false, nil
	}
	stub.posted = append(stub.posted, candidate)
	stub.outgoing = append(stub.outgoing, outgoing)
	return Posted{ConversationID: uuid.New(), GuestID: uuid.New(), HostID: uuid.New()}, true, nil
}

type vaultStub struct{ sealed []Sealed }

func (stub vaultStub) Sealed(context.Context, uuid.UUID, []uuid.UUID) ([]Sealed, error) {
	return stub.sealed, nil
}

type publisherStub struct{ events []messages.MessageEvent }

func (stub *publisherStub) Publish(_ uuid.UUID, _ string, payload any) {
	event, ok := payload.(messages.MessageEvent)
	if ok {
		stub.events = append(stub.events, event)
	}
}

func day(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func instant(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

var longBefore = instant("2026-01-01T00:00:00Z")

func phoenixStay(trigger Trigger, body string) Candidate {
	return Candidate{
		BookingID: uuid.New(), AutoMessageID: uuid.New(), Trigger: trigger, Body: body,
		HasArrivalGuide: true, ActiveSince: longBefore, ConfirmedAt: instant("2026-05-01T12:00:00Z"),
		StartsOn: day("2026-06-10"), EndsOn: day("2026-06-13"),
		Schedule:  stays.Schedule{CheckInTime: "16:00", CheckOutTime: "11:00", TimeZone: "America/Phoenix"},
		GuestName: "Maria S.", ListingTitle: "Trip House",
	}
}

func sendAt(t *testing.T, now time.Time, candidates ...Candidate) *outboxStub {
	t.Helper()
	outbox := &outboxStub{candidates: candidates}
	service := NewAutoMessageService(nil, outbox, nil)
	service.now = func() time.Time { return now }
	if _, err := service.SendDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	return outbox
}

func TestEachTriggerFiresAtItsMomentInTheListingTimeZone(t *testing.T) {
	tests := []struct {
		name    string
		trigger Trigger
		due     string
	}{
		{"two days before check-in", TriggerBeforeCheckIn, "2026-06-08T23:00:00Z"},
		{"morning of check-in", TriggerCheckInDay, "2026-06-10T15:00:00Z"},
		{"evening before checkout", TriggerCheckOutEve, "2026-06-13T01:00:00Z"},
		{"three hours before checkout", TriggerCheckOutDay, "2026-06-13T15:00:00Z"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			due := instant(test.due)
			if early := sendAt(t, due.Add(-time.Second), phoenixStay(test.trigger, "Hello")); len(early.posted) != 0 {
				t.Fatal("sent before its moment")
			}
			if onTime := sendAt(t, due, phoenixStay(test.trigger, "Hello")); len(onTime.posted) != 1 {
				t.Fatalf("not sent at %s", test.due)
			}
		})
	}
}

func TestCheckInDayFollowsTheListingThroughADaylightSavingChange(t *testing.T) {
	candidate := phoenixStay(TriggerCheckInDay, "Hello")
	candidate.Schedule.TimeZone = "America/New_York"
	candidate.StartsOn, candidate.EndsOn = day("2026-11-01"), day("2026-11-03")
	candidate.ConfirmedAt = instant("2026-10-01T00:00:00Z")

	if early := sendAt(t, instant("2026-11-01T12:00:00Z"), candidate); len(early.posted) != 0 {
		t.Fatal("sent at the summer offset")
	}
	if onTime := sendAt(t, instant("2026-11-01T13:00:00Z"), candidate); len(onTime.posted) != 1 {
		t.Fatal("not sent at 8 AM local standard time")
	}
}

func TestBookingConfirmedSendsRightAfterPaymentButNotBefore(t *testing.T) {
	candidate := phoenixStay(TriggerBookingConfirmed, "Thanks for booking")
	if early := sendAt(t, candidate.ConfirmedAt.Add(-time.Second), candidate); len(early.posted) != 0 {
		t.Fatal("sent before confirmation")
	}
	if onTime := sendAt(t, candidate.ConfirmedAt, candidate); len(onTime.posted) != 1 {
		t.Fatal("not sent on confirmation")
	}
}

func TestCheckInDetailsNeverPostBeforeTheArrivalWindow(t *testing.T) {
	windowOpens := instant("2026-06-08T23:00:00Z")
	tests := map[string]Candidate{
		"uses a fill-in":      phoenixStay(TriggerBookingConfirmed, "Code {entry_code}"),
		"marked private":      phoenixStay(TriggerBookingConfirmed, "Key is under the mat"),
		"types the real code": phoenixStay(TriggerBookingConfirmed, "Code 4821"),
	}
	marked := tests["marked private"]
	marked.Private = true
	tests["marked private"] = marked
	quoted := tests["types the real code"]
	quoted.QuotesSecret = true
	tests["types the real code"] = quoted

	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if early := sendAt(t, windowOpens.Add(-time.Second), candidate); len(early.posted) != 0 {
				t.Fatal("private details were posted early")
			}
			open := sendAt(t, windowOpens, candidate)
			if len(open.outgoing) != 1 {
				t.Fatal("not sent when the window opened")
			}
			if got := open.outgoing[0]; got.Body != PrivateNotice || got.PrivateBody != candidate.Body {
				t.Fatalf("details leaked into the visible body: %+v", got)
			}
		})
	}
}

func TestALateBookingGetsOverdueMessagesAtOnceInTriggerOrder(t *testing.T) {
	confirmed := instant("2026-06-10T18:00:00Z")
	arrivalDay := phoenixStay(TriggerCheckInDay, "Arrival day")
	steps := phoenixStay(TriggerBeforeCheckIn, "Steps")
	welcome := phoenixStay(TriggerBookingConfirmed, "Welcome")
	for _, candidate := range []*Candidate{&arrivalDay, &steps, &welcome} {
		candidate.ConfirmedAt = confirmed
	}
	outbox := sendAt(t, confirmed, arrivalDay, steps, welcome)
	var order []string
	for _, outgoing := range outbox.outgoing {
		order = append(order, outgoing.Body)
	}
	if strings.Join(order, ",") != "Welcome,Steps,Arrival day" {
		t.Fatalf("order: %v", order)
	}
}

func TestNothingIsSentAfterCheckout(t *testing.T) {
	checkout := instant("2026-06-13T18:00:00Z")
	for _, trigger := range triggers {
		if outbox := sendAt(t, checkout, phoenixStay(trigger, "Hello")); len(outbox.posted) != 0 {
			t.Fatalf("%s sent after checkout", trigger)
		}
	}
}

func TestANewMessageDoesNotGoOutForMomentsThatAlreadyPassed(t *testing.T) {
	created := instant("2026-06-09T00:00:00Z")
	welcome := phoenixStay(TriggerBookingConfirmed, "Welcome")
	steps := phoenixStay(TriggerBeforeCheckIn, "Steps")
	arrival := phoenixStay(TriggerCheckInDay, "Arrival day")
	for _, candidate := range []*Candidate{&welcome, &steps, &arrival} {
		candidate.ActiveSince = created
	}
	outbox := sendAt(t, instant("2026-06-10T16:00:00Z"), welcome, steps, arrival)
	if len(outbox.posted) != 1 || outbox.posted[0].Trigger != TriggerCheckInDay {
		t.Fatalf("sent: %+v", outbox.posted)
	}
}

func TestAMessageThatQuotesTheArrivalGuideWaitsUntilItIsSaved(t *testing.T) {
	candidate := phoenixStay(TriggerBeforeCheckIn, "{entry_instructions}")
	candidate.HasArrivalGuide = false
	now := instant("2026-06-09T00:00:00Z")
	if outbox := sendAt(t, now, candidate); len(outbox.posted) != 0 {
		t.Fatal("sent without any entry instructions")
	}
}

func TestPublicMessagesFillInStayDetailsOnce(t *testing.T) {
	candidate := phoenixStay(TriggerBookingConfirmed, "Hi {guest_name}, {listing_title} from {check_in_time} to {check_out_time}.")
	candidate.GuestName = "{entry_code}"
	outbox := sendAt(t, candidate.ConfirmedAt, candidate)
	want := "Hi {entry_code}, Trip House from 4:00 PM to 11:00 AM."
	if len(outbox.outgoing) != 1 || outbox.outgoing[0].Body != want || outbox.outgoing[0].PrivateBody != "" {
		t.Fatalf("outgoing: %+v", outbox.outgoing)
	}
}

func TestSentMessagesAreAnnouncedWithoutTheirPrivateDetails(t *testing.T) {
	outbox := &outboxStub{candidates: []Candidate{phoenixStay(TriggerBeforeCheckIn, "Code {entry_code}")}}
	publisher := &publisherStub{}
	service := NewAutoMessageService(nil, outbox, nil).WithPublisher(publisher)
	service.now = func() time.Time { return instant("2026-06-09T00:00:00Z") }
	if sent, err := service.SendDue(context.Background()); err != nil || sent != 1 {
		t.Fatalf("sent %d: %v", sent, err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("expected the guest and host to hear it, got %d", len(publisher.events))
	}
	message := publisher.events[0].Message
	if message.Body != PrivateNotice || message.Details != "" || !message.Private || !message.Automatic || message.SenderRole != messages.RoleHost {
		t.Fatalf("announced: %+v", message)
	}
}

func TestAMessageAlreadyTakenIsNotAnnouncedAgain(t *testing.T) {
	outbox := &outboxStub{candidates: []Candidate{phoenixStay(TriggerBookingConfirmed, "Welcome")}, stale: true}
	publisher := &publisherStub{}
	service := NewAutoMessageService(nil, outbox, nil).WithPublisher(publisher)
	service.now = func() time.Time { return instant("2026-06-09T00:00:00Z") }
	if sent, err := service.SendDue(context.Background()); err != nil || sent != 0 || len(publisher.events) != 0 {
		t.Fatalf("sent %d, events %d, err %v", sent, len(publisher.events), err)
	}
}

func TestPrivateDetailsOpenOnlyFromTwoDaysBeforeCheckInUntilCheckout(t *testing.T) {
	sealed := Sealed{
		MessageID: uuid.New(), Template: "Code {entry_code}\nWi-Fi: {wifi_name}",
		BookingStatus: "confirmed", PaymentStatus: "succeeded",
		StartsOn: day("2026-06-10"), EndsOn: day("2026-06-13"),
		Schedule: stays.Schedule{CheckInTime: "16:00", CheckOutTime: "11:00", TimeZone: "America/Phoenix"},
		Arrival:  stays.ArrivalDetails{EntryCode: "4821"},
	}
	checkedOut := instant("2026-06-12T00:00:00Z")
	with := func(change func(*Sealed)) Sealed {
		changed := sealed
		change(&changed)
		return changed
	}
	tests := []struct {
		name   string
		sealed Sealed
		now    string
		open   bool
	}{
		{"a second before the window", sealed, "2026-06-08T22:59:59Z", false},
		{"as the window opens", sealed, "2026-06-08T23:00:00Z", true},
		{"a second before checkout", sealed, "2026-06-13T17:59:59Z", true},
		{"at checkout", sealed, "2026-06-13T18:00:00Z", false},
		{"cancelled", with(func(s *Sealed) { s.BookingStatus = "cancelled" }), "2026-06-10T00:00:00Z", false},
		{"unpaid", with(func(s *Sealed) { s.PaymentStatus = "pending" }), "2026-06-10T00:00:00Z", false},
		{"checked out early", with(func(s *Sealed) { s.CheckedOutAt = &checkedOut }), "2026-06-12T01:00:00Z", false},
		{"the host, any time", with(func(s *Sealed) { s.ViewerIsHost = true }), "2026-07-01T00:00:00Z", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewAutoMessageService(nil, nil, vaultStub{sealed: []Sealed{test.sealed}})
			service.now = func() time.Time { return instant(test.now) }
			details, err := service.Unseal(context.Background(), uuid.New(), []uuid.UUID{sealed.MessageID})
			if err != nil {
				t.Fatal(err)
			}
			got, open := details[sealed.MessageID]
			if open != test.open || open && got != "Code 4821" {
				t.Fatalf("open=%v details=%q", open, got)
			}
		})
	}
}

func TestLinesForMissingDetailsAreLeftOut(t *testing.T) {
	values := map[string]string{FillWifiName: "", FillWifiPassword: "", FillEntryCode: "4821"}
	got := render("Welcome\n\nWi-Fi: {wifi_name}\nPassword: {wifi_password}\n\nCode: {entry_code}", values)
	if got != "Welcome\n\nCode: 4821" {
		t.Fatalf("rendered %q", got)
	}
}

func TestSavingChecksTheMessageAndMarksFillInsPrivate(t *testing.T) {
	valid := Input{Name: " Steps ", Trigger: TriggerBeforeCheckIn, Body: " Code {entry_code} ", Enabled: true}
	cleaned, err := cleanInput(valid)
	if err != nil || cleaned.Name != "Steps" || cleaned.Body != "Code {entry_code}" || !cleaned.Private {
		t.Fatalf("cleaned %+v: %v", cleaned, err)
	}
	invalid := map[string]Input{
		"no name":         {Trigger: TriggerCheckInDay, Body: "Hi"},
		"no body":         {Name: "Hi", Trigger: TriggerCheckInDay, Body: "  "},
		"unknown trigger": {Name: "Hi", Trigger: "later", Body: "Hi"},
		"long name":       {Name: strings.Repeat("a", maxNameRunes+1), Trigger: TriggerCheckInDay, Body: "Hi"},
		"long body":       {Name: "Hi", Trigger: TriggerCheckInDay, Body: strings.Repeat("a", maxBodyRunes+1)},
		"nul byte":        {Name: "Hi", Trigger: TriggerCheckInDay, Body: "a\x00b"},
	}
	for name, input := range invalid {
		if _, err := cleanInput(input); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSuggestionsAreValidAndSkipTriggersAlreadyUsed(t *testing.T) {
	for _, suggestion := range suggestions() {
		cleaned, err := cleanInput(suggestion)
		if err != nil || cleaned != suggestion {
			t.Errorf("suggestion %q changes when saved: %+v %v", suggestion.Name, cleaned, err)
		}
	}
	unused := unusedSuggestions([]AutoMessage{{Input: Input{Trigger: TriggerBeforeCheckIn}}})
	for _, suggestion := range unused {
		if suggestion.Trigger == TriggerBeforeCheckIn {
			t.Fatal("suggested a trigger the host already has")
		}
	}
	if len(unused) != len(suggestions())-1 {
		t.Fatalf("suggestions left: %d", len(unused))
	}
}
