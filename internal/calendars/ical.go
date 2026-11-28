package calendars

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	calendarDate     = "20060102"
	calendarStamp    = "20060102T150405Z"
	maxLineBytes     = 64 << 10
	foldedLineLength = 75
)

type event struct {
	uid       string
	start     string
	end       string
	duration  string
	cancelled bool
	free      bool
}

func parseBusyRanges(data []byte, today time.Time) ([]Range, error) {
	lines, err := unfold(data)
	if err != nil || len(lines) == 0 || !strings.EqualFold(lines[0], "BEGIN:VCALENDAR") || !strings.EqualFold(lines[len(lines)-1], "END:VCALENDAR") {
		return nil, ErrNotACalendar
	}
	horizon := today.AddDate(horizonYears, 0, 0)
	ranges := []Range{}
	var current *event
	nested := 0
	for _, line := range lines {
		name, value := property(line)
		switch {
		case name == "BEGIN" && strings.EqualFold(value, "VEVENT"):
			current = &event{}
		case current == nil:
		case name == "BEGIN":
			nested++
		case name == "END" && nested > 0:
			nested--
		case name == "END":
			held, busy, err := current.held()
			if err != nil {
				return nil, err
			}
			if busy && held.EndsOn.After(today) && held.StartsOn.Before(horizon) {
				ranges = append(ranges, held)
			}
			if len(ranges) > maxBlocksPerFeed {
				return nil, ErrTooLarge
			}
			current = nil
		case nested == 0:
			current.read(name, value)
		}
	}
	if current != nil {
		return nil, ErrNotACalendar
	}
	return ranges, nil
}

func unfold(data []byte) ([]string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, maxLineBytes), maxFeedBytes)
	var lines []string
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
		case (line[0] == ' ' || line[0] == '\t') && len(lines) > 0:
			lines[len(lines)-1] += line[1:]
		default:
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

func property(line string) (string, string) {
	quoted := false
	for index, letter := range line {
		switch {
		case letter == '"':
			quoted = !quoted
		case letter == ':' && !quoted:
			name, _, _ := strings.Cut(line[:index], ";")
			return strings.ToUpper(name), strings.TrimSpace(line[index+1:])
		}
	}
	return "", ""
}

func (found *event) read(name, value string) {
	switch name {
	case "UID":
		found.uid = value
	case "DTSTART":
		found.start = value
	case "DTEND":
		found.end = value
	case "DURATION":
		found.duration = value
	case "STATUS":
		found.cancelled = strings.EqualFold(value, "CANCELLED")
	case "TRANSP":
		found.free = strings.EqualFold(value, "TRANSPARENT")
	}
}

func (found *event) held() (Range, bool, error) {
	if found.cancelled || found.free || strings.HasSuffix(found.uid, ownEventSuffix) {
		return Range{}, false, nil
	}
	start, err := dateOf(found.start)
	if err != nil {
		return Range{}, false, ErrNotACalendar
	}
	end := start.AddDate(0, 0, daysIn(found.duration))
	if found.end != "" {
		if end, err = dateOf(found.end); err != nil {
			return Range{}, false, ErrNotACalendar
		}
	}
	if !end.After(start) {
		end = start.AddDate(0, 0, 1)
	}
	return Range{StartsOn: start, EndsOn: end}, true, nil
}

func dateOf(value string) (time.Time, error) {
	if len(value) < len(calendarDate) {
		return time.Time{}, ErrNotACalendar
	}
	return time.Parse(calendarDate, value[:len(calendarDate)])
}

func daysIn(duration string) int {
	duration = strings.TrimPrefix(strings.ToUpper(duration), "+")
	duration = strings.TrimPrefix(duration, "P")
	for suffix, days := range map[string]int{"W": 7, "D": 1} {
		number, _, found := strings.Cut(duration, suffix)
		if count, err := strconv.Atoi(number); found && err == nil && count > 0 {
			return min(count*days, 366*horizonYears)
		}
	}
	return 0
}

func writeCalendar(reservations []Reservation) []byte {
	var out strings.Builder
	line := func(text string) {
		for len(text) > foldedLineLength {
			out.WriteString(text[:foldedLineLength] + "\r\n")
			text = " " + text[foldedLineLength:]
		}
		out.WriteString(text + "\r\n")
	}
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Better Guests//Calendar//EN")
	line("CALSCALE:GREGORIAN")
	for _, held := range reservations {
		line("BEGIN:VEVENT")
		line(fmt.Sprintf("UID:%s%s", held.ID, ownEventSuffix))
		line("DTSTAMP:" + held.CreatedAt.UTC().Format(calendarStamp))
		line("DTSTART;VALUE=DATE:" + held.StartsOn.Format(calendarDate))
		line("DTEND;VALUE=DATE:" + held.EndsOn.Format(calendarDate))
		line("SUMMARY:Reserved")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return []byte(out.String())
}
