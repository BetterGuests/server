package reports

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	filed    []Submission
	decided  []Decision
	listed   int
	failWith error
}

func (store *memoryStore) Create(_ context.Context, _, _ uuid.UUID, submission Submission, _ time.Time) error {
	if store.failWith != nil {
		return store.failWith
	}
	store.filed = append(store.filed, submission)
	return nil
}

func (store *memoryStore) List(context.Context, uuid.UUID, bool, int) ([]Report, error) {
	store.listed++
	return []Report{}, nil
}

func (store *memoryStore) Resolve(_ context.Context, _, _ uuid.UUID, decision Decision, _ time.Time) error {
	store.decided = append(store.decided, decision)
	return store.failWith
}

type adminSet map[uuid.UUID]bool

func (admins adminSet) IsAdmin(_ context.Context, accountID uuid.UUID) (bool, error) {
	return admins[accountID], nil
}

func TestEachTargetAcceptsOnlyItsOwnReasonsAndContext(t *testing.T) {
	listing, booking, conversation := uuid.New(), uuid.New(), uuid.New()
	cases := map[string]struct {
		submission Submission
		want       error
	}{
		"listing":                 {Submission{Target: TargetListing, Reason: ReasonInaccurate, ListingID: listing}, nil},
		"host from a listing":     {Submission{Target: TargetHost, Reason: ReasonHarassment, ListingID: listing}, nil},
		"host from a trip":        {Submission{Target: TargetHost, Reason: ReasonScam, BookingID: booking}, nil},
		"host from messages":      {Submission{Target: TargetHost, Reason: ReasonOther, ConversationID: conversation}, nil},
		"guest from a booking":    {Submission{Target: TargetGuest, Reason: ReasonPropertyDamage, BookingID: booking}, nil},
		"guest from messages":     {Submission{Target: TargetGuest, Reason: ReasonOffPlatformPayment, ConversationID: conversation}, nil},
		"unknown target":          {Submission{Target: "city", Reason: ReasonOther, ListingID: listing}, ErrInvalidReport},
		"host reason on listing":  {Submission{Target: TargetListing, Reason: ReasonHarassment, ListingID: listing}, ErrInvalidReport},
		"damage is only a guest":  {Submission{Target: TargetHost, Reason: ReasonPropertyDamage, BookingID: booking}, ErrInvalidReport},
		"listing without an id":   {Submission{Target: TargetListing, Reason: ReasonScam}, ErrInvalidReport},
		"listing from a trip":     {Submission{Target: TargetListing, Reason: ReasonScam, BookingID: booking}, ErrInvalidReport},
		"guest from a listing":    {Submission{Target: TargetGuest, Reason: ReasonScam, ListingID: listing}, ErrInvalidReport},
		"host with two contexts":  {Submission{Target: TargetHost, Reason: ReasonScam, BookingID: booking, ConversationID: conversation}, ErrInvalidReport},
		"guest with two contexts": {Submission{Target: TargetGuest, Reason: ReasonScam, BookingID: booking, ConversationID: conversation}, ErrInvalidReport},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store := &memoryStore{}
			test.submission.Details = "Something happened."
			receipt, err := NewReportService(store, adminSet{}).File(context.Background(), uuid.New(), test.submission)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if stored := len(store.filed) == 1; stored != (test.want == nil) {
				t.Fatalf("stored = %v", stored)
			}
			if test.want == nil && receipt.ID == uuid.Nil {
				t.Fatal("missing receipt id")
			}
		})
	}
}

func TestDetailsAreTrimmedAndMustFitTheLimit(t *testing.T) {
	cases := map[string]struct {
		details string
		want    error
	}{
		"blank":                        {"   \n ", ErrInvalidDetails},
		"too long":                     {strings.Repeat("é", MaxDetailsRunes+1), ErrInvalidDetails},
		"longest counted in letters":   {strings.Repeat("é", MaxDetailsRunes), nil},
		"padding does not count":       {"  " + strings.Repeat("a", MaxDetailsRunes) + "  ", nil},
		"surrounding space is removed": {"  Broken lock.  ", nil},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store := &memoryStore{}
			_, err := NewReportService(store, adminSet{}).File(context.Background(), uuid.New(), Submission{
				Target: TargetListing, Reason: ReasonOther, ListingID: uuid.New(), Details: test.details,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.want == nil && store.filed[0].Details != strings.TrimSpace(test.details) {
				t.Fatalf("details = %q", store.filed[0].Details)
			}
		})
	}
}

func TestOnlyTheTeamCanReadOrCloseReports(t *testing.T) {
	admin := uuid.New()
	store := &memoryStore{}
	service := NewReportService(store, adminSet{admin: true})
	ctx := context.Background()

	if _, err := service.List(ctx, uuid.New(), true); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("list by a member: %v", err)
	}
	if err := service.Resolve(ctx, uuid.New(), uuid.New(), Decision{Outcome: StatusDismissed}); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("resolve by a member: %v", err)
	}
	if store.listed != 0 || len(store.decided) != 0 {
		t.Fatal("the store was reached without admin rights")
	}
	if _, err := service.List(ctx, admin, true); err != nil {
		t.Fatal(err)
	}
	if err := service.Resolve(ctx, admin, uuid.New(), Decision{Outcome: StatusActioned, Note: "  Warned.  "}); err != nil {
		t.Fatal(err)
	}
	if store.decided[0].Note != "Warned." {
		t.Fatalf("note = %q", store.decided[0].Note)
	}
}

func TestADecisionMustCloseTheReport(t *testing.T) {
	admin := uuid.New()
	service := NewReportService(&memoryStore{}, adminSet{admin: true})
	for name, decision := range map[string]Decision{
		"reopen":   {Outcome: StatusOpen},
		"empty":    {},
		"long one": {Outcome: StatusDismissed, Note: strings.Repeat("a", MaxNoteRunes+1)},
	} {
		if err := service.Resolve(context.Background(), admin, uuid.New(), decision); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
