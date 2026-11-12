// Package worldgeo is the embedded table of every ISO 3166-1 country: codes,
// the national currency (ISO 4217), one of seven regions and the English and
// Russian names.
//
// The arena country reference (the countries table, GET /v1/geo/countries)
// holds only the countries arena works in. This table is the world the
// clients pick from (GET /v1/geo/world-countries), and Ensure turns one of
// its entries into a reference row when a venue or an approved organizer
// application needs a country the reference does not have yet (spec 64
// section 2.1.1). Where migration 0127 seeded a country, the table agrees
// with it on iso3, currency and region (TestTable_MatchesMigration0127).
//
// Region placement that is a choice, not geography: Armenia, Georgia, Cyprus
// and Russia are europe; Kazakhstan and Azerbaijan are asia; Turkey and Israel
// are middle_east; Egypt is africa; the Caribbean is latin_america; the
// uninhabited territories go with the nearest region (Antarctica and Heard
// Island oceania, Bouvet Island europe, French Southern Territories africa,
// South Georgia latin_america).
package worldgeo

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

// The seven regions, in the order a client shows them.
const (
	RegionEurope       = "europe"
	RegionMiddleEast   = "middle_east"
	RegionAsia         = "asia"
	RegionAfrica       = "africa"
	RegionNorthAmerica = "north_america"
	RegionLatinAmerica = "latin_america"
	RegionOceania      = "oceania"
)

var regions = []string{RegionEurope, RegionMiddleEast, RegionAsia, RegionAfrica, RegionNorthAmerica, RegionLatinAmerica, RegionOceania}

// Regions lists the seven regions in display order.
func Regions() []string { return append([]string(nil), regions...) }

// ValidRegion reports whether r is one of the seven regions.
func ValidRegion(r string) bool {
	for _, x := range regions {
		if x == r {
			return true
		}
	}
	return false
}

// Country is one entry of the world table.
type Country struct {
	Iso2     string
	Iso3     string
	Currency string
	Region   string
	NameEn   string
	NameRu   string
}

// Name is the country's name in the locale: Russian for "ru", English for
// anything else.
func (c Country) Name(locale string) string {
	if strings.EqualFold(strings.TrimSpace(locale), "ru") && c.NameRu != "" {
		return c.NameRu
	}
	return c.NameEn
}

//go:embed countries.tsv
var countriesTSV string

var (
	table  []Country
	byIso2 map[string]Country
	// parseErr is a broken embedded table; the table is then empty and
	// TestTable_SizeCodesAndRegions fails the build.
	parseErr error
)

func init() {
	table, parseErr = parse(countriesTSV)
	byIso2 = make(map[string]Country, len(table))
	for _, c := range table {
		byIso2[c.Iso2] = c
	}
}

// parse reads the embedded table: one country per line, six tab-separated
// fields iso2, iso3, currency, region, name_en, name_ru.
func parse(raw string) ([]Country, error) {
	var out []Country
	for i, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			return nil, fmt.Errorf("worldgeo: line %d has %d fields, want 6", i+1, len(f))
		}
		out = append(out, Country{Iso2: f[0], Iso3: f[1], Currency: f[2], Region: f[3], NameEn: f[4], NameRu: f[5]})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Iso2 < out[b].Iso2 })
	return out, nil
}

// All returns a copy of the table, ordered by iso2.
func All() []Country { return append([]Country(nil), table...) }

// Lookup finds a country by its alpha-2 code (any case, space trimmed).
func Lookup(iso2 string) (Country, bool) {
	c, ok := byIso2[strings.ToUpper(strings.TrimSpace(iso2))]
	return c, ok
}

// fallbackZones are the IANA zones of the territories the embedded city data
// of geotz has no place in (no city of 15 000 people, or nobody lives there),
// so that GET /v1/geo/countries/{iso2}/timezones answers for every country of
// the table. TestEveryCountryHasAZone keeps the two together.
var fallbackZones = map[string][]string{
	"AQ": {"Antarctica/McMurdo", "Antarctica/Casey", "Antarctica/Davis", "Antarctica/Mawson", "Antarctica/Palmer", "Antarctica/Rothera", "Antarctica/Syowa", "Antarctica/Troll", "Antarctica/Vostok"},
	"BV": {"Europe/Oslo"},
	"HM": {"Indian/Kerguelen"},
	"IO": {"Indian/Chagos"},
	"TK": {"Pacific/Fakaofo"},
	"UM": {"Pacific/Midway", "Pacific/Wake"},
}

// FallbackZones returns the zones of a territory the city data does not
// cover, nil for any other code.
func FallbackZones(iso2 string) []string {
	return append([]string(nil), fallbackZones[strings.ToUpper(strings.TrimSpace(iso2))]...)
}
