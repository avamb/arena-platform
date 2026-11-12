package seating

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// Synthetic plans for the key resolution of key_resolve.go. Every plan is
// written inline; no client data.

// kGroup is one row group of a synthetic plan: a sector name, a row name
// and the seat numbers in document order.
type kGroup struct {
	sector  string
	row     string
	numbers []string
}

const (
	dialectAuthoring = "authoring"
	dialectSBT       = "sbt"
)

var keyDialects = []string{dialectAuthoring, dialectSBT}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// buildKeyPlan renders groups as an SVG of the given dialect. Seat i of the
// whole plan sits at x = 10 + 2*i, so a test can map a seat back to its
// group by its X.
func buildKeyPlan(dialect string, groups []kGroup) []byte {
	var b strings.Builder
	seq := 0
	switch dialect {
	case dialectAuthoring:
		b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 2000 1000">`)
		b.WriteString(`<g id="PriceCategory"><circle inkscape:label="#First" style="fill:#ff0000" cx="1" cy="1" r="1"/></g>`)
		b.WriteString(`<g id="Legend"><circle cx="1" cy="1" r="1"/></g>`)
		for _, g := range groups {
			fmt.Fprintf(&b, `<g inkscape:label="#%s"><title>%s</title>`, xmlEsc(g.sector), xmlEsc(g.row))
			for _, n := range g.numbers {
				fmt.Fprintf(&b, `<circle style="fill:#ff0000" cx="%d" cy="20" r="1"><title>%s</title></circle>`, 10+2*seq, xmlEsc(n))
				seq++
			}
			b.WriteString(`</g>`)
		}
	case dialectSBT:
		b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 2000 1000">`)
		b.WriteString(`<metadata><sbt:category sbt:id="7" sbt:index="1" sbt:name="First" sbt:color="#ff0000" sbt:price="10"/></metadata>`)
		for _, g := range groups {
			fmt.Fprintf(&b, `<g sbt:sect="%s"><g sbt:row="%s">`, xmlEsc(g.sector), xmlEsc(g.row))
			for _, n := range g.numbers {
				fmt.Fprintf(&b, `<circle sbt:id="%d" sbt:state="1" sbt:cat="1" sbt:seat="%s" cx="%d" cy="20" r="1"/>`, 100+seq, xmlEsc(n), 10+2*seq)
				seq++
			}
			b.WriteString(`</g></g>`)
		}
	default:
		panic(dialect)
	}
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func importKeyPlan(t *testing.T, dialect string, groups []kGroup) (Geometry, []ValidationError, ValidationErrors) {
	t.Helper()
	raw := buildKeyPlan(dialect, groups)
	if dialect == dialectSBT {
		plan, w, e := ImportSBTSVG(raw)
		return plan.Geometry, w, e
	}
	return ImportSVG(raw)
}

// seatRef is one imported seat with the names of the group it came from.
type seatRef struct {
	sector, row, number string
	key                 string
	secKey, rowKey      string
	secName, rowName    string
}

// seatsByGroup maps every imported seat back to the group that wrote it.
func seatsByGroup(t *testing.T, g Geometry, groups []kGroup) []seatRef {
	t.Helper()
	type origin struct{ sector, row, number string }
	byX := map[float64]origin{}
	seq := 0
	for _, gr := range groups {
		for _, n := range gr.numbers {
			byX[float64(10+2*seq)] = origin{gr.sector, gr.row, n}
			seq++
		}
	}
	var out []seatRef
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				o, ok := byX[s.X]
				if !ok {
					t.Fatalf("seat %q at x=%g maps to no group", s.Key, s.X)
				}
				out = append(out, seatRef{
					sector: o.sector, row: o.row, number: o.number, key: s.Key,
					secKey: sec.Key, rowKey: row.Key, secName: sec.Name, rowName: row.Name,
				})
			}
		}
	}
	return out
}

func numbers(ns ...string) []string { return ns }

func mustNoErrors(t *testing.T, errs ValidationErrors) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func countCode(list []ValidationError, code string) int {
	n := 0
	for _, e := range list {
		if e.Code == code {
			n++
		}
	}
	return n
}

