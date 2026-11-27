package proofscan

import "testing"

func TestCompareNamesFollowsTheReviewerRules(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		found    string
		want     NameVerdict
	}{
		{"same name in another case", "Alexander J Sample", "ALEXANDER J SAMPLE", NameMatch},
		{"missing middle name", "Alexander James Sample", "Alexander Sample", NameMatch},
		{"middle initial with a period", "Alexander James Sample", "Alexander J. Sample", NameMatch},
		{"different first name, same last", "Alexander Sample", "Maria Sample", NameLookCloser},
		{"different last name", "Alexander Sample", "Alexander Jones", NameNoMatch},
		{"nothing found", "Alexander Sample", "", NameNoMatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := CompareNames(testCase.expected, testCase.found); got != testCase.want {
				t.Fatalf("CompareNames(%q, %q) = %q, want %q", testCase.expected, testCase.found, got, testCase.want)
			}
		})
	}
}

const utilityBill = `Juniper Valley Power & Water
Service statement
Account holder
JASON M. COAWETTE
Service address
4122 E Cathedral Rock Dr
Phoenix, AZ 85044-1234
Electric service, 612 kWh $58.14`

func TestFindNamePicksTheLineThatBestMatchesTheExpectedName(t *testing.T) {
	found, verdict := FindName(utilityBill, "Jason Coawette")
	if found != "JASON M. COAWETTE" || verdict != NameMatch {
		t.Fatalf("got %q %q", found, verdict)
	}
}

func TestFindNameReportsADifferentPersonAsNoMatch(t *testing.T) {
	found, verdict := FindName("Account holder\nMaria Lopez\nService address", "Maria Sample")
	if found != "Maria Lopez" || verdict != NameNoMatch {
		t.Fatalf("got %q %q", found, verdict)
	}
}

func TestFindNameReportsNotFoundWhenNoLineSharesAWord(t *testing.T) {
	found, verdict := FindName(utilityBill, "Alexander Sample")
	if found != "" || verdict != NameNotFound {
		t.Fatalf("got %q %q", found, verdict)
	}
}

func TestFindNameWithoutAnExpectedNameIsNotFound(t *testing.T) {
	if _, verdict := FindName(utilityBill, " "); verdict != NameNotFound {
		t.Fatalf("got %q", verdict)
	}
}

func TestAddressMatchesAcrossAbbreviationsAndZipPlusFour(t *testing.T) {
	address := Address{Line1: "4122 East Cathedral Rock Drive", City: "Phoenix", PostalCode: "85044"}
	if !AddressMatches(utilityBill, address) {
		t.Fatal("expected the address to match")
	}
}

func TestAddressDoesNotMatchAnotherHouseNumber(t *testing.T) {
	address := Address{Line1: "4124 East Cathedral Rock Drive", City: "Phoenix", PostalCode: "85044"}
	if AddressMatches(utilityBill, address) {
		t.Fatal("expected no match for a different house number")
	}
}

func TestAddressDoesNotMatchTheSameStreetInAnotherZip(t *testing.T) {
	address := Address{Line1: "4122 East Cathedral Rock Drive", City: "Phoenix", PostalCode: "85048"}
	if AddressMatches(utilityBill, address) {
		t.Fatal("expected no match for a different zip")
	}
}

func TestAddressFallsBackToTheCityWithoutAPostalCode(t *testing.T) {
	address := Address{Line1: "4122 E Cathedral Rock Dr", City: "Phoenix"}
	if !AddressMatches(utilityBill, address) {
		t.Fatal("expected a match on street and city")
	}
}

func TestAddressWithoutAStreetNeverMatches(t *testing.T) {
	if AddressMatches(utilityBill, Address{City: "Phoenix", PostalCode: "85044"}) {
		t.Fatal("expected no match without a street line")
	}
}
