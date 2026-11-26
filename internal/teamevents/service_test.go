package teamevents

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubStore struct{ got Query }

func (store *stubStore) Events(_ context.Context, _ uuid.UUID, query Query) ([]Event, error) {
	store.got = query
	return []Event{}, nil
}

type stubAdmins struct{ admin bool }

func (admins stubAdmins) IsAdmin(context.Context, uuid.UUID) (bool, error) {
	return admins.admin, nil
}

func TestFeedHidesFromNonAdmins(t *testing.T) {
	service := NewTeamEventsService(&stubStore{}, stubAdmins{admin: false})
	if _, err := service.Feed(context.Background(), uuid.New(), time.Time{}, nil); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("err = %v, want ErrNotAdmin", err)
	}
}

func TestFeedRejectsUnknownKinds(t *testing.T) {
	service := NewTeamEventsService(&stubStore{}, stubAdmins{admin: true})
	_, err := service.Feed(context.Background(), uuid.New(), time.Time{}, []Kind{"drop_table"})
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("err = %v, want ErrInvalidKind", err)
	}
}

func TestFeedReadsEveryKindWithinTheLookbackByDefault(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	store := &stubStore{}
	service := NewTeamEventsService(store, stubAdmins{admin: true})
	service.now = func() time.Time { return now }

	if _, err := service.Feed(context.Background(), uuid.New(), time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.got.Kinds) != len(AllKinds) {
		t.Fatalf("kinds = %v, want all", store.got.Kinds)
	}
	if !store.got.Since.Equal(now.Add(-feedLookback)) {
		t.Fatalf("since = %v, want %v", store.got.Since, now.Add(-feedLookback))
	}
}

func TestFeedKeepsARecentSinceForPolling(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Minute)
	store := &stubStore{}
	service := NewTeamEventsService(store, stubAdmins{admin: true})
	service.now = func() time.Time { return now }

	if _, err := service.Feed(context.Background(), uuid.New(), since, []Kind{PaymentSucceeded}); err != nil {
		t.Fatal(err)
	}
	if !store.got.Since.Equal(since) || len(store.got.Kinds) != 1 {
		t.Fatalf("query = %+v, want since %v and one kind", store.got, since)
	}
}