// sectionByName returns the section whose Name is name.
func sectionByName(t *testing.T, g Geometry, name string) Section {
	t.Helper()
	for _, s := range g.Sections {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no section named %q in %+v", name, g.Sections)
	return Section{}
}

func assertUniqueSeatKeys(t *testing.T, g Geometry) {
	t.Helper()
	seen := map[string]bool{}
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				if seen[s.Key] {
					t.Fatalf("duplicate seat key %q", s.Key)
				}
				seen[s.Key] = true
				for _, r := range sec.Key + row.Key {
					if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
						t.Fatalf("key part %q/%q holds %q", sec.Key, row.Key, r)
					}
				}
			}
		}
	}
}

func TestKeyResolve_CyrillicSectorsOverlap(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"Партер", "1", numbers("1", "2", "3")},
				{"Балкон", "1", numbers("1", "2", "3")},
			}
			g, w, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			if countCode(w, WarnNamesShareKey) != 0 {
				t.Errorf("conflicted names must not warn as merged: %v", w)
			}
			if len(g.Sections) != 2 {
				t.Fatalf("sections = %d, want 2", len(g.Sections))
			}
			p, bk := sectionByName(t, g, "Партер"), sectionByName(t, g, "Балкон")
			if p.Key != nameHash("Партер") || bk.Key != nameHash("Балкон") || p.Key == bk.Key {
				t.Errorf("keys = %q %q, want the bare name hashes", p.Key, bk.Key)
			}
			if p.Rows[0].Seats[0].Key != SeatKey(p.Key, "1", "1") {
				t.Errorf("seat key = %q", p.Rows[0].Seats[0].Key)
			}
			assertUniqueSeatKeys(t, g)
			if g.SeatCount() != 6 {
				t.Errorf("seats = %d, want 6", g.SeatCount())
			}
		})
	}
}

func TestKeyResolve_RowsSevenAndStarSeven(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"A", "7", numbers("1", "2")},
				{"A", "*7", numbers("1", "2")},
			}
			g, _, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			sec := sectionByName(t, g, "A")
			if sec.Key != "a" {
				t.Errorf("sector key = %q, want legacy %q", sec.Key, "a")
			}
			got := map[string]string{}
			for _, r := range sec.Rows {
				got[r.Name] = r.Key
			}
			want := map[string]string{"7": "7-" + nameHash("7"), "*7": "7-" + nameHash("*7")}
			for n, k := range want {
				if got[n] != k {
					t.Errorf("row %q key = %q, want %q", n, got[n], k)
				}
			}
			assertUniqueSeatKeys(t, g)
			if g.SeatCount() != 4 {
				t.Errorf("seats = %d, want 4", g.SeatCount())
			}
		})
	}
}

func TestKeyResolve_VIPandVIPPlus(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"VIP", "1", numbers("1", "2")},
				{"VIP+", "1", numbers("2", "3")},
			}
			g, _, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			if k := sectionByName(t, g, "VIP").Key; k != "vip-"+nameHash("VIP") {
				t.Errorf("VIP key = %q", k)
			}
			if k := sectionByName(t, g, "VIP+").Key; k != "vip-"+nameHash("VIP+") {
				t.Errorf("VIP+ key = %q", k)
			}
			assertUniqueSeatKeys(t, g)
		})
	}
}

func TestKeyResolve_CaseOnlyNamesCollideOnSeats(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"VYCHOD", "1", numbers("1", "2")},
				{"Vychod", "1", numbers("1", "2")},
			}
			g, _, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			a, b := sectionByName(t, g, "VYCHOD"), sectionByName(t, g, "Vychod")
			if a.Key != "vychod-"+nameHash("VYCHOD") || b.Key != "vychod-"+nameHash("Vychod") {
				t.Errorf("keys = %q %q", a.Key, b.Key)
			}
			assertUniqueSeatKeys(t, g)
		})
	}
}

func TestKeyResolve_MergedWithoutCollisionKeepsLegacyAndWarns(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"VYCHOD", "1", numbers("1", "2")},
				{"Vychod", "2", numbers("1", "2")},
			}
			g, w, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			if len(g.Sections) != 1 || g.Sections[0].Key != "vychod" || g.Sections[0].Name != "VYCHOD" {
				t.Fatalf("sections = %+v, want one merged %q named after the first group", g.Sections, "vychod")
			}
			for _, r := range g.Sections[0].Rows {
				for _, s := range r.Seats {
					if s.Key != SeatKey("vychod", r.Key, s.Number) || r.Key != normalizeKey(r.Name) {
						t.Errorf("seat key %q is not legacy", s.Key)
					}
				}
			}
			if countCode(w, WarnNamesShareKey) != 1 {
				t.Fatalf("warnings = %v, want one %s", w, WarnNamesShareKey)
			}
			for _, x := range w {
				if x.Code == WarnNamesShareKey {
					if x.Element != "vychod" || !strings.Contains(x.Detail, `"VYCHOD", "Vychod"`) {
						t.Errorf("warning = %+v", x)
					}
				}
			}
		})
	}
}

