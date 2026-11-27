package proofscan

import (
	"testing"
	"time"
)

func day(year int, month time.Month, date int) time.Time {
	return time.Date(year, month, date, 0, 0, 0, 0, time.UTC)
}

func TestLatestDate(t *testing.T) {
	scanned := time.Date(2026, 11, 26, 18, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		text  string
		want  time.Time
		found bool
	}{
		{"statement and due dates on a bill", "Statement date: Nov 3, 2026\nBilling period Oct 1 - Oct 31, 2026\nTotal due Nov 24, 2026", day(2026, 11, 24), true},
		{"a due date after the scan is ignored", "Statement date: November 3, 2026\nPay by Dec. 1, 2026", day(2026, 11, 3), true},
		{"the scan day itself counts", "Issued Nov 26, 2026", day(2026, 11, 26), true},
		{"day before month", "Issued 14 Sept 2026", day(2026, 9, 14), true},
		{"US slashes", "Bill date 10/07/2026", day(2026, 10, 7), true},
		{"ISO dates", "printed 2026-08-19", day(2026, 8, 19), true},
		{"impossible dates are skipped", "Due 02/30/2026 or Feb 30, 2026", time.Time{}, false},
		{"no dates", "Juniper Valley Power & Water", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := LatestDate(c.text, scanned)
			if found != c.found || !got.Equal(c.want) {
				t.Fatalf("got %v %v, want %v %v", got, found, c.want, c.found)
			}
		})
	}
}
