package pms

import (
	"strings"
	"unicode"

	"github.com/BetterGuests/BetterGuests-Server/internal/listings"
)

var propertyTypesByName = map[string]listings.PropertyType{
	"house":               listings.House,
	"vacationhome":        listings.House,
	"holidayhome":         listings.House,
	"countryhouse":        listings.House,
	"farmhouse":           listings.House,
	"bungalow":            listings.House,
	"apartment":           listings.Apartment,
	"servicedapartment":   listings.Apartment,
	"loft":                listings.Apartment,
	"studio":              listings.Apartment,
	"aparthotel":          listings.Apartment,
	"penthouse":           listings.Apartment,
	"condo":               listings.Condo,
	"condominium":         listings.Condo,
	"townhouse":           listings.Townhouse,
	"townhome":            listings.Townhouse,
	"villa":               listings.Villa,
	"cabin":               listings.Cabin,
	"logcabin":            listings.Cabin,
	"cottage":             listings.Cottage,
	"chalet":              listings.Cabin,
	"skichalet":           listings.Cabin,
	"barn":                listings.Unique,
	"boat":                listings.Unique,
	"houseboat":           listings.Unique,
	"camperrv":            listings.Unique,
	"rv":                  listings.Unique,
	"recreationalvehicle": listings.Unique,
	"castle":              listings.Unique,
	"cave":                listings.Unique,
	"dome":                listings.Unique,
	"domehouse":           listings.Unique,
	"farmstay":            listings.Unique,
	"igloo":               listings.Unique,
	"lighthouse":          listings.Unique,
	"tent":                listings.Unique,
	"tinyhouse":           listings.Unique,
	"tipi":                listings.Unique,
	"treehouse":           listings.Unique,
	"windmill":            listings.Unique,
	"yurt":                listings.Unique,
}

func propertyTypeNamed(name string) listings.PropertyType {
	simplified := strings.Map(func(letter rune) rune {
		if !unicode.IsLetter(letter) {
			return -1
		}
		return unicode.ToLower(letter)
	}, name)
	return propertyTypesByName[simplified]
}
