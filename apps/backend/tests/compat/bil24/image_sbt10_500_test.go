// image_sbt10_500_test.go — feature #500 (W1-B5a).
//
// Pins hseating.RenderSBT10SVG (the encoder behind
// `GET /compat/bil24/image?type=seatingPlan`, spec §8) against the checked-in
// golden testdata/wp/svg/palac_akropolis.sbt.svg, and replays the parsing
// rules of the WordPress seat picker (bil24-seat-picker.js) over the render.
//
// Fixture. The golden is regenerated from the SAME Palác Akropolis plan the
// integration harness seeds (06_venue_maps_and_seating/Palac_Akropolis.svg via
// seating.ImportSVG, seed_test.go:174-291), but the identities the DB would
// hand out — session_seats.system_seat_id, ticket_tiers.id,
// compatibility_id_map ids — are assigned here by a deterministic rule instead
// of by a sequence. That keeps the golden byte-stable and keeps this test in
// the CI Unit job (no DATABASE_URL, no schema): what it guards is the ENCODER,
// not the seeding. The seat-status mix (a sold and a held seat every few
// seats) exists so the golden actually carries both sbt:state values.
//
// The golden keeps the synthetic-plan disclaimer from feature #450 as an XML
// comment; the comparison is canonical-XML (comments and inter-element
// whitespace are dropped, attributes sorted by namespace+local name), so the
// disclaimer survives regeneration.
//
// Regenerating after an intentional encoder change:
//
//	ARENA_REGEN_SBT_GOLDEN=1 go.exe test ./apps/backend/tests/compat/bil24/ -run SBT10
//
// then re-add the disclaimer comment block at the top of the file. Goldens are
// only ever corrected to match the spec text, never to match the code.

package compat_bil24_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hseating"
)

// sbt10GoldenPath is the golden the WordPress contract is pinned to.
var sbt10GoldenPath = filepath.Join("testdata", "wp", "svg", "palac_akropolis.sbt.svg")

// akropolisSVGPath locates the reference plan relative to this file so the
// lookup is independent of the `go test` working directory.
func akropolisSVGPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate the SVG fixture")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", ".."))
	return filepath.Join(repoRoot, "06_venue_maps_and_seating", "Palac_Akropolis.svg")
}

// sbt10Fixture is the deterministic stand-in for one seeded Akropolis
// session: the imported geometry plus the session_seats / ticket_tiers /
// compat-id projections the encoder consumes.
type sbt10Fixture struct {
	geom        seating.Geometry
	seats       []gen.SessionSeatRow
	tiers       []gen.TicketTierRow
	categoryIDs map[int]int64
	statusVer   int64
}

// buildSBT10Fixture imports the Akropolis plan and materialises one live
// session over it with deterministic identities.
//
//	system_seat_id  10000 + canonical seat ordinal (0-based)
//	status          every 7th seat "sold", every 11th "held", rest available
//	tier            one fixed-price tier per seated category, price =
//	                50000 + 25000*index minor units (500.00 / 750.00 / …)
//	category id     1_000_000_000 + 10*index (compatibility_id_map range, §4)
func buildSBT10Fixture(t *testing.T) sbt10Fixture {
	t.Helper()

	raw, err := os.ReadFile(akropolisSVGPath(t))
	if err != nil {
		t.Fatalf("read Palac_Akropolis.svg: %v", err)
	}
	geom, _, errs := seating.ImportSVG(raw)
	if len(errs) > 0 {
		t.Fatalf("seating.ImportSVG: %d errors: %v", len(errs), errs)
	}
	geom = seating.Canonicalize(geom)
	if geom.SeatCount() == 0 {
		t.Fatal("Akropolis geometry parsed with zero seats — check the fixture")
	}

	sessionID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sbt10-golden-session"))

	tiers := make([]gen.TicketTierRow, 0, len(geom.Categories))
	tierByCategory := make(map[int]uuid.UUID, len(geom.Categories))
	categoryIDs := make(map[int]int64, len(geom.Categories))
	for _, cat := range geom.Categories {
		categoryIDs[cat.Index] = 1_000_000_000 + int64(10*cat.Index)
		if cat.IsGA() {
			continue
		}
		tierID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sbt10-tier-"+strconv.Itoa(cat.Index)))
		tierByCategory[cat.Index] = tierID
		tiers = append(tiers, gen.TicketTierRow{
			ID:          tierID,
			SessionID:   sessionID,
			Name:        cat.Name,
			PricingMode: "fixed",
			PriceAmount: int64(50000 + 25000*cat.Index),
			Currency:    "CZK",
			SortOrder:   int32(cat.Index),
		})
	}

	var seats []gen.SessionSeatRow
	ordinal := 0
	for _, sec := range geom.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				key := s.Key
				if key == "" {
					key = seating.SeatKey(sec.Key, row.Key, s.Number)
				}
				status := "available"
				switch {
				case ordinal%7 == 0 && ordinal > 0:
					status = "sold"
				case ordinal%11 == 0 && ordinal > 0:
					status = "held"
				}
				seat := gen.SessionSeatRow{
					ID:           uuid.NewSHA1(uuid.NameSpaceOID, []byte("sbt10-seat-"+key)),
					SessionID:    sessionID,
					SeatKey:      key,
					SectorName:   sec.Name,
					RowName:      row.Name,
					SeatNumber:   s.Number,
					Status:       status,
					SystemSeatID: int64(10000 + ordinal),
				}
				if tierID, ok := tierByCategory[s.CategoryIndex]; ok {
					id := tierID
					seat.TierID = &id
				}
				seats = append(seats, seat)
				ordinal++
			}
		}
	}

	return sbt10Fixture{
		geom:        geom,
		seats:       seats,
		tiers:       tiers,
		categoryIDs: categoryIDs,
		statusVer:   42,
	}
}

