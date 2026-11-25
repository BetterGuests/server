package notifications

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	guest      = uuid.New()
	host       = uuid.New()
	checkInUTC = time.Date(2026, 12, 10, 23, 0, 0, 0, time.UTC)
	endUTC     = time.Date(2026, 12, 13, 18, 0, 0, 0, time.UTC)
)

func stay(status string) Journey {
	return Journey{
		BookingID: uuid.New(), ListingTitle: "Desert House", Status: status, GuestID: guest, HostID: host,
		GuestName: "Maria S.", HasArrivalGuide: true,
		StartsOn:    time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC),
		EndsOn:      time.Date(2026, 12, 13, 0, 0, 0, 0, time.UTC),
		CheckInTime: "16:00", CheckOutTime: "11:00", TimeZone: "America/Phoenix",
	}
}

func alertNames(plan Plan) []string {
	names := []string{}
	for _, alert := range plan.Alerts {
		to := "guest:"
		if alert.Recipient == host {
			to = "host:"
		}
		names = append(names, to+string(alert.Kind))
	}
	slices.Sort(names)
	return names
}

func TestWhoHearsWhatDuringAStay(t *testing.T) {
	checkedIn := checkInUTC.Add(time.Hour)
	finished := endUTC
	confirmed := []string{"guest:booking_confirmed", "host:booking_confirmed"}
	cases := map[string]struct {
		journey func() Journey
		now     time.Time
		want    []string
	}{
		"a request without a card hold stays quiet": {
			func() Journey { return stay(statusRequested) }, checkInUTC.Add(-72 * time.Hour), []string{},
		},
		"a held request reaches the host": {
			func() Journey { j := stay(statusRequested); j.PaymentStatus = paymentAuthorized; return j },
			checkInUTC.Add(-72 * time.Hour), []string{"host:booking_requested"},
		},
		"declined": {func() Journey { return stay(statusDeclined) }, checkInUTC.Add(-72 * time.Hour), []string{"guest:booking_declined"}},
		"cancelled before the host ever heard of it": {
			func() Journey { return stay(statusCancelled) }, checkInUTC.Add(-72 * time.Hour), []string{},
		},
		"confirmed well ahead": {func() Journey { return stay(statusConfirmed) }, checkInUTC.Add(-96 * time.Hour), confirmed},
		"two days out": {
			func() Journey { return stay(statusConfirmed) }, checkInUTC.Add(-47 * time.Hour),
			append([]string{"guest:arrival_ready"}, confirmed...),
		},
		"two days out with no guide": {
			func() Journey { j := stay(statusConfirmed); j.HasArrivalGuide = false; return j }, checkInUTC.Add(-47 * time.Hour),
			append(slices.Clone(confirmed), "host:arrival_guide_needed"),
		},
		"check-in time": {
			func() Journey { return stay(statusConfirmed) }, checkInUTC,
			append([]string{"guest:arrival_ready", "guest:check_in_open"}, confirmed...),
		},
		"checked in, the evening before checkout": {
			func() Journey { j := stay(statusConfirmed); j.CheckedInAt = &checkedIn; return j }, endUTC.Add(-17 * time.Hour),
			[]string{"guest:arrival_ready", "guest:booking_confirmed", "guest:check_out_reminder", "host:booking_confirmed", "host:guest_checked_in"},
		},
		"completed": {
			func() Journey { j := stay(statusCompleted); j.CompletedAt = &finished; return j }, endUTC.Add(time.Hour),
			[]string{"guest:review_invite", "host:review_invite"},
		},
		"completed long ago invites nobody": {
			func() Journey { j := stay(statusCompleted); j.CompletedAt = &finished; return j }, endUTC.Add(15 * 24 * time.Hour), []string{},
		},
		"both reviews published": {
			func() Journey {
				j := stay(statusCompleted)
				j.CompletedAt = &finished
				j.GuestReviewed, j.HostReviewed, j.GuestReviewPublished, j.HostReviewPublished = true, true, true, true
				return j
			}, endUTC.Add(time.Hour), []string{"guest:review_published", "host:review_published"},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			plan, err := planFor(test.journey(), test.now)
			if err != nil {
				t.Fatal(err)
			}
			want := slices.Clone(test.want)
			slices.Sort(want)
			if got := alertNames(plan); !slices.Equal(got, want) {
				t.Fatalf("alerts = %v, want %v", got, want)
			}
		})
	}
}

func TestNothingIsSentTwice(t *testing.T) {
	journey := stay(statusConfirmed)
	journey.SentNotifications = []string{sentKey(KindBookingConfirmed, guest), sentKey(KindBookingConfirmed, host)}
	journey.PostedEvents = []string{string(KindBookingConfirmed)}
	plan, err := planFor(journey, checkInUTC.Add(-96*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Alerts) != 0 || len(plan.Notes) != 0 {
		t.Fatalf("repeated: %+v", plan)
	}
}

func TestThreadNotesNeverCarryTheEntryCode(t *testing.T) {
	plan, err := planFor(stay(statusConfirmed), checkInUTC)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []Kind{}
	for _, note := range plan.Notes {
		kinds = append(kinds, note.Kind)
	}
	if !slices.Equal(kinds, []Kind{KindBookingConfirmed, KindArrivalReady}) {
		t.Fatalf("notes = %v", kinds)
	}
}

type journeyStoreStub struct {
	journeys []Journey
	fresh    bool
}

func (stub *journeyStoreStub) Journeys(context.Context, uuid.UUID, int) ([]Journey, error) {
	return stub.journeys, nil
}

func (stub *journeyStoreStub) DeliverAlert(context.Context, uuid.UUID, uuid.UUID, Alert) (bool, error) {
	return stub.fresh, nil
}

func (stub *journeyStoreStub) PostNote(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ThreadNote) (Posted, bool, error) {
	return Posted{ConversationID: uuid.New(), GuestID: guest, HostID: host}, stub.fresh, nil
}

type publisherStub struct{ kinds []string }

func (stub *publisherStub) Publish(_ uuid.UUID, kind string, _ any) {
	stub.kinds = append(stub.kinds, kind)
}

func TestSweepPushesOnlyWhatWasNewlyStored(t *testing.T) {
	for _, test := range []struct {
		fresh bool
		want  int
	}{{true, 4}, {false, 0}} {
		publisher := &publisherStub{}
		service := NewJourneyService(&journeyStoreStub{journeys: []Journey{stay(statusConfirmed)}, fresh: test.fresh}, publisher)
		service.now = func() time.Time { return checkInUTC.Add(-96 * time.Hour) }
		if err := service.Sweep(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(publisher.kinds) != test.want {
			t.Fatalf("fresh=%v published %v", test.fresh, publisher.kinds)
		}
	}
}

func TestNudgeNeverBlocks(t *testing.T) {
	service := NewJourneyService(&journeyStoreStub{}, &publisherStub{})
	for range 5 {
		service.Nudge()
	}
}
