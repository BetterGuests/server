package proofscan

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var monthNumbers = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

const monthPattern = `(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?`

var (
	monthFirstDate = regexp.MustCompile(`(?i)\b` + monthPattern + `\s+(\d{1,2}),?\s+(\d{4})\b`)
	dayFirstDate   = regexp.MustCompile(`(?i)\b(\d{1,2})\s+` + monthPattern + `,?\s+(\d{4})\b`)
	slashDate      = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)
	isoDate        = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
)

func calendarDate(year, month, day string) (time.Time, bool) {
	y, errYear := strconv.Atoi(year)
	m, errMonth := strconv.Atoi(month)
	d, errDay := strconv.Atoi(day)
	if errYear != nil || errMonth != nil || errDay != nil || m < 1 || m > 12 {
		return time.Time{}, false
	}
	date := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if date.Day() != d || date.Month() != time.Month(m) {
		return time.Time{}, false
	}
	return date, true
}

func namedMonth(name string) string {
	return strconv.Itoa(int(monthNumbers[strings.ToLower(name[:3])]))
}

func datesIn(text string) []time.Time {
	var dates []time.Time
	keep := func(date time.Time, ok bool) {
		if ok {
			dates = append(dates, date)
		}
	}
	for _, m := range monthFirstDate.FindAllStringSubmatch(text, -1) {
		keep(calendarDate(m[3], namedMonth(m[1]), m[2]))
	}
	for _, m := range dayFirstDate.FindAllStringSubmatch(text, -1) {
		keep(calendarDate(m[3], namedMonth(m[2]), m[1]))
	}
	for _, m := range slashDate.FindAllStringSubmatch(text, -1) {
		keep(calendarDate(m[3], m[1], m[2]))
	}
	for _, m := range isoDate.FindAllStringSubmatch(text, -1) {
		keep(calendarDate(m[1], m[2], m[3]))
	}
	return dates
}

func LatestDate(text string, notAfter time.Time) (time.Time, bool) {
	limit := notAfter.UTC().Truncate(24 * time.Hour)
	var latest time.Time
	for _, date := range datesIn(text) {
		if date.After(limit) || !date.After(latest) {
			continue
		}
		latest = date
	}
	return latest, !latest.IsZero()
}
