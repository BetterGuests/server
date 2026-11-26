package usermap

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubDirectory struct {
	members    []Member
	activities map[string]MemberActivity
	reads      int
}

func (directory *stubDirectory) Members(context.Context, int) ([]Member, int, error) {
	directory.reads++
	return directory.members, len(directory.members) + 2, nil
}

func (directory *stubDirectory) Activity(_ context.Context, clerkUserID string) (MemberActivity, error) {
	return directory.activities[clerkUserID], nil
}

type stubAdmins struct{ admin bool }

func (admins stubAdmins) IsAdmin(context.Context, uuid.UUID) (bool, error) {
	return admins.admin, nil
}

func TestSummaryRejectsNonAdmins(t *testing.T) {
	service := NewUserMapService(&stubDirectory{}, stubAdmins{admin: false})
	if _, err := service.Summary(context.Background(), uuid.New()); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("err = %v, want ErrNotAdmin", err)
	}
}

func TestSummaryCountsActivityAndPlaces(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	yesterday := now.Add(-24 * time.Hour)
	lastMonth := now.Add(-40 * 24 * time.Hour)
	directory := &stubDirectory{
		members: []Member{
			{ClerkUserID: "user_new", Name: "New", JoinedAt: yesterday, LastActiveAt: &yesterday},
			{ClerkUserID: "user_old", Name: "Old", JoinedAt: lastMonth, LastActiveAt: &lastMonth},
		},
		activities: map[string]MemberActivity{"user_new": {
			Place: Place{City: "Lisbon", Country: "PT"},
			SignIns: []SignIn{
				{At: now.Add(-time.Hour), Place: Place{City: "Lisbon", Country: "PT"}, Device: "Macintosh", Browser: "Safari"},
				{At: now.Add(-60 * 24 * time.Hour), Place: Place{City: "Porto", Country: "PT"}},
			},
		}},
	}
	service := NewUserMapService(directory, stubAdmins{admin: true})
	service.now = func() time.Time { return now }

	summary, err := service.Summary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || !summary.Truncated {
		t.Fatalf("total = %d truncated = %v, want 4 true", summary.Total, summary.Truncated)
	}
	if summary.Active7d != 1 || summary.Joined30d != 1 || summary.Unplaced != 1 {
		t.Fatalf("active %d joined %d unplaced %d, want 1 1 1", summary.Active7d, summary.Joined30d, summary.Unplaced)
	}
	if summary.Members[0].City != "Lisbon" || summary.Members[0].Country != "PT" {
		t.Fatalf("first member place = %+v", summary.Members[0])
	}
}

func TestSummaryReusesRecentRead(t *testing.T) {
	directory := &stubDirectory{}
	service := NewUserMapService(directory, stubAdmins{admin: true})
	for range 3 {
		if _, err := service.Summary(context.Background(), uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	if directory.reads != 1 {
		t.Fatalf("reads = %d, want 1", directory.reads)
	}
}

func TestSummaryListsNewestEventsAndDailyCounts(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	joined := now.Add(-2 * 24 * time.Hour)
	directory := &stubDirectory{
		members: []Member{{ClerkUserID: "user_a", Name: "Ava", JoinedAt: joined}},
		activities: map[string]MemberActivity{"user_a": {SignIns: []SignIn{
			{At: now.Add(-time.Hour), Place: Place{City: "Austin", Country: "US"}},
			{At: now.Add(-3 * time.Hour)},
		}}},
	}
	service := NewUserMapService(directory, stubAdmins{admin: true})
	service.now = func() time.Time { return now }

	summary, err := service.Summary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	kinds := []EventKind{}
	for _, event := range summary.Events {
		kinds = append(kinds, event.Kind)
	}
	if len(kinds) != 3 || kinds[0] != SignedIn || kinds[2] != Joined {
		t.Fatalf("event kinds = %v, want sign-ins newest first then the join", kinds)
	}
	if summary.Events[0].City != "Austin" {
		t.Fatalf("newest event city = %q, want Austin", summary.Events[0].City)
	}
	if len(summary.Days) != graphDays {
		t.Fatalf("days = %d, want %d", len(summary.Days), graphDays)
	}
	activeDays := []int{}
	for index, day := range summary.Days {
		if day.Active > 0 {
			activeDays = append(activeDays, len(summary.Days)-1-index)
		}
	}
	if fmt.Sprint(activeDays) != "[2 0]" {
		t.Fatalf("active days ago = %v, want joined two days ago and signed in today", activeDays)
	}
	if summary.Days[len(summary.Days)-1].Date != "2026-11-25" || summary.Days[len(summary.Days)-1].Active != 1 {
		t.Fatalf("today = %+v, want one active member on 2026-11-25", summary.Days[len(summary.Days)-1])
	}
	if summary.Members[0].SignIns30d != 2 {
		t.Fatalf("sign-ins in 30 days = %d, want 2", summary.Members[0].SignIns30d)
	}
}

func TestDailyActivityCountsEachMemberOncePerDayAcrossLongSessions(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	joined := now.Add(-400 * 24 * time.Hour)
	directory := &stubDirectory{
		members: []Member{{ClerkUserID: "user_a", JoinedAt: joined}, {ClerkUserID: "user_b", JoinedAt: joined}},
		activities: map[string]MemberActivity{
			"user_a": {SignIns: []SignIn{
				{At: now.Add(-3 * 24 * time.Hour), LastActiveAt: now.Add(-1 * 24 * time.Hour)},
				{At: now.Add(-2 * 24 * time.Hour), LastActiveAt: now.Add(-2 * 24 * time.Hour)},
			}},
			"user_b": {SignIns: []SignIn{{At: now.Add(-time.Hour), LastActiveAt: now}}},
		},
	}
	service := NewUserMapService(directory, stubAdmins{admin: true})
	service.now = func() time.Time { return now }

	summary, err := service.Summary(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	last := summary.Days[len(summary.Days)-4:]
	got := []int{last[0].Active, last[1].Active, last[2].Active, last[3].Active}
	if fmt.Sprint(got) != "[1 1 1 1]" {
		t.Fatalf("last four days active = %v, want [1 1 1 1]", got)
	}
}
