package proofs

import (
	"regexp"
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

var honorifics = []string{"mr", "mrs", "ms", "dr"}

var countryWords = []string{"us", "usa", "united", "states", "of", "america"}

var peopleSeparators = regexp.MustCompile(`(?i)[&;/+|\n]|\band\b`)

var usStates = map[string]string{
	"al": "alabama", "ak": "alaska", "az": "arizona", "ar": "arkansas", "ca": "california", "co": "colorado",
	"ct": "connecticut", "de": "delaware", "dc": "district of columbia", "fl": "florida", "ga": "georgia",
	"hi": "hawaii", "id": "idaho", "il": "illinois", "in": "indiana", "ia": "iowa", "ks": "kansas",
	"ky": "kentucky", "la": "louisiana", "me": "maine", "md": "maryland", "ma": "massachusetts",
	"mi": "michigan", "mn": "minnesota", "ms": "mississippi", "mo": "missouri", "mt": "montana",
	"ne": "nebraska", "nv": "nevada", "nh": "new hampshire", "nj": "new jersey", "nm": "new mexico",
	"ny": "new york", "nc": "north carolina", "nd": "north dakota", "oh": "ohio", "ok": "oklahoma",
	"or": "oregon", "pa": "pennsylvania", "ri": "rhode island", "sc": "south carolina", "sd": "south dakota",
	"tn": "tennessee", "tx": "texas", "ut": "utah", "vt": "vermont", "va": "virginia", "wa": "washington",
	"wv": "west virginia", "wi": "wisconsin", "wy": "wyoming",
}

var generationSuffixes = []string{"jr", "sr", "ii", "iii", "iv"}

func words(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func addressWords(value string) []string {
	split := words(strings.ReplaceAll(value, "#", " unit "))
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

func nameMatches(documentName string, id IDName) bool {
	return slices.ContainsFunc(peopleSeparators.Split(documentName, -1), func(person string) bool {
		return namesOnePerson(person, id)
	})
}

func namesOnePerson(person string, id IDName) bool {
	first, last, suffix := words(id.First), words(id.Last), words(id.Suffix)
	if len(first) == 0 || len(last) == 0 {
		return false
	}
	parts := strings.Split(person, ",")
	if len(parts) > 1 && slices.Equal(words(parts[len(parts)-1]), suffix) && len(suffix) > 0 {
		parts = []string{strings.Join(parts, " ")}
	}
	switch len(parts) {
	case 1:
		given, found := strings.CutSuffix(strings.Join(withoutHonorific(words(parts[0])), " "), joined(last, suffix))
		return found && strings.HasSuffix(given, " ") && isGivenName(strings.Fields(given), first, words(id.Middle))
	case 2:
		surname := words(parts[0])
		given := words(parts[1])
		if len(suffix) > 0 {
			if rest, found := trimTail(given, suffix); found {
				given = rest
			} else if rest, found := trimTail(surname, suffix); found {
				surname = rest
			} else {
				return false
			}
		}
		return slices.Equal(surname, last) && isGivenName(given, first, words(id.Middle))
	}
	return false
}

func joined(last, suffix []string) string {
	return strings.Join(append(slices.Clone(last), suffix...), " ")
}

func trimTail(split, tail []string) ([]string, bool) {
	if len(split) < len(tail) || !slices.Equal(split[len(split)-len(tail):], tail) {
		return split, false
	}
	return split[:len(split)-len(tail)], true
}

func withoutHonorific(split []string) []string {
	if len(split) > 0 && slices.Contains(honorifics, split[0]) {
		return split[1:]
	}
	return split
}

func isGivenName(given, first, middle []string) bool {
	if len(given) < len(first) || !slices.Equal(given[:len(first)], first) {
		return false
	}
	return !slices.ContainsFunc(given[len(first):], func(word string) bool {
		return len([]rune(word)) > 1 && !slices.Contains(middle, word)
	})
}

func addressMatches(documentAddress string, listing PropertyAddress) bool {
	document := addressWords(documentAddress)
	if !containsRun(document, addressWords(listing.Line1)) {
		return false
	}
	if !unitMatches(document, listing) {
		return false
	}
	if !stateMatches(document, listing.Region) {
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

func stateMatches(document []string, region string) bool {
	wanted := words(region)
	if len(wanted) == 0 {
		return true
	}
	name := strings.Join(wanted, " ")
	for abbreviation, full := range usStates {
		if name == abbreviation || name == full {
			ending := withoutCountryAndPostalCode(document)
			return endsWith(ending, []string{abbreviation}) || endsWithStateName(ending, full)
		}
	}
	return containsRun(document, wanted)
}

func withoutCountryAndPostalCode(document []string) []string {
	end := len(document)
	for end > 0 && (isNumber(document[end-1]) || slices.Contains(countryWords, document[end-1])) {
		end--
	}
	return document[:end]
}

func isNumber(word string) bool {
	return !strings.ContainsFunc(word, func(r rune) bool { return !unicode.IsDigit(r) })
}

func endsWith(split, tail []string) bool {
	_, found := trimTail(split, tail)
	return found
}

func endsWithStateName(ending []string, full string) bool {
	if full == "virginia" && endsWith(ending, addressWords("west virginia")) {
		return false
	}
	return endsWith(ending, addressWords(full))
}

func isUnitMarker(word string) bool {
	return slices.Contains(unitMarkers, word)
}
