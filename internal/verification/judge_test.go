package verification

import (
	"slices"
	"testing"
	"time"
)

func TestJudgeCompletion(t *testing.T) {
	jordan := IDName{First: "Jordan", Last: "Rivera"}
	cases := []struct {
		name       string
		completion Completion
		history    History
		want       []ReviewReason
	}{
		{"first pass is accepted", Completion{Name: jordan}, History{}, nil},
		{"same name in other casing is accepted", Completion{Name: IDName{First: "JORDAN", Middle: "A", Last: "RIVERA"}}, History{PreviousName: jordan, HasEarlierPass: true}, nil},
		{"another first name is held", Completion{Name: IDName{First: "Sam", Last: "Rivera"}}, History{PreviousName: jordan, HasEarlierPass: true}, []ReviewReason{ReasonNameChanged}},
		{"added suffix is held", Completion{Name: IDName{First: "Jordan", Last: "Rivera", Suffix: "Jr"}}, History{PreviousName: jordan, HasEarlierPass: true}, []ReviewReason{ReasonNameChanged}},
		{"new ID with no readable name is held", Completion{}, History{PreviousName: jordan, HasEarlierPass: true}, []ReviewReason{ReasonNameUnconfirmed}},
		{"host with a proof and no stored name is held", Completion{Name: jordan}, History{HasEarlierPass: true, HasApprovedProof: true}, []ReviewReason{ReasonNameUnconfirmed}},
		{"guest with no stored name is accepted", Completion{Name: jordan}, History{HasEarlierPass: true}, nil},
		{"minor is held", Completion{Name: jordan, UnderAge: true}, History{}, []ReviewReason{ReasonUnderAge}},
		{"identity used on another account is held", Completion{Name: jordan}, History{DuplicateIdentity: true}, []ReviewReason{ReasonDuplicateIdentity}},
		{"pass after a decline is held", Completion{Name: jordan}, History{HasDecline: true}, []ReviewReason{ReasonPreviouslyDeclined}},
		{"decline cleared by a later pass is accepted", Completion{Name: jordan}, History{HasDecline: true, HasEarlierPass: true, PreviousName: jordan}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := judgeCompletion(testCase.completion, testCase.history); !slices.Equal(got, testCase.want) {
				t.Fatalf("reasons = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestUnderAgeTurnsOnTheEighteenthBirthday(t *testing.T) {
	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for born, want := range map[string]bool{"2008-11-25": false, "2008-11-26": true, "1990-01-01": false, "": false, "not-a-date": false} {
		if got := underAge(born, at); got != want {
			t.Errorf("born %q: under age = %v, want %v", born, got, want)
		}
	}
}

func TestFingerprintNeedsAKeyNameAndBirthDate(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	name := IDName{First: "Jordan", Last: "Rivera"}
	upper := IDName{First: "JORDAN", Middle: "A", Last: "RIVERA"}
	if got := fingerprint(key, name, "1990-01-01"); len(got) != 32 || !slices.Equal(got, fingerprint(key, upper, "1990-01-01")) {
		t.Fatal("the same person must have one fingerprint")
	}
	if slices.Equal(fingerprint(key, name, "1990-01-01"), fingerprint(key, name, "1990-01-02")) {
		t.Fatal("another birth date must change the fingerprint")
	}
	if fingerprint(nil, name, "1990-01-01") != nil || fingerprint(key, IDName{Last: "Rivera"}, "1990-01-01") != nil || fingerprint(key, name, "") != nil {
		t.Fatal("an incomplete identity must have no fingerprint")
	}
}

func TestSummarizeShowsTheLatestAttempt(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	passed := Attempt{InquiryID: "old", Status: StatusCompleted, CompletedAt: now.Add(-24 * time.Hour)}
	lapsed := Attempt{InquiryID: "old", Status: StatusCompleted, CompletedAt: now.Add(-ReverifyAfter - time.Hour)}
	fresh := func(status Status, review ReviewState) Attempt {
		return Attempt{InquiryID: "new", Status: status, Review: review, CompletedAt: now}
	}
	cases := []struct {
		name     string
		attempts []Attempt
		inquiry  string
		status   string
		verified bool
	}{
		{"nothing yet", nil, "", "none", false},
		{"new check still open is not reported as passed", []Attempt{fresh(StatusCreated, ""), passed}, "new", "created", true},
		{"expired user sees the new failure", []Attempt{fresh(StatusFailed, ""), lapsed}, "", "failed", false},
		{"abandoned attempt leaves the account stale", []Attempt{fresh(StatusCreated, ""), lapsed}, "", "stale", false},
		{"held change keeps the old pass", []Attempt{fresh(StatusCompleted, ReviewPending), passed}, "", "needs_review", true},
		{"rejected review reads as declined", []Attempt{fresh(StatusCompleted, ReviewRejected)}, "", "declined", false},
		{"revoked pass does not verify", []Attempt{{InquiryID: "old", Status: StatusCompleted, Revoked: true, CompletedAt: now}}, "", "declined", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, found := summarize(testCase.attempts, testCase.inquiry, now)
			if !found || got.Status != testCase.status || got.Verified != testCase.verified {
				t.Fatalf("got %+v found %v", got, found)
			}
		})
	}
	if _, found := summarize(nil, "someone-elses", now); found {
		t.Fatal("an unknown inquiry must not be found")
	}
}
