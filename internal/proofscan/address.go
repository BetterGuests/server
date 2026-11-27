package proofscan

import (
	"strings"
	"unicode"
)

type Address struct {
	Line1      string
	City       string
	PostalCode string
}

var streetWords = map[string]string{
	"street": "st", "avenue": "ave", "av": "ave", "drive": "dr", "road": "rd",
	"boulevard": "blvd", "lane": "ln", "court": "ct", "place": "pl", "terrace": "ter",
	"circle": "cir", "parkway": "pkwy", "highway": "hwy", "trail": "trl", "way": "way",
	"north": "n", "south": "s", "east": "e", "west": "w",
	"northeast": "ne", "northwest": "nw", "southeast": "se", "southwest": "sw",
	"apartment": "apt", "suite": "ste", "unit": "unit",
}

func addressWords(text string) []string {
	cleaned := strings.Map(func(letter rune) rune {
		if unicode.IsLetter(letter) || unicode.IsDigit(letter) {
			return unicode.ToLower(letter)
		}
		return ' '
	}, text)
	words := strings.Fields(cleaned)
	for index, word := range words {
		if short, ok := streetWords[word]; ok {
			words[index] = short
		}
	}
	return words
}

func containsRun(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		matched := true
		for offset, word := range needle {
			if haystack[start+offset] != word {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func postalPrefix(postalCode string) []string {
	words := addressWords(postalCode)
	if len(words) == 0 {
		return nil
	}
	return words[:1]
}

func AddressMatches(text string, address Address) bool {
	street := addressWords(address.Line1)
	if len(street) == 0 {
		return false
	}
	found := addressWords(text)
	if !containsRun(found, street) {
		return false
	}
	if postal := postalPrefix(address.PostalCode); postal != nil {
		return containsRun(found, postal)
	}
	return containsRun(found, addressWords(address.City))
}