func TestKeyResolve_SingleCyrillicSectorKeepsEmptyKey(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"Партер", "1", numbers("1", "2")},
				{"Партер", "2", numbers("1", "2")},
			}
			g, w, errs := importKeyPlan(t, d, groups)
			mustNoErrors(t, errs)
			if len(w) != 0 {
				t.Errorf("warnings = %v", w)
			}
			if len(g.Sections) != 1 || g.Sections[0].Key != "" || g.Sections[0].Name != "Партер" {
				t.Fatalf("sections = %+v", g.Sections)
			}
			if k := g.Sections[0].Rows[0].Seats[0].Key; k != "|1|1" {
				t.Errorf("seat key = %q, want %q", k, "|1|1")
			}
		})
	}
}

func TestKeyResolve_TrueDuplicateStillFails(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			groups := []kGroup{
				{"Партер", "1", numbers("1", "2")},
				{"Партер", "1", numbers("2", "3")},
			}
			_, w, errs := importKeyPlan(t, d, groups)
			if countCode(errs, ErrDuplicateSeat) != 1 || len(errs) != 1 {
				t.Fatalf("errs = %v, want one %s", errs, ErrDuplicateSeat)
			}
			if errs[0].Element != "|1|2" {
				t.Errorf("element = %q, want the legacy triple", errs[0].Element)
			}
			if countCode(w, WarnNamesShareKey) != 0 {
				t.Errorf("warnings = %v", w)
			}
		})
	}
}

func TestKeyResolve_SuffixCollisionIsAnError(t *testing.T) {
	t.Parallel()
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			// "A" and "a" collide on seats, so "A" gets "a-<hash(A)>". A
			// third sector literally named that takes the same key.
			groups := []kGroup{
				{"A", "1", numbers("1")},
				{"a", "1", numbers("1")},
				{"a-" + nameHash("A"), "1", numbers("1")},
			}
			_, _, errs := importKeyPlan(t, d, groups)
			if countCode(errs, ErrNameKeyCollision) != 1 {
				t.Fatalf("errs = %v, want one %s", errs, ErrNameKeyCollision)
			}
		})
	}
}

func TestKeyResolve_Deterministic(t *testing.T) {
	t.Parallel()
	groups := []kGroup{
		{"Партер", "1", numbers("1", "2")},
		{"Балкон", "1", numbers("1", "2")},
		{"A", "7", numbers("1")},
		{"A", "*7", numbers("1")},
		{"VYCHOD", "1", numbers("1")},
		{"Vychod", "2", numbers("1")},
	}
	reversed := make([]kGroup, len(groups))
	for i, g := range groups {
		reversed[len(groups)-1-i] = g
	}
	for _, d := range keyDialects {
		d := d
		t.Run(d, func(t *testing.T) {
			g1, _, e1 := importKeyPlan(t, d, groups)
			g2, _, e2 := importKeyPlan(t, d, groups)
			mustNoErrors(t, e1)
			mustNoErrors(t, e2)
			j1, _ := CanonicalJSON(g1)
			j2, _ := CanonicalJSON(g2)
			if !bytes.Equal(j1, j2) {
				t.Fatal("same input imported twice differs")
			}
			g3, _, e3 := importKeyPlan(t, d, reversed)
			mustNoErrors(t, e3)
			keyOf := func(refs []seatRef) []string {
				var out []string
				for _, r := range refs {
					out = append(out, r.sector+"\x00"+r.row+"\x00"+r.number+"\x00"+r.key)
				}
				sort.Strings(out)
				return out
			}
			a, b := keyOf(seatsByGroup(t, g1, groups)), keyOf(seatsByGroup(t, g3, reversed))
			if strings.Join(a, "\n") != strings.Join(b, "\n") {
				t.Fatalf("reversed document order changed keys:\n%v\n%v", a, b)
			}
		})
	}
}

