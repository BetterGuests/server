package usermap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	clerk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/session"
	"github.com/clerk/clerk-sdk-go/v2/user"
)

const (
	clerkPageSize       = 100
	sessionsPerLookup   = 100
	newestFirstOrdering = "-created_at"
)

type ClerkDirectory struct {
	users    *user.Client
	sessions *session.Client
}

func NewClerkDirectory(secretKey string) (*ClerkDirectory, error) {
	if secretKey == "" {
		return nil, errors.New("clerk secret key is required")
	}
	config := &clerk.ClientConfig{BackendConfig: clerk.BackendConfig{Key: &secretKey}}
	return &ClerkDirectory{users: user.NewClient(config), sessions: session.NewClient(config)}, nil
}

func (directory *ClerkDirectory) Members(ctx context.Context, limit int) ([]Member, int, error) {
	members := make([]Member, 0, limit)
	total := 0
	ordering := newestFirstOrdering
	for offset := 0; offset < limit; offset += clerkPageSize {
		pageSize := int64(min(clerkPageSize, limit-offset))
		pageOffset := int64(offset)
		page, err := directory.users.List(ctx, &user.ListParams{
			ListParams: clerk.ListParams{Limit: &pageSize, Offset: &pageOffset},
			OrderBy:    &ordering,
		})
		if err != nil {
			return nil, 0, fmt.Errorf("list clerk users: %w", err)
		}
		total = int(page.TotalCount)
		for _, found := range page.Users {
			members = append(members, memberFrom(found))
		}
		if len(page.Users) < int(pageSize) {
			break
		}
	}
	return members, total, nil
}

func (directory *ClerkDirectory) Activity(ctx context.Context, clerkUserID string) (MemberActivity, error) {
	limit := int64(sessionsPerLookup)
	list, err := directory.sessions.List(ctx, &session.ListParams{
		ListParams: clerk.ListParams{Limit: &limit},
		UserID:     &clerkUserID,
	})
	if err != nil {
		return MemberActivity{}, fmt.Errorf("list clerk sessions: %w", err)
	}
	return activityFrom(list.Sessions), nil
}

func activityFrom(sessions []*clerk.Session) MemberActivity {
	var activity MemberActivity
	var newest int64
	for _, found := range sessions {
		place := placeOf(found.LatestActivity)
		signIn := SignIn{
			At:           time.UnixMilli(found.CreatedAt).UTC(),
			LastActiveAt: time.UnixMilli(found.LastActiveAt).UTC(),
			Place:        place,
		}
		if found.LatestActivity != nil {
			signIn.Device = strings.TrimSpace(valueOf(found.LatestActivity.DeviceType))
			signIn.Browser = strings.TrimSpace(valueOf(found.LatestActivity.BrowserName))
		}
		activity.SignIns = append(activity.SignIns, signIn)
		if place.Country != "" && found.LastActiveAt > newest {
			newest = found.LastActiveAt
			activity.Place = place
		}
	}
	return activity
}

func placeOf(latest *clerk.SessionActivity) Place {
	if latest == nil {
		return Place{}
	}
	return Place{
		City:    strings.TrimSpace(valueOf(latest.City)),
		Country: strings.TrimSpace(valueOf(latest.Country)),
	}
}

func memberFrom(found *clerk.User) Member {
	member := Member{
		ClerkUserID: found.ID,
		Name:        displayName(found),
		ImageURL:    valueOf(found.ImageURL),
		JoinedAt:    time.UnixMilli(found.CreatedAt).UTC(),
	}
	if found.LastActiveAt != nil {
		active := time.UnixMilli(*found.LastActiveAt).UTC()
		member.LastActiveAt = &active
	}
	return member
}

func displayName(found *clerk.User) string {
	name := strings.TrimSpace(valueOf(found.FirstName) + " " + valueOf(found.LastName))
	if name != "" {
		return name
	}
	if username := strings.TrimSpace(valueOf(found.Username)); username != "" {
		return username
	}
	return "Unnamed member"
}

func valueOf(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}
