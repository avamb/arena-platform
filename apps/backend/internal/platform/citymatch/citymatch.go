// Package citymatch is the ONE place that decides whether a city a caller names
// is already in a country of the reference. Every find-or-create of a city
// (the org city route, a venue created with its city, the event-bundle import,
// the Bil24 venue loader) asks FindInCountry before it inserts, so "Praha",
// "Prague" and "Прага" end up on one row instead of two.
//
// A candidate city matches an existing one of the SAME country when any of
// these holds:
//
//   - the slug the name would produce is the existing slug;
//   - the name equals, after geotz.Normalize (case, accents and punctuation
//     folded), the stored name of the existing city in ANY locale, or its slug;
//   - both resolve to the same place of the embedded GeoNames data
//     (geotz.CityPoint, a Latin, Cyrillic or Hebrew spelling) or to the same
//     well-known exonym pair of the small alias table below.
//
// The embedded data holds the ASCII GeoNames name plus its alternate names for
// cities of 15 000 people or more; for Czech cities it knows "Prague" but NOT
// "Praha" or "Прага", which is why the alias table exists.
package citymatch

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/geoslug"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
)

// DB is what FindInCountry needs; pgx.Tx and *pgxpool.Pool satisfy it. Pass
// the caller's transaction so the read sees the caller's own writes.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// aliasGroup is the spellings of one city that the geo data does not tie
// together (endonym and exonym, folded with geotz.Normalize). It applies ONLY
// in its own country: "Roma" is Rome in IT but a different town in US.
type aliasGroup struct {
	iso2  string
	names []string
}

var aliasGroups = []aliasGroup{
	{"CZ", []string{"praha", "prague", "praga", "прага"}},
	{"AT", []string{"wien", "vienna", "vena", "вена"}},
	{"PL", []string{"warszawa", "warsaw", "varsavia", "варшава"}},
	{"IT", []string{"roma", "rome", "рим"}},
	{"DE", []string{"koln", "cologne", "кельн"}},
	{"DE", []string{"munchen", "munich", "мюнхен"}},
	{"PT", []string{"lisboa", "lisbon", "лиссабон"}},
	{"DK", []string{"kobenhavn", "copenhagen", "копенгаген"}},
	{"UA", []string{"kyiv", "kiev", "киев", "київ"}},
	{"EE", []string{"tallinn", "таллин", "таллинн"}},
	{"IL", []string{"yerushalayim", "jerusalem", "иерусалим"}},
	{"IL", []string{"tel aviv", "tel aviv yafo", "tel aviv jaffa", "тель авив"}},
}

// aliasIndex: ISO2 -> folded spelling -> the group's spellings.
var aliasIndex = func() map[string]map[string][]string {
	m := map[string]map[string][]string{}
	for _, g := range aliasGroups {
		if m[g.iso2] == nil {
			m[g.iso2] = map[string][]string{}
		}
		for _, n := range g.names {
			m[g.iso2][geotz.Normalize(n)] = g.names
		}
	}
	return m
}()

// aliasesOf returns the group of a folded spelling in the country; an empty or
// unknown iso2 applies no alias.
func aliasesOf(iso2, folded string) []string {
	return aliasIndex[strings.ToUpper(strings.TrimSpace(iso2))][folded]
}

// Keys returns the comparison keys of one spelling in one country: its folded
// form, the folded forms of its alias group, and the ASCII name of the place the
// geo data resolves it to. Two spellings of one city share at least one key.
func Keys(iso2, name string) map[string]struct{} {
	out := map[string]struct{}{}
	n := geotz.Normalize(name)
	if n == "" {
		return out
	}
	out[n] = struct{}{}
	for _, a := range aliasesOf(iso2, n) {
		out[geotz.Normalize(a)] = struct{}{}
	}
	if p, ok := geotz.CityPoint(iso2, name); ok {
		pn := geotz.Normalize(p.Name)
		out[pn] = struct{}{}
		for _, a := range aliasesOf(iso2, pn) {
			out[geotz.Normalize(a)] = struct{}{}
		}
	}
	return out
}

// Existing is one city row of the country with every name stored for it.
type Existing struct {
	ID    uuid.UUID
	Slug  string
	Names []string
}

// Match picks the existing city the name refers to, or false. A slug hit wins,
// then the first row in the given order (callers pass oldest first).
func Match(iso2, name string, existing []Existing) (Existing, bool) {
	slug := geoslug.Slugify(name)
	if slug != "" {
		for _, e := range existing {
			if e.Slug == slug {
				return e, true
			}
		}
	}
	want := Keys(iso2, name)
	if len(want) == 0 {
		return Existing{}, false
	}
	for _, e := range existing {
		spellings := append([]string{e.Slug}, e.Names...)
		for _, s := range spellings {
			for k := range Keys(iso2, s) {
				if _, hit := want[k]; hit {
					return e, true
				}
			}
		}
	}
	return Existing{}, false
}

// FindInCountry looks for a city of the country that the name already refers
// to, by slug, by any stored name in any locale, or by the same place. found is
// false when the country has no such city and a new one may be inserted.
func FindInCountry(ctx context.Context, db DB, countryID uuid.UUID, iso2, name string) (Existing, bool, error) {
	rows, err := db.Query(ctx, `
SELECT ci.id, ci.slug, COALESCE(t.value, '')
FROM   cities ci
LEFT   JOIN i18n_text t ON t.namespace = 'geo.cities' AND t.key = ci.slug
WHERE  ci.country_id = $1
ORDER  BY ci.created_at, ci.id`, countryID)
	if err != nil {
		return Existing{}, false, err
	}
	defer rows.Close()
	byID := map[uuid.UUID]*Existing{}
	var order []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var slug, value string
		if err := rows.Scan(&id, &slug, &value); err != nil {
			return Existing{}, false, err
		}
		e := byID[id]
		if e == nil {
			e = &Existing{ID: id, Slug: slug}
			byID[id] = e
			order = append(order, id)
		}
		if value != "" {
			e.Names = append(e.Names, value)
		}
	}
	if err := rows.Err(); err != nil {
		return Existing{}, false, err
	}
	list := make([]Existing, 0, len(order))
	for _, id := range order {
		list = append(list, *byID[id])
	}
	e, ok := Match(iso2, name, list)
	return e, ok, nil
}
