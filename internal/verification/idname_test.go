package verification

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func governmentID(status, first, middle, last, suffix string) string {
	return fmt.Sprintf(`{"type":"verification/government-id","id":"ver_1","attributes":{"status":%q,"name-first":%q,"name-middle":%q,"name-last":%q,"name-suffix":%q}}`,
		status, first, middle, last, suffix)
}

func eventWithIncluded(name, accountID string, included ...string) []byte {
	return []byte(fmt.Sprintf(`{"data":{"type":"event","id":"evt_1","attributes":{"name":%q,"created-at":%q,"payload":{"data":{"type":"inquiry","id":"inq_1","attributes":{"reference-id":%q,"fields":{"name-first":{"type":"string","value":"Typed"},"name-last":{"type":"string","value":"ByUser"}}}},"included":[%s]}}}}`,
		name, testNow.Format(time.RFC3339), accountID, strings.Join(included, ",")))
}

func TestWebhookSavesOnlyTheNameReadFromAPassedGovernmentID(t *testing.T) {
	cases := []struct {
		name     string
		event    string
		included []string
		want     IDName
	}{
		{name: "full name", event: EventCompleted, included: []string{governmentID("passed", "JOHN", "MICHAEL", "SMITH", "JR")}, want: IDName{First: "JOHN", Middle: "MICHAEL", Last: "SMITH", Suffix: "JR"}},
		{name: "approved event", event: EventApproved, included: []string{governmentID("passed", "John", "", "Smith", "")}, want: IDName{First: "John", Last: "Smith"}},
		{name: "surrounding spaces", event: EventCompleted, included: []string{governmentID("passed", "  John ", " ", " Smith ", "")}, want: IDName{First: "John", Last: "Smith"}},
		{name: "single legal name", event: EventCompleted, included: []string{governmentID("passed", "", "", "Sukarno", "")}, want: IDName{Last: "Sukarno"}},
		{name: "retry keeps the passed attempt", event: EventCompleted, included: []string{governmentID("failed", "Wrong", "", "Person", ""), governmentID("passed", "John", "", "Smith", "")}, want: IDName{First: "John", Last: "Smith"}},
		{name: "failed attempt only", event: EventCompleted, included: []string{governmentID("failed", "Wrong", "", "Person", "")}},
		{name: "typed fields are ignored", event: EventCompleted},
		{name: "other verification kinds are ignored", event: EventCompleted, included: []string{`{"type":"verification/selfie","id":"ver_2","attributes":{"status":"passed","name-first":"Not","name-last":"ID"}}`}},
		{name: "oversized name is dropped", event: EventCompleted, included: []string{governmentID("passed", strings.Repeat("a", maxNamePartLength+1), "", "Smith", "")}, want: IDName{Last: "Smith"}},
		{name: "declined saves nothing", event: EventDeclined, included: []string{governmentID("passed", "John", "", "Smith", "")}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, store, accountID := seededService(t)
			body := eventWithIncluded(testCase.event, accountID.String(), testCase.included...)

			if err := service.HandleWebhook(context.Background(), signedHeader(testSecret, testNow, body), body); err != nil {
				t.Fatal(err)
			}
			if len(store.syncedNames) != 1 {
				t.Fatalf("name synced %d times", len(store.syncedNames))
			}
			if got := store.syncedNames[0]; got != testCase.want {
				t.Fatalf("name = %+v, want %+v", got, testCase.want)
			}
		})
	}
}
