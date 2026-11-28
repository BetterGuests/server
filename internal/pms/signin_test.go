package pms

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type authorizerStub struct{ refuse error }

func (authorizerStub) AuthorizeURL(state string) string {
	return "https://pms.test/authorize?state=" + url.QueryEscape(state)
}

func (stub authorizerStub) Exchange(_ context.Context, code string) (string, error) {
	return "token-for-" + code, stub.refuse
}

func signInsWith(t *testing.T, authorizer authorizerStub) *SignInService {
	t.Helper()
	service, err := NewSignInService([]App{{Provider: Hospitable, ClientSecret: "app-secret", Authorizer: authorizer}})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func stateFrom(t *testing.T, service *SignInService, host uuid.UUID) string {
	t.Helper()
	address, err := service.Start(host, Hospitable)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("state")
}

func TestASignInGivesAPassThatHidesTheTokenAndOnlyWorksForThatHost(t *testing.T) {
	service := signInsWith(t, authorizerStub{})
	host := uuid.New()

	pass, err := service.Finish(context.Background(), host, Hospitable, "abc", stateFrom(t, service, host))
	if err != nil || strings.Contains(pass, "token-for-abc") {
		t.Fatalf("pass %q, error %v", pass, err)
	}
	if token, err := service.Token(host, Hospitable, pass); err != nil || token != "token-for-abc" {
		t.Fatalf("token %q, error %v", token, err)
	}
	if _, err := service.Token(uuid.New(), Hospitable, pass); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("another host: %v", err)
	}
}

func TestAnotherHostsStateIsRefusedSoNobodyCanBeSignedInToSomeoneElsesAccount(t *testing.T) {
	service := signInsWith(t, authorizerStub{})
	attacker, victim := uuid.New(), uuid.New()

	_, err := service.Finish(context.Background(), victim, Hospitable, "abc", stateFrom(t, service, attacker))

	if !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("error %v", err)
	}
}

func TestAStateCannotBeUsedAsAPass(t *testing.T) {
	service := signInsWith(t, authorizerStub{})
	host := uuid.New()

	if _, err := service.Token(host, Hospitable, stateFrom(t, service, host)); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("error %v", err)
	}
}

func TestPassesAndStatesRunOut(t *testing.T) {
	service := signInsWith(t, authorizerStub{})
	host := uuid.New()
	state := stateFrom(t, service, host)
	pass, err := service.Finish(context.Background(), host, Hospitable, "abc", state)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	if _, err := service.Token(host, Hospitable, pass); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("pass: %v", err)
	}
	if _, err := service.Finish(context.Background(), host, Hospitable, "abc", state); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("state: %v", err)
	}
}

func TestImportingWithASignInSendsTheRealTokenAndRefusesAPastedOne(t *testing.T) {
	signIns := signInsWith(t, authorizerStub{})
	host := uuid.New()
	pass, err := signIns.Finish(context.Background(), host, Hospitable, "abc", stateFrom(t, signIns, host))
	if err != nil {
		t.Fatal(err)
	}
	seen := &recordingSource{}
	service := NewImportService(map[ProviderName]Source{Hospitable: seen}, ownedStub{}, &writerStub{}, fetcherStub{}).WithSignIns(signIns)

	if _, err := service.Preview(context.Background(), host, Hospitable, Credentials{Secret: pass}); err != nil || seen.secret != "token-for-abc" {
		t.Fatalf("secret %q, error %v", seen.secret, err)
	}
	if _, err := service.Preview(context.Background(), host, Hospitable, Credentials{Secret: "a-pasted-token"}); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("error %v", err)
	}
}

type recordingSource struct{ secret string }

func (source *recordingSource) Properties(_ context.Context, credentials Credentials) ([]Property, error) {
	source.secret = credentials.Secret
	return nil, nil
}

