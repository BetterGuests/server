package usermap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

var ErrNotAdmin = errors.New("not found")

const (
	maxMembers         = 500
	placeLookupsAtOnce = 4
	summaryFreshFor    = 10 * time.Minute
	activeWindow       = 7 * 24 * time.Hour
	joinedWindow       = 30 * 24 * time.Hour
	graphDays          = 52 * 7
	maxEvents          = 200
)

type UserMapService struct {
	directory Directory
	admins    AdminChecker
	now       func() time.Time

	mu     sync.Mutex
	cached *Summary
}

func NewUserMapService(directory Directory, admins AdminChecker) *UserMapService {
	return &UserMapService{directory: directory, admins: admins, now: time.Now}
}

func (service *UserMapService) Summary(ctx context.Context, adminID uuid.UUID) (Summary, error) {
	admin, err := service.admins.IsAdmin(ctx, adminID)
	if err != nil {
		return Summary{}, fmt.Errorf("check admin: %w", err)
	}
	if !admin {
		return Summary{}, ErrNotAdmin
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if service.cached != nil && service.now().Sub(service.cached.ReadAt) < summaryFreshFor {
		return *service.cached, nil
	}
	summary, err := service.read(ctx)
	if err != nil {
		return Summary{}, err
	}
	service.cached = &summary
	return summary, nil
}

func (service *UserMapService) read(ctx context.Context) (Summary, error) {
	members, total, err := service.directory.Members(ctx, maxMembers)
	if err != nil {
		return Summary{}, fmt.Errorf("list members: %w", err)
	}
	activities, err := service.activitiesFor(ctx, members)
	if err != nil {
		return Summary{}, err
	}
	return summarize(members, activities, total, service.now()), nil
}

func (service *UserMapService) activitiesFor(ctx context.Context, members []Member) ([]MemberActivity, error) {
	activities := make([]MemberActivity, len(members))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(placeLookupsAtOnce)
	for index, member := range members {
		group.Go(func() error {
			activity, err := service.directory.Activity(groupCtx, member.ClerkUserID)
			if err != nil {
				return fmt.Errorf("member activity: %w", err)
			}
			activities[index] = activity
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return activities, nil
}

func summarize(members []Member, activities []MemberActivity, total int, now time.Time) Summary {
	summary := Summary{
		Total:     total,
		Truncated: total > len(members),
		Members:   make([]MemberView, 0, len(members)),
		ReadAt:    now,
	}
	days := newDayCounter(now, graphDays)
	var events []EventView
	for index, member := range members {
		activity := activities[index]
		if member.LastActiveAt != nil && now.Sub(*member.LastActiveAt) <= activeWindow {
			summary.Active7d++
		}
		if now.Sub(member.JoinedAt) <= joinedWindow {
			summary.Joined30d++
		}
		if activity.Place.Country == "" {
			summary.Unplaced++
		}
		activeDays := days.newMemberActivity()
		activeDays.mark(member.JoinedAt, member.JoinedAt)
		if member.LastActiveAt != nil {
			activeDays.mark(*member.LastActiveAt, *member.LastActiveAt)
		}
		events = append(events, EventView{
			Kind:     Joined,
			At:       member.JoinedAt,
			MemberID: member.ClerkUserID,
			Name:     member.Name,
			City:     activity.Place.City,
			Country:  activity.Place.Country,
		})
		recentSignIns := 0
		for _, signIn := range activity.SignIns {
			activeDays.mark(signIn.At, signIn.LastActiveAt)
			if now.Sub(signIn.At) <= joinedWindow {
				recentSignIns++
			}
			events = append(events, EventView{
				Kind:     SignedIn,
				At:       signIn.At,
				MemberID: member.ClerkUserID,
				Name:     member.Name,
				City:     signIn.Place.City,
				Country:  signIn.Place.Country,
				Device:   signIn.Device,
				Browser:  signIn.Browser,
			})
		}
		days.add(activeDays)
		summary.Members = append(summary.Members, MemberView{
			ID:           member.ClerkUserID,
			Name:         member.Name,
			ImageURL:     member.ImageURL,
			City:         activity.Place.City,
			Country:      activity.Place.Country,
			JoinedAt:     member.JoinedAt,
			LastActiveAt: member.LastActiveAt,
			SignIns30d:   recentSignIns,
		})
	}
	summary.Events = newestEvents(events, maxEvents)
	summary.Days = days.views()
	return summary
}

func newestEvents(events []EventView, limit int) []EventView {
	sort.SliceStable(events, func(a, b int) bool { return events[a].At.After(events[b].At) })
	if len(events) > limit {
		events = events[:limit]
	}
	return append([]EventView{}, events...)
}

type dayCounter struct {
	first time.Time
	days  []DayView
}

func newDayCounter(now time.Time, count int) *dayCounter {
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -(count - 1))
	days := make([]DayView, count)
	for index := range days {
		days[index].Date = first.AddDate(0, 0, index).Format(time.DateOnly)
	}
	return &dayCounter{first: first, days: days}
}

func (counter *dayCounter) offset(at time.Time) int {
	return int(at.UTC().Truncate(24*time.Hour).Sub(counter.first) / (24 * time.Hour))
}

type memberDays map[int]bool

func (counter *dayCounter) newMemberActivity() *memberActivity {
	return &memberActivity{counter: counter, days: memberDays{}}
}

type memberActivity struct {
	counter *dayCounter
	days    memberDays
}

func (activity *memberActivity) mark(from, until time.Time) {
	if until.Before(from) {
		until = from
	}
	first := max(activity.counter.offset(from), 0)
	last := min(activity.counter.offset(until), len(activity.counter.days)-1)
	for index := first; index <= last; index++ {
		activity.days[index] = true
	}
}

func (counter *dayCounter) add(activity *memberActivity) {
	for index := range activity.days {
		counter.days[index].Active++
	}
}

func (counter *dayCounter) views() []DayView {
	return counter.days
}