// legacySeatKey is the seat key every importer derived before
// key_resolve.go existed. Test-only copy; do not reuse it in code.
func legacySeatKey(sector, row, number string) string {
	return SeatKey(normalizeKey(sector), normalizeKey(row), number)
}

func TestKeyResolve_PropertyLegacyInvariant(t *testing.T) {
	t.Parallel()
	sectorPool := []string{"A", "a", "A.", "Партер", "Балкон", "VIP", "VIP+", "Ложа 1", "ложа-1", "B", "Сектор Б"}
	rowPool := []string{"1", "*1", "1.", "2", "Ряд", "ряд", "I", "i"}
	numberPool := []string{"1", "2", "3", "4"}
	rng := rand.New(rand.NewSource(20261112))

	legacyPlans, collidedPlans := 0, 0
	for i := 0; i < 400; i++ {
		var groups []kGroup
		for n := 1 + rng.Intn(5); n > 0; n-- {
			var nums []string
			used := map[string]bool{}
			for m := 1 + rng.Intn(3); m > 0; m-- {
				num := numberPool[rng.Intn(len(numberPool))]
				if !used[num] {
					used[num] = true
					nums = append(nums, num)
				}
			}
			groups = append(groups, kGroup{
				sector:  sectorPool[rng.Intn(len(sectorPool))],
				row:     rowPool[rng.Intn(len(rowPool))],
				numbers: nums,
			})
		}

		// What the plan holds: distinct name triples, and whether the
		// legacy triples were unique among them.
		nameTriples := map[[3]string]bool{}
		legacyOwners := map[string]map[[3]string]bool{}
		trueDup := false
		for _, g := range groups {
			for _, num := range g.numbers {
				nt := [3]string{g.sector, g.row, num}
				if nameTriples[nt] {
					trueDup = true
				}
				nameTriples[nt] = true
				lk := legacySeatKey(g.sector, g.row, num)
				if legacyOwners[lk] == nil {
					legacyOwners[lk] = map[[3]string]bool{}
				}
				legacyOwners[lk][nt] = true
			}
		}
		legacyUnique := !trueDup
		for _, owners := range legacyOwners {
			if len(owners) > 1 {
				legacyUnique = false
			}
		}
		if legacyUnique {
			legacyPlans++
		} else {
			collidedPlans++
		}

		for _, d := range keyDialects {
			g, w, errs := importKeyPlan(t, d, groups)
			for _, e := range errs {
				// A row group made only of true duplicates is also empty.
				if e.Code != ErrDuplicateSeat && e.Code != ErrEmptyRow {
					t.Fatalf("plan %d %s: unexpected error %v in %+v", i, d, e, groups)
				}
			}
			if (len(errs) > 0) != trueDup {
				t.Fatalf("plan %d %s: errs %v, true duplicate %v", i, d, errs, trueDup)
			}
			refs := seatsByGroup(t, g, groups)
			if len(refs) != len(nameTriples) {
				t.Fatalf("plan %d %s: seats %d, want %d distinct name triples", i, d, len(refs), len(nameTriples))
			}
			seen := map[string][3]string{}
			for _, r := range refs {
				nt := [3]string{r.sector, r.row, r.number}
				if prev, dup := seen[r.key]; dup && prev != nt {
					t.Fatalf("plan %d %s: %v and %v share key %q", i, d, prev, nt, r.key)
				}
				seen[r.key] = nt
				if legacyUnique {
					if r.key != legacySeatKey(r.sector, r.row, r.number) {
						t.Fatalf("plan %d %s: key %q, legacy %q", i, d, r.key, legacySeatKey(r.sector, r.row, r.number))
					}
					if r.secKey != normalizeKey(r.secName) || r.rowKey != normalizeKey(r.rowName) {
						t.Fatalf("plan %d %s: section/row keys %q/%q are not legacy", i, d, r.secKey, r.rowKey)
					}
				}
			}
			if legacyUnique {
				for _, x := range w {
					if x.Code != WarnNamesShareKey {
						t.Fatalf("plan %d %s: unexpected warning %v", i, d, x)
					}
				}
			}
		}
	}
	if legacyPlans < 50 || collidedPlans < 50 {
		t.Fatalf("generator is lopsided: %d legacy-unique, %d collided plans", legacyPlans, collidedPlans)
	}
}
