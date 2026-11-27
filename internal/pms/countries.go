package pms

import (
	"strings"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var countryCodesByName = sync.OnceValue(func() map[string]string {
	codes := map[string]string{
		"usa": "US", "united states of america": "US", "uk": "GB", "great britain": "GB",
		"england": "GB", "scotland": "GB", "wales": "GB",
	}
	names := display.English.Regions()
	for first := 'A'; first <= 'Z'; first++ {
		for second := 'A'; second <= 'Z'; second++ {
			code := string([]rune{first, second})
			region, err := language.ParseRegion(code)
			if err != nil || !region.IsCountry() || region.Canonicalize().String() != code {
				continue
			}
			if name := names.Name(region); name != "" {
				codes[strings.ToLower(name)] = code
			}
		}
	}
	return codes
})

func countryCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 2 && !strings.EqualFold(value, "uk") {
		return strings.ToUpper(value)
	}
	return countryCodesByName()[strings.ToLower(value)]
}
