// key_resolve.go decides the Section.Key / Row.Key of every sector and row
// name of one imported plan. Both importers (ImportSVG and ImportSBTSVG)
// share it.
//
// The legacy key of a name is normalizeKey(name): lowercase, keep a-z and
// 0-9, collapse everything else into one "-". Names that differ only in
// non-Latin letters, case or punctuation ("Партер" and "Балкон", "7" and
// "*7", "VIP" and "VIP+") therefore share a legacy key. Seat keys are
// stored in session_seats.seat_key of live sessions, so the rule below
// changes a key ONLY where the legacy key could not tell two real groups
// of seats apart:
//
//  1. Sector step. Distinct sector names are grouped by legacy key. A
//     group is CONFLICTED when it holds 2+ distinct names AND two seats of
//     DIFFERENT names of the group share the legacy triple
//     (normalizeKey(sector), normalizeKey(row), number). Every name of a
//     conflicted group gets legacyKey + "-" + h (or just h when the legacy
//     key is empty), where h is the first 8 hex characters of
//     sha256(name). A non-conflicted group keeps the legacy key, so its
//     names still merge into one section, and a group of 2+ names gets one
//     WarnNamesShareKey warning.
//  2. Row step, inside each final sector key: the same rule on row names,
//     a group being conflicted when two seats of different row names share
//     one number.
//  3. Seats with the same sector name, row name and number are a true
//     duplicate (ErrDuplicateSeat in the importer) and never a conflict.
//  4. If a suffixed key still equals the key of another name, the plan is
//     rejected with ErrNameKeyCollision.
//
// A plan without any legacy-triple collision therefore imports exactly as
// before: same keys, same CanonicalJSON.
package seating

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// keySeat is one seat that reached the importer's duplicate check, named by
// the display names the importer stores (Section.Name / Row.Name) and its
// verbatim number.
type keySeat struct {
	sector string
	row    string
	number string
}

// keyResolution maps display names to final keys.
type keyResolution struct {
	sector   map[string]string    // sector name -> final sector key
	row      map[[2]string]string // {final sector key, row name} -> final row key
	warnings []ValidationError
	errs     ValidationErrors
}

// sectorKey returns the final key of a sector name. A name that no
// candidate seat carries falls back to its legacy key.
func (r keyResolution) sectorKey(name string) string {
	if k, ok := r.sector[name]; ok {
		return k
	}
	return normalizeKey(name)
}

// rowKey returns the final key of a row name inside a final sector key.
func (r keyResolution) rowKey(secKey, name string) string {
	if k, ok := r.row[[2]string{secKey, name}]; ok {
		return k
	}
	return normalizeKey(name)
}

// nameHash is the 8-hex-character disambiguator of a conflicted name.
func nameHash(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}

// suffixedKey is the key of a name in a conflicted group.
func suffixedKey(legacy, name string) string {
	h := nameHash(name)
	if legacy == "" {
		return h
	}
	return legacy + "-" + h
}

// nameGroup is one legacy-key group of distinct names.
type nameGroup struct {
	legacy     string
	names      map[string]bool
	conflicted bool
}

