package main

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const centsPerDollar = 100

var legacyPropertyTypes = map[string]string{
	"HOUSE": "house", "CABIN": "cabin", "VILLA": "villa",
	"APARTMENT": "apartment", "COTTAGE": "cottage", "UNIQUE": "unique",
}

var legacyAmenityKeys = map[string]string{
	"ac": "air_conditioning", "pool": "outdoor_pool", "workspace": "dedicated_workspace",
	"washer": "washer", "kitchen": "kitchen", "heating": "heating",
	"fireplace": "fireplace", "hot_tub": "hot_tub",
}

var countryCodes = map[string]string{
	"united states": "US", "norway": "NO", "united kingdom": "GB", "ireland": "IE",
	"iceland": "IS", "greece": "GR", "croatia": "HR", "italy": "IT",
	"spain": "ES", "portugal": "PT", "denmark": "DK",
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]`)

type sitePrice struct {
	NightlyCents  int
	MinimumNights int
}

func mapPropertyType(legacy string) (string, error) {
	mapped, ok := legacyPropertyTypes[legacy]
	if !ok {
		return "", fmt.Errorf("unsupported legacy property type %q", legacy)
	}
	return mapped, nil
}

func mapAmenities(legacy []string) (mapped, skipped []string) {
	for _, key := range legacy {
		if target, ok := legacyAmenityKeys[key]; ok {
			mapped = append(mapped, target)
			continue
		}
		skipped = append(skipped, key)
	}
	return mapped, skipped
}

func mapCountryCode(country string) (string, error) {
	trimmed := strings.TrimSpace(country)
	if len(trimmed) == 2 {
		return strings.ToUpper(trimmed), nil
	}
	code, ok := countryCodes[strings.ToLower(trimmed)]
	if !ok {
		return "", fmt.Errorf("unsupported country %q", country)
	}
	return code, nil
}

func bedCount(beds, bedrooms int) int {
	return max(beds, bedrooms, 1)
}

func photoObjectKey(listingID string, position int, sourceURL string) string {
	digest := sha256.Sum256([]byte(sourceURL))
	return fmt.Sprintf("listings/%s/%02d-%s", listingID, position, hex.EncodeToString(digest[:4]))
}

func siteSlug(legacySlug string) string {
	return nonAlphanumeric.ReplaceAllString(strings.ToLower(legacySlug), "")
}

func parsePrices(reader io.Reader) (map[string]sitePrice, error) {
	records, err := csv.NewReader(reader).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read prices: %w", err)
	}
	prices := make(map[string]sitePrice, len(records))
	for index, record := range records {
		if index == 0 {
			continue
		}
		if len(record) < 6 {
			return nil, fmt.Errorf("price row %d has %d columns", index, len(record))
		}
		median, err := strconv.Atoi(record[3])
		if err != nil {
			return nil, fmt.Errorf("price row %d median: %w", index, err)
		}
		minNights, err := strconv.Atoi(record[5])
		if err != nil {
			return nil, fmt.Errorf("price row %d min nights: %w", index, err)
		}
		prices[record[0]] = sitePrice{NightlyCents: median * centsPerDollar, MinimumNights: minNights}
	}
	return prices, nil
}
