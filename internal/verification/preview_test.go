package verification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BetterGuests/BetterGuests-Server/internal/accounts"
	"github.com/google/uuid"
)

func included(t *testing.T, kind string, attributes map[string]any) includedObject {
	t.Helper()
	raw, err := json.Marshal(attributes)
	if err != nil {
		t.Fatal(err)
	}
	return includedObject{Type: kind, Attributes: raw}
}

func TestPreviewPrefersThePassedGovernmentIDAndTheSelfieCheck(t *testing.T) {
	reviewed := reviewedIDFrom(RoleThis, []includedObject{
		included(t, governmentIDType, map[string]any{"status": "passed", "name-first": "MARIA", "name-middle": "ELENA", "name-last": "TORRES",
			"birthdate": "1991-04-02", "id-class": "dl", "country-code": "US", "expiration-date": "2030-04-02",
			"front-photo-url": "https://files.withpersona.com/front.jpg?access_token=a",
			"back-photo-url":  "https://files.withpersona.com/back.jpg?access_token=b"}),
		included(t, governmentIDType, map[string]any{"status": "failed", "name-first": "WRONG",
			"front-photo-url": "https://files.withpersona.com/blurry.jpg?access_token=c"}),
		included(t, selfieType, map[string]any{"status": "passed", "center-photo-url": "https://files.withpersona.com/selfie.jpg?access_token=d"}),
	})
	want := ReviewedID{
		Role: RoleThis, Available: true, Name: "MARIA ELENA TORRES", BirthDate: "1991-04-02", IDClass: "dl", CountryCode: "US",
		ExpiresOn: "2030-04-02", FrontPhotoURL: "https://files.withpersona.com/front.jpg?access_token=a",
		BackPhotoURL: "https://files.withpersona.com/back.jpg?access_token=b", SelfiePhotoURL: "https://files.withpersona.com/selfie.jpg?access_token=d",
	}
	if reviewed != want {
		t.Fatalf("got %+v", reviewed)
	}
}

func TestPreviewFallsBackToTheSelfieOnTheGovernmentID(t *testing.T) {
	reviewed := reviewedIDFrom(RolePrevious, []includedObject{
		included(t, governmentIDType, map[string]any{"status": "passed", "name-first": "Ann",
			"selfie-photo-url": "https://files.withpersona.com/face.jpg?access_token=x"}),
	})
	if reviewed.SelfiePhotoURL != "https://files.withpersona.com/face.jpg?access_token=x" || reviewed.Name != "Ann" {
		t.Fatalf("got %+v", reviewed)
	}
}

func TestPreviewOfARedactedInquiryIsUnavailable(t *testing.T) {
	if reviewed := reviewedIDFrom(RoleOtherAccount, nil); reviewed.Available || reviewed.Role != RoleOtherAccount {
		t.Fatalf("got %+v", reviewed)
	}
}

func TestPreviewOnlyPassesPersonaFileLinks(t *testing.T) {
	for raw, want := range map[string]string{
		"https://files.withpersona.com/a.jpg?access_token=t": "https://files.withpersona.com/a.jpg?access_token=t",
		"http://files.withpersona.com/a.jpg":                 "",
		"https://evil.example/a.jpg":                         "",
		"https://files.withpersona.com.evil.example/a.jpg":   "",
		"https://user@files.withpersona.com/a.jpg":           "",
		"javascript:alert(1)":                                "",
		"":                                                   "",
	} {
		if got := personaFileURL(raw); got != want {
			t.Errorf("personaFileURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

type fakeInquiryStore struct {
	inquiries []ReviewInquiry
	err       error
}

func (store fakeInquiryStore) ReviewInquiries(context.Context, uuid.UUID, uuid.UUID) ([]ReviewInquiry, error) {
	return store.inquiries, store.err
}

type fakeInquiryReader map[string][]includedObject

func (reader fakeInquiryReader) GetInquiry(_ context.Context, inquiryID string) (InquirySnapshot, error) {
	objects, found := reader[inquiryID]
	if !found {
		return InquirySnapshot{}, ErrPersonaRequest
	}
	return InquirySnapshot{ID: inquiryID, Included: objects}, nil
}

func TestPreviewKeepsGoingWhenOneInquiryIsGone(t *testing.T) {
	service := NewReviewService(nil).WithInquiries(
		fakeInquiryStore{inquiries: []ReviewInquiry{{Role: RoleThis, InquiryID: "inq_new"}, {Role: RolePrevious, InquiryID: "inq_gone"}}},
		fakeInquiryReader{"inq_new": {included(t, governmentIDType, map[string]any{"status": "passed", "name-first": "Ann"})}},
	)
	reviewed, err := service.Preview(context.Background(), uuid.New(), uuid.New())
	if err != nil || len(reviewed) != 2 || !reviewed[0].Available || reviewed[1].Available || reviewed[1].Role != RolePrevious {
		t.Fatalf("reviewed = %+v, err = %v", reviewed, err)
	}
}

func TestPreviewEndpointMapsErrors(t *testing.T) {
	for name, test := range map[string]struct {
		err    error
		status int
	}{
		"not an admin":     {ErrNotAdmin, http.StatusForbidden},
		"already decided":  {ErrReviewNotFound, http.StatusNotFound},
		"persona disabled": {ErrPreviewUnavailable, http.StatusNotFound},
		"database down":    {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			service := NewReviewService(nil)
			if !errors.Is(test.err, ErrPreviewUnavailable) {
				service = service.WithInquiries(fakeInquiryStore{err: test.err}, fakeInquiryReader{})
			}
			handler := (&HTTPHandler{}).WithReviews(service)
			request := httptest.NewRequest(http.MethodGet, "/preview", nil)
			request.SetPathValue("verification_id", uuid.NewString())
			request = request.WithContext(accounts.WithUserID(request.Context(), uuid.New()))
			recorder := httptest.NewRecorder()
			handler.PreviewReview(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}