func (g *nameGroup) sortedNames() []string {
	out := make([]string, 0, len(g.names))
	for n := range g.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// assignKeys gives every name of every group its final key and returns the
// map, plus an error per pair of different names that still collide.
// scope describes the namespace for messages ("sector" or "row in sector
// key \"x\"").
func assignKeys(groups map[string]*nameGroup, scope string) (map[string]string, []ValidationError, ValidationErrors) {
	legacies := make([]string, 0, len(groups))
	for l := range groups {
		legacies = append(legacies, l)
	}
	sort.Strings(legacies)

	out := map[string]string{}
	var warnings []ValidationError
	var errs ValidationErrors
	// owner of a final key: the group (non-conflicted) or the single name
	// (conflicted). Two different owners of one key is a collision.
	type owner struct {
		group string
		name  string // "" for a whole non-conflicted group
		shown string // a name to quote in the error
	}
	owners := map[string]owner{}
	claim := func(key string, o owner) {
		prev, ok := owners[key]
		if !ok {
			owners[key] = o
			return
		}
		if prev.group == o.group && prev.name == o.name {
			return
		}
		a, b := prev.shown, o.shown
		if b < a {
			a, b = b, a
		}
		errs = append(errs, ValidationError{
			Code:    ErrNameKeyCollision,
			Element: key,
			Detail:  fmt.Sprintf("%s names %q and %q resolve to the same key", scope, a, b),
		})
	}

	for _, l := range legacies {
		g := groups[l]
		names := g.sortedNames()
		if g.conflicted {
			for _, n := range names {
				k := suffixedKey(l, n)
				out[n] = k
				claim(k, owner{group: l, name: n, shown: n})
			}
			continue
		}
		for _, n := range names {
			out[n] = l
		}
		claim(l, owner{group: l, shown: names[0]})
		if len(names) > 1 {
			warnings = append(warnings, ValidationError{
				Code:    WarnNamesShareKey,
				Element: l,
				Detail:  fmt.Sprintf("%s names %s share one key and are merged", scope, quotedList(names)),
			})
		}
	}
	return out, warnings, errs
}

// resolveKeys computes the final sector and row keys for the candidate
// seats of one plan. The result is independent of the seat order.
func resolveKeys(seats []keySeat) keyResolution {
	res := keyResolution{
		sector: map[string]string{},
		row:    map[[2]string]string{},
	}

	// --- sector step ---
	secGroups := map[string]*nameGroup{}
	// legacy triple -> first sector name seen with it
	tripleOwner := map[string]string{}
	for _, s := range seats {
		l := normalizeKey(s.sector)
		g, ok := secGroups[l]
		if !ok {
			g = &nameGroup{legacy: l, names: map[string]bool{}}
			secGroups[l] = g
		}
		g.names[s.sector] = true
		triple := SeatKey(l, normalizeKey(s.row), s.number)
		if prev, seen := tripleOwner[triple]; !seen {
			tripleOwner[triple] = s.sector
		} else if prev != s.sector {
			g.conflicted = true
		}
	}
	for _, g := range secGroups {
		if len(g.names) < 2 {
			g.conflicted = false
		}
	}
	secKeys, w, e := assignKeys(secGroups, "sector")
	res.sector = secKeys
	res.warnings = append(res.warnings, w...)
	res.errs = append(res.errs, e...)

	// --- row step, per final sector key ---
	type rowScope struct {
		groups map[string]*nameGroup
		// {legacy row key, number} -> first row name seen with it
		numberOwner map[[2]string]string
	}
	scopes := map[string]*rowScope{}
	for _, s := range seats {
		fsk := res.sector[s.sector]
		sc, ok := scopes[fsk]
		if !ok {
			sc = &rowScope{groups: map[string]*nameGroup{}, numberOwner: map[[2]string]string{}}
			scopes[fsk] = sc
		}
		l := normalizeKey(s.row)
		g, ok := sc.groups[l]
		if !ok {
			g = &nameGroup{legacy: l, names: map[string]bool{}}
			sc.groups[l] = g
		}
		g.names[s.row] = true
		nk := [2]string{l, s.number}
		if prev, seen := sc.numberOwner[nk]; !seen {
			sc.numberOwner[nk] = s.row
		} else if prev != s.row {
			g.conflicted = true
		}
	}
	fsks := make([]string, 0, len(scopes))
	for k := range scopes {
		fsks = append(fsks, k)
	}
	sort.Strings(fsks)
	for _, fsk := range fsks {
		sc := scopes[fsk]
		for _, g := range sc.groups {
			if len(g.names) < 2 {
				g.conflicted = false
			}
		}
		rowKeys, w, e := assignKeys(sc.groups, fmt.Sprintf("row (sector key %q)", fsk))
		for name, k := range rowKeys {
			res.row[[2]string{fsk, name}] = k
		}
		res.warnings = append(res.warnings, w...)
		res.errs = append(res.errs, e...)
	}
	return res
}
