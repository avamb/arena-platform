package geotz

// city.go answers the question the country table cannot: which zone is THIS
// city in, and which zones does a many-zone country have. The data is
// GeoNames' cities15000 (every city above 15 000 people, CC BY 4.0, see
// NOTICE) condensed by ops/geotz/build_cities.py and embedded, so the bot
// needs no network call and no model to suggest a zone.
//
// A suggestion is only ever offered as a button the person confirms — a wrong
// zone shifts every time a buyer reads, so nothing here is applied silently.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

//go:embed cities.tsv.gz
var citiesGz []byte

// Zone is one IANA zone of a country with the country's largest city in it,
// which is what the zone's button is labelled with.
type Zone struct {
	ID   string
	City string
}

var (
	loadOnce sync.Once
	zonesBy  map[string][]Zone            // ISO2 -> zones, the zone of the biggest city first
	cityZone map[string]map[string]string // ISO2 -> normalised city name -> zone
	topBy    map[string][]string          // ISO2 -> the largest cities, biggest first
)

func load() {
	zonesBy = map[string][]Zone{}
	cityZone = map[string]map[string]string{}
	topBy = map[string][]string{}
	zr, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		return
	}
	defer func() { _ = zr.Close() }()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) >= 3 && p[0] == "T" {
			topBy[p[1]] = append(topBy[p[1]], p[2])
			continue
		}
		if len(p) != 4 {
			continue
		}
		kind, cc, zone, rest := p[0], p[1], p[2], p[3]
		switch kind {
		case "Z":
			zonesBy[cc] = append(zonesBy[cc], Zone{ID: zone, City: rest})
		case "C":
			m := cityZone[cc]
			if m == nil {
				m = map[string]string{}
				cityZone[cc] = m
			}
			for _, name := range strings.Split(rest, "|") {
				if _, taken := m[name]; !taken { // the biggest city comes first and wins
					m[name] = zone
				}
			}
		}
	}
}

// Normalize folds a place name the way the data file was built: lower case,
// accents dropped, every run of non letters and digits one space. It must stay
// identical to normalize() in ops/geotz/build_cities.py.
func Normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// a combining mark: drop it
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// ForCity returns the zone of the named city in the country (ISO-3166-1
// alpha-2), or "" when the country has one zone only (the caller then uses
// ForCountry or ZonesFor), the city is not in the data, or the name is blank.
// The name may be in Latin, Cyrillic or Hebrew script.
func ForCity(iso2, name string) string {
	loadOnce.Do(load)
	n := Normalize(name)
	if n == "" {
		return ""
	}
	return cityZone[strings.ToUpper(strings.TrimSpace(iso2))][n]
}

// ZonesFor lists the zones of a country that is not in the single-zone table,
// the zone of its biggest city first. It is nil for a country the data does not
// know, and has one entry for a country with a single zone.
func ZonesFor(iso2 string) []Zone {
	loadOnce.Do(load)
	return zonesBy[strings.ToUpper(strings.TrimSpace(iso2))]
}

// CityOf returns the largest city of a zone in the country, for a label.
func CityOf(iso2, zone string) string {
	for _, z := range ZonesFor(iso2) {
		if z.ID == zone {
			return z.City
		}
	}
	return ""
}

// TopCities lists the largest cities of the country, biggest first, for the
// cities the bot offers as buttons. It is nil for a country outside the ones
// the data covers (Europe, the United States, Mexico, South America, Israel).
func TopCities(iso2 string) []string {
	loadOnce.Do(load)
	return topBy[strings.ToUpper(strings.TrimSpace(iso2))]
}
