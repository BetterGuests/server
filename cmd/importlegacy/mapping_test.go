package main

import (
	"strings"
	"testing"
)

func TestMapPropertyTypeCoversEveryLegacyType(t *testing.T) {
	cases := map[string]string{
		"HOUSE": "house", "CABIN": "cabin", "VILLA": "villa",
		"APARTMENT": "apartment", "COTTAGE": "cottage", "UNIQUE": "unique",
	}
	for legacy, want := range cases {
		got, err := mapPropertyType(legacy)
		if err != nil || got != want {
			t.Fatalf("%s: got %q, %v", legacy, got, err)
		}
	}
}

func TestMapPropertyTypeRejectsUnknownType(t *testing.T) {
	if _, err := mapPropertyType("CASTLE"); err == nil {
		t.Fatal("expected an error for an unknown type")
	}
}

func TestMapAmenitiesTranslatesLegacyKeysAndReportsUnmapped(t *testing.T) {
	mapped, skipped := mapAmenities([]string{"ac", "pool", "workspace", "kitchen", "mystery"})

	want := []string{"air_conditioning", "outdoor_pool", "dedicated_workspace", "kitchen"}
	if strings.Join(mapped, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v", mapped)
	}
	if len(skipped) != 1 || skipped[0] != "mystery" {
		t.Fatalf("expected mystery to be skipped, got %v", skipped)
	}
}

func TestMapCountryCodeHandlesNamesAndCodes(t *testing.T) {
	for input, want := range map[string]string{"United States": "US", "us": "US", "Norway": "NO"} {
		got, err := mapCountryCode(input)
		if err != nil || got != want {
			t.Fatalf("%s: got %q, %v", input, got, err)
		}
	}
	if _, err := mapCountryCode("Atlantis"); err == nil {
		t.Fatal("expected an error for an unknown country")
	}
}

func TestBedCountNeverDropsBelowOne(t *testing.T) {
	if got := bedCount(0, 3); got != 3 {
		t.Fatalf("expected bedrooms to backfill beds, got %d", got)
	}
	if got := bedCount(0, 0); got != 1 {
		t.Fatalf("expected a floor of 1, got %d", got)
	}
	if got := bedCount(4, 2); got != 4 {
		t.Fatalf("expected declared beds to win, got %d", got)
	}
}

func TestPhotoObjectKeyIsStableAndScopedToTheListing(t *testing.T) {
	first := photoObjectKey("listing-1", 3, "https://cdn.test/a%2Fb.jpg")
	again := photoObjectKey("listing-1", 3, "https://cdn.test/a%2Fb.jpg")
	other := photoObjectKey("listing-2", 3, "https://cdn.test/a%2Fb.jpg")

	if first != again || first == other {
		t.Fatalf("keys not stable or not scoped: %s %s %s", first, again, other)
	}
	if !strings.HasPrefix(first, "listings/listing-1/") {
		t.Fatalf("unexpected prefix: %s", first)
	}
}

func TestParsePricesReadsMedianAndMinimumNights(t *testing.T) {
	csv := "site_slug,title,nightly_min,nightly_median,nightly_max,min_nights\nbeachbreezeinn,Beach Breeze Inn,235,522,1027,2\n"

	prices, err := parsePrices(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}

	got := prices["beachbreezeinn"]
	if got.NightlyCents != 52200 || got.MinimumNights != 2 {
		t.Fatalf("unexpected price: %+v", got)
	}
}

func TestSiteSlugMatchesLegacySlugWithoutPunctuation(t *testing.T) {
	if got := siteSlug("gulf-breeze-10-min-to-beaches-4bed-2-5bath"); got != "gulfbreeze10mintobeaches4bed25bath" {
		t.Fatalf("got %s", got)
	}
}
