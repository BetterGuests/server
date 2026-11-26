package usermap

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Member struct {
	ClerkUserID  string
	Name         string
	ImageURL     string
	JoinedAt     time.Time
	LastActiveAt *time.Time
}

type Place struct {
	City    string
	Country string
}

type SignIn struct {
	At           time.Time
	LastActiveAt time.Time
	Place        Place
	Device       string
	Browser      string
}

type MemberActivity struct {
	Place   Place
	SignIns []SignIn
}

type Summary struct {
	Total     int          `json:"total"`
	Active7d  int          `json:"active_7d"`
	Joined30d int          `json:"joined_30d"`
	Unplaced  int          `json:"unplaced"`
	Truncated bool         `json:"truncated"`
	Members   []MemberView `json:"members"`
	Events    []EventView  `json:"events"`
	Days      []DayView    `json:"days"`
	ReadAt    time.Time    `json:"read_at"`
}

type MemberView struct {
	ID           string     `json:"id"`
	AccountID    string     `json:"account_id"`
	Name         string     `json:"name"`
	ImageURL     string     `json:"image_url"`
	City         string     `json:"city"`
	Country      string     `json:"country"`
	JoinedAt     time.Time  `json:"joined_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	SignIns30d   int        `json:"sign_ins_30d"`
}

type EventKind string

const (
	SignedIn EventKind = "signed_in"
	Joined   EventKind = "joined"
)

type EventView struct {
	Kind     EventKind `json:"kind"`
	At       time.Time `json:"at"`
	MemberID string    `json:"member_id"`
	Name     string    `json:"name"`
	City     string    `json:"city"`
	Country  string    `json:"country"`
	Device   string    `json:"device"`
	Browser  string    `json:"browser"`
}

type DayView struct {
	Date   string `json:"date"`
	Active int    `json:"active"`
}

type Directory interface {
	Members(ctx context.Context, limit int) ([]Member, int, error)
	Activity(ctx context.Context, clerkUserID string) (MemberActivity, error)
}

type AdminChecker interface {
	IsAdmin(ctx context.Context, accountID uuid.UUID) (bool, error)
}

type Reader interface {
	Summary(ctx context.Context, adminID uuid.UUID) (Summary, error)
}
