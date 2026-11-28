package calendars

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func day(value string) time.Time {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func calendarOf(events ...string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n")
}

func eventOf(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func TestBusyDatesAreReadFromTheShapesRentalSitesSend(t *testing.T) {
	data := calendarOf(
		eventOf("UID:a", "DTSTART;VALUE=DATE:20270105", "DTEND;VALUE=DATE:20270108"),
		eventOf("UID:b", "DTSTART;TZID=\"America/Denver\":20270110T150000", "DTEND:20270112T100000Z"),
		eventOf("UID:c", "DTSTART;VALUE=DATE:20270120"),
		eventOf("UID:d", "DTSTART;VALUE=DATE:20270201", "DURATION:P1W"),
		eventOf("UID:e", "DTSTART:2027030", " 1", "DTEND:20270303", "BEGIN:VALARM", "DTSTART:20990101", "END:VALARM"),
	)

	got, err := parseBusyRanges(data, day("2027-01-01"))

	want := []Range{
		{day("2027-01-05"), day("2027-01-08")},
		{day("2027-01-10"), day("2027-01-12")},
		{day("2027-01-20"), day("2027-01-21")},
		{day("2027-02-01"), day("2027-02-08")},
		{day("2027-03-01"), day("2027-03-03")},
	}
	if err != nil || len(got) != len(want) {
		t.Fatalf("got %v, error %v", got, err)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("event %d: got %v, want %v", index, got[index], want[index])
		}
	}
}

func TestCancelledFreePastAndOurOwnEventsHoldNothing(t *testing.T) {
	data := calendarOf(
		eventOf("UID:a", "STATUS:CANCELLED", "DTSTART:20270105", "DTEND:20270108"),
		eventOf("UID:b", "TRANSP:TRANSPARENT", "DTSTART:20270105", "DTEND:20270108"),
		eventOf("UID:c", "DTSTART:20250105", "DTEND:20250108"),
		eventOf("UID:"+uuid.NewString()+"@better-guests.com", "DTSTART:20270105", "DTEND:20270108"),
	)

	got, err := parseBusyRanges(data, day("2027-01-01"))

	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, error %v", got, err)
	}
}

func TestACutOffOrWrongPageIsRefusedSoDatesAreNotOpenedByMistake(t *testing.T) {
	for name, data := range map[string]string{
		"web page":   "<html>Sign in</html>",
		"cut off":    "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20270105\r\n",
		"unclosed":   "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VCALENDAR\r\n",
		"unreadable": string(calendarOf(eventOf("UID:a", "DTSTART:soon"))),
		"empty":      "",
	} {
		if _, err := parseBusyRanges([]byte(data), day("2027-01-01")); !errors.Is(err, ErrNotACalendar) {
			t.Errorf("%s: error %v", name, err)
		}
	}
}

func TestOurCalendarNamesNoGuestAndReadsBackAsOurs(t *testing.T) {
	data := writeCalendar([]Reservation{{ID: uuid.New(), StartsOn: day("2027-01-05"), EndsOn: day("2027-01-08"), CreatedAt: day("2027-01-01")}})

	if !strings.Contains(string(data), "DTSTART;VALUE=DATE:20270105\r\nDTEND;VALUE=DATE:20270108\r\nSUMMARY:Reserved\r\n") {
		t.Fatalf("calendar %s", data)
	}
	if got, err := parseBusyRanges(data, day("2027-01-01")); err != nil || len(got) != 0 {
		t.Fatalf("our own events came back as blocks: %v, %v", got, err)
	}
}

func TestOnlySecureCalendarLinksAreAccepted(t *testing.T) {
	if got, err := cleanAddress(" webcal://site.test/a.ics "); err != nil || got != "https://site.test/a.ics" {
		t.Fatalf("got %q, error %v", got, err)
	}
	for _, address := range []string{"http://site.test/a.ics", "file:///etc/passwd", "https://user:pass@site.test/a", "nonsense", ""} {
		if _, err := cleanAddress(address); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("%q: error %v", address, err)
		}
	}
}
