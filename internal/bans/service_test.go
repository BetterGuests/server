package bans

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

type memoryStore struct {
	banned []Request
	lifted int
	err    error
}

func (store *memoryStore) Ban(_ context.Context, _, _ uuid.UUID, request Request, _ time.Time) error {
	if store.err == nil {
		store.banned = append(store.banned, request)
	}
	return store.err
}

func (store *memoryStore) Lift(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	store.lifted++
	return store.err
}

func (store *memoryStore) List(context.Context, uuid.UUID, int) ([]Ban, error) {
	return []Ban{}, store.err
}

type adminSet map[uuid.UUID]bool

func (admins adminSet) IsAdmin(_ context.Context, accountID uuid.UUID) (bool, error) {
	return admins[accountID], nil
}

type dropped []uuid.UUID

func (log *dropped) Disconnect(accountID uuid.UUID) { *log = append(*log, accountID) }

func TestBanningNeedsAnAdminAReasonAndAMemberAsTarget(t *testing.T) {
	admin, otherAdmin, member := uuid.New(), uuid.New(), uuid.New()
	cases := map[string]struct {
		actor   uuid.UUID
		request Request
		want    error
	}{
		"a member tries":   {member, Request{AccountID: otherAdmin, Reason: "x"}, ErrNotAdmin},
		"no reason":        {admin, Request{AccountID: member, Reason: "  "}, ErrInvalidBan},
		"reason too long":  {admin, Request{AccountID: member, Reason: strings.Repeat("a", MaxReasonRunes+1)}, ErrInvalidBan},
		"no account":       {admin, Request{Reason: "x"}, ErrInvalidBan},
		"themself":         {admin, Request{AccountID: admin, Reason: "x"}, ErrCannotBanAdmin},
		"another admin":    {admin, Request{AccountID: otherAdmin, Reason: "x"}, ErrCannotBanAdmin},
		"a member is fine": {admin, Request{AccountID: member, Reason: "  Fraud.  "}, nil},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store, connections := &memoryStore{}, &dropped{}
			service := NewBanService(store, adminSet{admin: true, otherAdmin: true}, connections)
			_, err := service.Ban(context.Background(), test.actor, test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			applied := test.want == nil
			if (len(store.banned) == 1) != applied || (len(*connections) == 1) != applied {
				t.Fatalf("stored=%d disconnected=%d", len(store.banned), len(*connections))
			}
			if applied && store.banned[0].Reason != "Fraud." {
				t.Fatalf("reason = %q", store.banned[0].Reason)
			}
		})
	}
}

func TestAFailedBanDisconnectsNobody(t *testing.T) {
	admin := uuid.New()
	connections := &dropped{}
	service := NewBanService(&memoryStore{err: ErrAlreadyBanned}, adminSet{admin: true}, connections)
	if _, err := service.Ban(context.Background(), admin, Request{AccountID: uuid.New(), Reason: "x"}); !errors.Is(err, ErrAlreadyBanned) {
		t.Fatal(err)
	}
	if len(*connections) != 0 {
		t.Fatal("disconnected without banning")
	}
}

func TestOnlyAdminsListOrLiftBans(t *testing.T) {
	store := &memoryStore{}
	service := NewBanService(store, adminSet{}, &dropped{})
	if _, err := service.List(context.Background(), uuid.New()); !errors.Is(err, ErrNotAdmin) {
		t.Fatal(err)
	}
	if err := service.Lift(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrNotAdmin) || store.lifted != 0 {
		t.Fatal(err)
	}
}

func TestBanRequestsMapToStatusCodes(t *testing.T) {
	admin := uuid.New()
	store := &memoryStore{}
	handler := NewHTTPHandler(NewBanService(store, adminSet{admin: true}, &dropped{}))
	post := func(actor uuid.UUID, body string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/bans", strings.NewReader(body))
		request = request.WithContext(accounts.WithUserID(request.Context(), actor))
		response := httptest.NewRecorder()
		handler.Ban(response, request)
		return response.Code
	}
	valid := `{"account_id":"` + uuid.NewString() + `","reason":"Fraud."}`
	for name, test := range map[string]struct {
		actor uuid.UUID
		body  string
		err   error
		want  int
	}{
		"created":        {admin, valid, nil, http.StatusCreated},
		"not an admin":   {uuid.New(), valid, nil, http.StatusNotFound},
		"unknown field":  {admin, `{"account":"x"}`, nil, http.StatusBadRequest},
		"already banned": {admin, valid, ErrAlreadyBanned, http.StatusConflict},
		"no account":     {admin, valid, ErrAccountNotFound, http.StatusNotFound},
		"broken":         {admin, valid, errors.New("boom"), http.StatusInternalServerError},
	} {
		store.err = test.err
		if got := post(test.actor, test.body); got != test.want {
			t.Errorf("%s = %d, want %d", name, got, test.want)
		}
	}
}
