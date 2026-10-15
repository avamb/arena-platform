package geotz

// search.go is the type-ahead behind GET /v1/geo/cities?country=&q=: a typed
// beginning of a city name, in Latin, Cyrillic or Hebrew script, becomes up to
// SearchLimit places with the zone each one is in. Like every other lookup in
// this package it only suggests - the caller shows the result as buttons or a
// list and nothing is stored from it.

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SearchLimit caps the number of matches one search returns.
const SearchLimit = 20

// CityMatch is one suggested place.
type CityMatch struct {
	// Name is the place's display name: the one the top-cities list carries
	// when the place is on it, else its main name in a capitalised Latin form.
	Name string
	// Timezone is the IANA zone of the place. It is "" only for a place of a
	// country the data does not know a zone for.
	Timezone string
}

// zoneOfCountry is the zone every place of a single-zone country is in, "" for
// a country with several zones or one the data does not know.
func zoneOfCountry(iso2 string) string {
	if z := ForCountry(iso2); z != "" {
		return z
	}
	if zs := ZonesFor(iso2); len(zs) == 1 {
		return zs[0].ID
	}
	return ""
}

// SingleZone is the zone of a country that has exactly one, "" otherwise.
func SingleZone(iso2 string) string {
	loadOnce.Do(load)
	return zoneOfCountry(iso2)
}

// SearchCities returns the places of the country whose normalised name (or a
// normalised alias, so "мадр" finds Madrid) starts with the normalised query,
// the biggest cities first and the rest alphabetically, at most SearchLimit.
// A blank query, an unknown country or no match gives nil.
//
// Coverage follows the data file: a country with several zones lists every
// place of 15 000 people or more; a single-zone country lists only its largest
// cities (the top-cities list), which is all the file keeps for it.
func SearchCities(iso2, query string) []CityMatch {
	loadOnce.Do(load)
	q := Normalize(query)
	if q == "" {
		return nil
	}
	cc := strings.ToUpper(strings.TrimSpace(iso2))
	only := zoneOfCountry(cc)

	type hit struct {
		primary string
		name    string
		zone    string
		rank    int // position in the top list, huge when not on it
	}
	topRank := map[string]int{} // normalised top name -> position
	topName := map[string]string{}
	for i, n := range topBy[cc] {
		k := Normalize(n)
		if _, dup := topRank[k]; !dup {
			topRank[k], topName[k] = i, n
		}
	}
	byPrimary := map[string]*hit{}
	add := func(primary, zone string) {
		if byPrimary[primary] != nil {
			return
		}
		h := &hit{primary: primary, zone: zone, rank: 1 << 30}
		if r, ok := topRank[primary]; ok {
			h.rank, h.name = r, topName[primary]
		} else {
			h.name = displayName(primary)
		}
		byPrimary[primary] = h
	}
	for alias, e := range cityZone[cc] {
		if strings.HasPrefix(alias, q) {
			add(e.primary, e.zone)
		}
	}
	for k := range topRank {
		if !strings.HasPrefix(k, q) || byPrimary[k] != nil {
			continue
		}
		zone := ForCity(cc, k)
		if zone == "" {
			zone = only
		}
		add(k, zone)
	}
	hits := make([]*hit, 0, len(byPrimary))
	for _, h := range byPrimary {
		if h.zone == "" {
			h.zone = only
		}
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].primary < hits[j].primary
	})
	if len(hits) > SearchLimit {
		hits = hits[:SearchLimit]
	}
	if len(hits) == 0 {
		return nil
	}
	out := make([]CityMatch, 0, len(hits))
	for _, h := range hits {
		out = append(out, CityMatch{Name: h.name, Timezone: h.zone})
	}
	return out
}

// displayName turns a normalised name back into something readable: each word
// capitalised. The accents are gone for good, which is why a place on the
// top-cities list keeps its own spelling.
func displayName(norm string) string {
	words := strings.Fields(norm)
	for i, w := range words {
		r, size := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[size:]
	}
	return strings.Join(words, " ")
}
