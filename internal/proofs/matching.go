package proofs

import (
	"slices"
	"strings"
	"unicode"
)

const postalPrefixLength = 5

var addressAliases = map[string]string{
	"street": "st", "avenue": "ave", "av": "ave", "road": "rd", "drive": "dr", "boulevard": "blvd",
	"lane": "ln", "court": "ct", "place": "pl", "circle": "cir", "terrace": "ter", "parkway": "pkwy",
	"highway": "hwy", "trail": "trl", "square": "sq", "way": "way",
	"north": "n", "south": "s", "east": "e", "west": "w",
	"northeast": "ne", "northwest": "nw", "southeast": "se", "southwest": "sw",
}

var unitMarkers = []string{"apt", "apartment", "unit", "suite", "ste"}

var generationSuffixes = []string{"jr", "sr", "ii", "iii", "iv"}

func words(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func addressWords(value string) []string {
	split := words(value)
	for index, word := range split {
		if alias, found := addressAliases[word]; found {
			split[index] = alias
		}
	}
	return split
}

func containsRun(haystack, needle []string) bool {
	if len(needle) == 0 {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if slices.Equal(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

func suffixesIn(split []string) []string {
	var found []string
	for _, suffix := range generationSuffixes {
		if slices.Contains(split, suffix) {
			found = append(found, suffix)
		}
	}
	return found
}

func nameMatches(documentName string, id IDName) bool {
	document := words(documentName)
	first, last := words(id.First), words(id.Last)
	if !containsRun(document, first) || !containsRun(document, last) {
		return false
	}
	return slices.Equal(suffixesIn(document), suffixesIn(words(id.Suffix)))
}

func addressMatches(documentAddress string, listing PropertyAddress) bool {
	document := addressWords(documentAddress)
	if !containsRun(document, addressWords(listing.Line1)) {
		return false
	}
	if !unitMatches(document, listing) {
		return false
	}
	postal := words(listing.PostalCode)
	if len(postal) == 0 {
		return containsRun(document, addressWords(listing.City))
	}
	prefix := postal[0]
	if len(prefix) > postalPrefixLength {
		prefix = prefix[:postalPrefixLength]
	}
	return slices.Contains(document, prefix)
}

func unitMatches(document []string, listing PropertyAddress) bool {
	unit := slices.DeleteFunc(addressWords(listing.Line2), isUnitMarker)
	if len(unit) == 0 {
		return slices.ContainsFunc(addressWords(listing.Line1), isUnitMarker) ||
			!slices.ContainsFunc(document, isUnitMarker)
	}
	remainder := slices.DeleteFunc(slices.Clone(document), isUnitMarker)
	street := addressWords(listing.Line1)
	return containsRun(remainder, append(street, unit...))
}

func isUnitMarker(word string) bool {
	return slices.Contains(unitMarkers, word)
}
