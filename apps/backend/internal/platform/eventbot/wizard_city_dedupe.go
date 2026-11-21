package eventbot

// wizard_city_dedupe.go — the city question must not show one city twice.
//
// The platform's own cities (the venue directory and the reference) lead, and
// the country's biggest cities from the embedded geotz data follow as
// suggestions. The two sources spell a city differently («Прага», «Praha»,
// «Prague»), so a suggestion is dropped when it is the same PLACE as a platform
// city: the same normalised name under any known name of the platform city, or
// the same place of the geotz data (which knows a city's names in several
// scripts). A platform city is never dropped.

import (
	"context"
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// knownCities is the set of platform cities of one country seen as names and
// as places of the geotz data.
type knownCities struct {
	iso2   string
	names  map[string]bool // normalised names
	places map[string]bool // geotz place keys
}

func newKnownCities(iso2 string) *knownCities {
	return &knownCities{iso2: iso2, names: map[string]bool{}, places: map[string]bool{}}
}

// add records one name of a platform city.
func (k *knownCities) add(name string) {
	n := geotz.Normalize(name)
	if n == "" {
		return
	}
	k.names[n] = true
	if p, ok := geotz.CityPoint(k.iso2, name); ok {
		k.places[placeKey(p)] = true
	}
}

func placeKey(p geotz.Place) string {
	return fmt.Sprintf("%s|%.4f|%.4f", p.Name, p.Lat, p.Lon)
}

// has reports whether a suggested name is a platform city under any name.
func (k *knownCities) has(name string) bool {
	if k.names[geotz.Normalize(name)] {
		return true
	}
	if p, ok := geotz.CityPoint(k.iso2, name); ok {
		return k.places[placeKey(p)]
	}
	return false
}

// platformCityNames gathers the names the platform has for the cities of the
// country: the directory's (user's language), the reference in the user's
// language and in English. A failed read only means fewer names to compare.
func (w *Wizard) platformCityNames(ctx context.Context, ws WizSession, s *DraftSession, dirCities []DirCity, cities []RefItem) *knownCities {
	k := newKnownCities(s.CountryISO2)
	for _, c := range dirCities {
		k.add(c.Name)
	}
	for _, c := range cities {
		k.add(c.Name)
	}
	if !s.outsideReference() && s.CountryID != "" && !strings.EqualFold(NormalizeLocale(ws.Locale), "en") {
		if en, err := w.refs.Cities(ctx, ws.JWT, s.CountryID, "en"); err == nil {
			for _, c := range en {
				k.add(c.Name)
			}
		}
	}
	return k
}