// TestBil24_500_SBT10GoldenMatchesEncoder compares the render of the fixture
// against the checked-in golden using canonical XML.
func TestBil24_500_SBT10GoldenMatchesEncoder(t *testing.T) {
	fx := buildSBT10Fixture(t)
	got := hseating.RenderSBT10SVG(fx.geom, fx.seats, fx.tiers, fx.categoryIDs, fx.statusVer)

	if os.Getenv("ARENA_REGEN_SBT_GOLDEN") == "1" {
		if err := os.WriteFile(sbt10GoldenPath, got, 0o644); err != nil {
			t.Fatalf("regenerate golden: %v", err)
		}
		t.Fatalf("golden %s regenerated — re-add the disclaimer comment and re-run without ARENA_REGEN_SBT_GOLDEN", sbt10GoldenPath)
	}

	want, err := os.ReadFile(sbt10GoldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	gotCanon := canonicalXML(t, got)
	wantCanon := canonicalXML(t, want)
	if gotCanon != wantCanon {
		t.Fatalf("sbt/1.0 render drifted from %s\nfirst difference:\n%s",
			sbt10GoldenPath, firstXMLDiff(wantCanon, gotCanon))
	}
}

// TestBil24_500_SBT10RenderIsDeterministic guards the ETag contract: the same
// inputs must produce byte-identical output on every call.
func TestBil24_500_SBT10RenderIsDeterministic(t *testing.T) {
	fx := buildSBT10Fixture(t)
	a := hseating.RenderSBT10SVG(fx.geom, fx.seats, fx.tiers, fx.categoryIDs, fx.statusVer)
	b := hseating.RenderSBT10SVG(fx.geom, fx.seats, fx.tiers, fx.categoryIDs, fx.statusVer)
	if !bytes.Equal(a, b) {
		t.Fatal("RenderSBT10SVG is not byte-deterministic for identical inputs")
	}
}

// TestBil24_500_SBT10ParsesLikeTheSeatPicker replays the DOM rules the
// WordPress picker applies (bil24-seat-picker.js:389-394, spec §8):
//
//  1. sbt:* attributes are read with getAttributeNS(sbt, name) — so every one
//     of them must actually be bound to http://www.w3.org/2015/sbt/1.0, not
//     just spelled with an "sbt:" prefix.
//  2. categories come from <metadata> children with localName "category" and
//     are indexed by sbt:index; every circle's sbt:cat must hit that map.
//  3. a seat's sector is the nearest ANCESTOR carrying sbt:sect, its row the
//     nearest ancestor carrying sbt:row.
//  4. viewBox must parse into four numbers (width/height are stripped).
//  5. sbt:state is 1 or 4; sbt:id is a positive integer, unique per plan.
//  6. GA zones and decor carry no sbt:id, so they are never seats.
func TestBil24_500_SBT10ParsesLikeTheSeatPicker(t *testing.T) {
	fx := buildSBT10Fixture(t)
	doc := hseating.RenderSBT10SVG(fx.geom, fx.seats, fx.tiers, fx.categoryIDs, fx.statusVer)

	dec := xml.NewDecoder(bytes.NewReader(doc))

	type frame struct {
		name string
		sect string
		row  string
	}
	var stack []frame
	catByIndex := map[int]map[string]string{}
	inMetadata := false
	seenSeatIDs := map[int64]string{}
	seatCount := 0
	rootChecked := false
	statusVersionSeen := ""

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("render is not well-formed XML: %v", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			if end, isEnd := tok.(xml.EndElement); isEnd {
				if end.Name.Local == "metadata" {
					inMetadata = false
				}
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			}
			continue
		}

		sbtAttrs := map[string]string{}
		plain := map[string]string{}
		for _, a := range start.Attr {
			switch a.Name.Space {
			case hseating.SBT10NamespaceURI:
				sbtAttrs[a.Name.Local] = a.Value
			case "", "http://www.w3.org/2000/svg":
				plain[a.Name.Local] = a.Value
			case "xmlns":
				// namespace declaration, ignored by the picker
			default:
				// Decor spliced in from the imported plan may carry
				// inkscape: / sodipodi: attributes. They are inert, but the
				// prefix MUST resolve to a declared URI — encoding/xml
				// leaves Space as the raw prefix when it does not, which is
				// exactly the "browser refuses the document" failure mode.
				if !strings.Contains(a.Name.Space, "://") {
					t.Errorf("attribute %s:%s uses an undeclared namespace prefix %q",
						a.Name.Space, a.Name.Local, a.Name.Space)
				}
			}
		}

		// Inherit sector/row from the enclosing frame, then override.
		f := frame{name: start.Name.Local}
		if len(stack) > 0 {
			f.sect = stack[len(stack)-1].sect
			f.row = stack[len(stack)-1].row
		}
		if v, ok := sbtAttrs["sect"]; ok {
			f.sect = v
		}
		if v, ok := sbtAttrs["row"]; ok {
			f.row = v
		}
		stack = append(stack, f)

		switch {
		case start.Name.Local == "svg" && !rootChecked:
			rootChecked = true
			if start.Name.Space != "http://www.w3.org/2000/svg" {
				t.Errorf("root <svg> namespace = %q, want the SVG namespace", start.Name.Space)
			}
			if _, hasW := plain["width"]; hasW {
				t.Error("root <svg> carries width; spec §8 says width/height are not emitted")
			}
			if _, hasH := plain["height"]; hasH {
				t.Error("root <svg> carries height; spec §8 says width/height are not emitted")
			}
			fields := strings.Fields(plain["viewBox"])
			if len(fields) != 4 {
				t.Fatalf("viewBox = %q, want four numbers", plain["viewBox"])
			}
			for _, f := range fields {
				if _, err := strconv.ParseFloat(f, 64); err != nil {
					t.Fatalf("viewBox component %q is not a number", f)
				}
			}
			statusVersionSeen = sbtAttrs["statusVersion"]

		case start.Name.Local == "metadata":
			inMetadata = true

		case inMetadata && start.Name.Local == "category":
			if start.Name.Space != hseating.SBT10NamespaceURI {
				t.Errorf("<category> namespace = %q, want the sbt/1.0 namespace", start.Name.Space)
			}
			idx, err := strconv.Atoi(sbtAttrs["index"])
			if err != nil {
				t.Fatalf("category sbt:index = %q, not an integer", sbtAttrs["index"])
			}
			if _, dup := catByIndex[idx]; dup {
				t.Fatalf("duplicate category index %d in <metadata>", idx)
			}
			for _, required := range []string{"id", "index", "name", "color", "price", "class"} {
				if _, ok := sbtAttrs[required]; !ok {
					t.Errorf("category index %d misses sbt:%s", idx, required)
				}
			}
			if _, err := strconv.ParseFloat(sbtAttrs["price"], 64); err != nil {
				t.Errorf("category index %d: sbt:price %q is not a number", idx, sbtAttrs["price"])
			}
			catByIndex[idx] = sbtAttrs

		case start.Name.Local == "circle" && sbtAttrs["id"] != "":
			seatCount++
			id, err := strconv.ParseInt(sbtAttrs["id"], 10, 64)
			if err != nil || id <= 0 {
				t.Fatalf("seat sbt:id = %q, want a positive integer", sbtAttrs["id"])
			}
			if prev, dup := seenSeatIDs[id]; dup {
				t.Fatalf("seat sbt:id %d repeats (also on %s)", id, prev)
			}
			seenSeatIDs[id] = f.sect + "/" + f.row + "/" + sbtAttrs["seat"]

			if state := sbtAttrs["state"]; state != "1" && state != "4" {
				t.Errorf("seat %d: sbt:state = %q, want 1 or 4", id, state)
			}
			cat, err := strconv.Atoi(sbtAttrs["cat"])
			if err != nil {
				t.Fatalf("seat %d: sbt:cat = %q, not an integer", id, sbtAttrs["cat"])
			}
			if _, ok := catByIndex[cat]; !ok {
				t.Fatalf("seat %d: sbt:cat %d has no <metadata> category with that sbt:index", id, cat)
			}
			if sbtAttrs["seat"] == "" {
				t.Errorf("seat %d: sbt:seat is empty", id)
			}
			if f.sect == "" {
				t.Errorf("seat %d: no ancestor carries sbt:sect", id)
			}
			if f.row == "" {
				t.Errorf("seat %d: no ancestor carries sbt:row", id)
			}
			for _, required := range []string{"cx", "cy", "r"} {
				if _, err := strconv.ParseFloat(plain[required], 64); err != nil {
					t.Errorf("seat %d: %s = %q is not a number", id, required, plain[required])
				}
			}
		}
	}

	if statusVersionSeen != strconv.FormatInt(fx.statusVer, 10) {
		t.Errorf("sbt:statusVersion = %q, want %d", statusVersionSeen, fx.statusVer)
	}
	if len(catByIndex) == 0 {
		t.Fatal("<metadata> carried no categories")
	}
	if seatCount != len(fx.seats) {
		t.Errorf("rendered %d seats, fixture has %d live session_seats", seatCount, len(fx.seats))
	}

	// Every live seat's system_seat_id must be addressable by the site.
	for _, s := range fx.seats {
		if _, ok := seenSeatIDs[s.SystemSeatID]; !ok {
			t.Fatalf("session seat %d (%s) is missing from the render", s.SystemSeatID, s.SeatKey)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Canonical XML helpers
// ─────────────────────────────────────────────────────────────────────────────

// canonicalXML renders doc as a normalised token stream: comments and
// processing instructions dropped, whitespace-only text dropped, attributes
// sorted by namespace+local name. Two documents that differ only in the
// disclaimer comment or in indentation compare equal.
func canonicalXML(t *testing.T, doc []byte) string {
	t.Helper()
	var out strings.Builder
	dec := xml.NewDecoder(bytes.NewReader(doc))
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("canonicalXML: %v", err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			attrs := make([]string, 0, len(tk.Attr))
			for _, a := range tk.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
					continue
				}
				attrs = append(attrs, fmt.Sprintf("%s|%s=%q", a.Name.Space, a.Name.Local, a.Value))
			}
			sort.Strings(attrs)
			fmt.Fprintf(&out, "%d<%s|%s %s>\n", depth, tk.Name.Space, tk.Name.Local, strings.Join(attrs, " "))
			depth++
		case xml.EndElement:
			depth--
			fmt.Fprintf(&out, "%d</%s|%s>\n", depth, tk.Name.Space, tk.Name.Local)
		case xml.CharData:
			if text := strings.TrimSpace(string(tk)); text != "" {
				fmt.Fprintf(&out, "%d#%s\n", depth, text)
			}
		}
	}
	return out.String()
}

// firstXMLDiff returns a short excerpt around the first differing canonical
// line so a golden failure names the element instead of dumping the plan.
func firstXMLDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			return fmt.Sprintf("  line %d\n  golden: %s\n  render: %s", i+1, w, g)
		}
	}
	return "  (streams are equal — difference is outside the canonical form)"
}