func TestSignInsStayOffUntilBothKeysAndASafeWebAddressAreSet(t *testing.T) {
	lookup := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}
	if apps, err := AppsFromLookup(lookup(nil)); err != nil || len(apps) != 0 {
		t.Fatalf("apps %v, error %v", apps, err)
	}
	if _, err := AppsFromLookup(lookup(map[string]string{"OWNERREZ_CLIENT_ID": "c_1"})); err == nil {
		t.Fatal("half a key pair was accepted")
	}
	keys := map[string]string{"OWNERREZ_CLIENT_ID": "c_1", "OWNERREZ_CLIENT_SECRET": "s", "APP_WEB_URL": "http://better-guests.example"}
	if _, err := AppsFromLookup(lookup(keys)); err == nil {
		t.Fatal("a plain web address was accepted")
	}
	keys["APP_WEB_URL"] = "https://better-guests.example/"
	apps, err := AppsFromLookup(lookup(keys))
	if err != nil || len(apps) != 1 || !strings.Contains(apps[0].Authorizer.AuthorizeURL("x"), url.QueryEscape("https://better-guests.example/hosting/connect/ownerrez")) {
		t.Fatalf("apps %v, error %v", apps, err)
	}
}

func TestHospitableSignInAndPropertiesAreRead(t *testing.T) {
	server := apiAnswering(t, map[string]string{
		"/oauth/token": `{"token_type":"Bearer","expires_in":43200,"access_token":"t"}`,
		"/v2/properties": `{"data":[{"id":"u1","public_name":"Canal loft","description":"A loft beside the canal with tall windows.",
			"address":{"number":"12","street":"Water Lane","city":"Leeds","country":"GB","coordinates":{"latitude":"53.8","longitude":-1.5}},
			"property_type":"loft","room_type":"ENTIRE_HOME","capacity":{"max":2,"bedrooms":1,"beds":1,"bathrooms":1.5},"picture":"https://img/cover"}]}`,
		"/v2/properties/u1/pricing": `{"data":{"currency":"USD","base_nightly_price":{"amount":12900}}}`,
		"/v2/properties/u1/images":  `{"data":[]}`,
	}, nil)
	source := NewHospitableSource("id", "secret")
	source.address, source.authAddress = server.URL, server.URL

	token, err := source.Exchange(context.Background(), "code")
	if err != nil || token != "t" {
		t.Fatalf("token %q, error %v", token, err)
	}
	properties, err := source.Properties(context.Background(), Credentials{Secret: token})
	if err != nil || len(properties) != 1 {
		t.Fatalf("properties %+v, error %v", properties, err)
	}
	got := properties[0]
	if got.Address.Line1 != "12 Water Lane" || got.NightlyCents != 12900 || got.PropertyType == "" || got.PhotoURLs[0] != "https://img/cover" {
		t.Fatalf("property %+v", got)
	}
}

func TestAUnitNumberStaysOnItsOwnLine(t *testing.T) {
	if line1, line2 := streetLines("4B", "221 Baker Street"); line1 != "221 Baker Street" || line2 != "4B" {
		t.Fatalf("%q %q", line1, line2)
	}
}

func TestOwnerRezWithoutListingAccessStillListsPlaces(t *testing.T) {
	server := apiAnswering(t, map[string]string{
		"/v2/properties": `{"items":[{"id":5,"name":"Ridge house","active":true,"address":{"street1":"9 Ridge","city":"Taos","country":"US"},
			"latitude":36.4,"longitude":-105.5,"property_type":"house","max_guests":6,"bedrooms":3,"bathrooms":2},{"id":6,"name":"Closed","active":false}]}`,
	}, nil)
	source := NewOwnerRezSource("c_1", "s", "https://web/hosting/connect/ownerrez")
	source.address = server.URL

	properties, err := source.Properties(context.Background(), Credentials{Secret: "t"})

	if err != nil || len(properties) != 1 || properties[0].MaxGuests != 6 || properties[0].Description != "" {
		t.Fatalf("properties %+v, error %v", properties, err)
	}
}

func TestOwnerRezSendsItsAppKeysOnlyWhenSwappingTheCode(t *testing.T) {
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen["authorization"], seen["agent"], seen["body"] = r.Header.Get("Authorization"), r.UserAgent(), string(body)
		_, _ = w.Write([]byte(`{"access_token":"at_1"}`))
	}))
	t.Cleanup(server.Close)
	source := NewOwnerRezSource("c_1", "s", "https://web/back")
	source.address = server.URL

	token, err := source.Exchange(context.Background(), "tc_code")

	if err != nil || token != "at_1" || !strings.HasPrefix(seen["authorization"], "Basic ") || !strings.Contains(seen["agent"], "c_1") || !strings.Contains(seen["body"], "code=tc_code") {
		t.Fatalf("token %q, seen %v, error %v", token, seen, err)
	}
}
