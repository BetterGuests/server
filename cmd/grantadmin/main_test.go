package main

import (
	"errors"
	"testing"
)

func TestPickVerifiedUser(t *testing.T) {
	verified := `{"id":"user_1","email_addresses":[{"email_address":"Jason@Example.com","verification":{"status":"verified"}}]}`
	unverified := `{"id":"user_2","email_addresses":[{"email_address":"jason@example.com","verification":{"status":"unverified"}}]}`
	other := `{"id":"user_3","email_addresses":[{"email_address":"someone@example.com","verification":{"status":"verified"}}]}`

	cases := []struct {
		name    string
		body    string
		want    string
		wantErr error
	}{
		{"verified match ignoring case", "[" + verified + "]", "user_1", nil},
		{"unverified address never counts", "[" + unverified + "]", "", errNoVerifiedUser},
		{"different address", "[" + other + "]", "", errNoVerifiedUser},
		{"no users", "[]", "", errNoVerifiedUser},
		{"two verified users are ambiguous", "[" + verified + "," + verified + "]", "", errAmbiguousClerkUsers},
		{"unverified duplicate is ignored", "[" + unverified + "," + verified + "]", "user_1", nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := pickVerifiedUser([]byte(testCase.body), "jason@example.com")
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
			if got != testCase.want {
				t.Fatalf("expected %q, got %q", testCase.want, got)
			}
		})
	}
}
