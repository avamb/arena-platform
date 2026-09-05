// sbt10_svg.go implements the sbt/1.0 seating-plan encoder consumed by the
// WordPress Bil24 seat picker (feature #500, W1-B5a; spec §8 of
// 08_architecture/18_bil24_compat_wave1_specification_ru.md).
//
// This is the SECOND encoder next to RenderBSSLayoutSVG (layout_svg.go): the
// legacy `/v1/event-sessions/{uuid}/layout.svg` export keeps its own
// namespace (http://bil24.pro/sbt), its swatch-circle PriceCategory group and
// its 0–5 status codes, because the arena widget already consumes that shape.
// The WP site instead reads namespace `http://www.w3.org/2015/sbt/1.0`,
// `<metadata><sbt:category …>` and the two-value state alphabet 1 / 4
// (bil24-seat-picker.js:389-394). Rather than break one consumer to serve the
// other, both projections live side by side over the same geometry.
//
// Wire surface (spec §8):
//
//	<svg xmlns="http://www.w3.org/2000/svg"
//	     xmlns:sbt="http://www.w3.org/2015/sbt/1.0"
//	     viewBox="0 0 1200 800" sbt:statusVersion="42">
//	  <metadata>
//	    <sbt:category sbt:id="1000000020" sbt:index="1" sbt:name="Parter"
//	                  sbt:color="#e53935" sbt:price="900" sbt:class="cat-1"/>
//	  </metadata>
//	  <g id="Decor">…</g>
//	  <g sbt:sect="Parter">
//	    <g sbt:row="3">
//	      <circle sbt:id="1731" sbt:state="1" sbt:cat="1" sbt:seat="12"
//	              cx="…" cy="…" r="6" fill="#e53935"/>
//
// Normative details, all enforced by the tests in sbt10_svg_test.go and by
// the golden in tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg:
//
//   - `sbt:id` on a <circle> is `session_seats.system_seat_id` (spec §4), NOT
//     the row UUID. A geometry seat with no materialised session_seats row has
//     no such identity and is therefore skipped: emitting it without sbt:id
//     would make the site render an unclickable ghost seat.
//   - `sbt:cat` is the category INDEX (matching `sbt:index` in <metadata>),
//     not the category id.
//   - `sbt:state` is 1 (free) or 4 (taken); held / sold / unavailable all
//     collapse to 4, which is the whole state alphabet the site knows.
//   - `viewBox` is mandatory; `width` / `height` are deliberately NOT emitted
//     (the picker strips them anyway).
//   - GA zones are decor: their polygons render inside <g id="Decor"> without
//     any `sbt:id`, so the picker never treats them as reservable seats.
//   - Money is major units with at most 2 decimals (spec, "Единицы"), e.g.
//     `900` or `12.5` — the DB stores minor units.
//
// Determinism: categories are walked in Index order, sections / rows / seats
// in canonical geometry order, so the output is byte-stable for equal inputs
// and safe to hash into an ETag.
package hseating

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// SBT10NamespaceURI is the namespace the WordPress seat picker matches on
// via getAttributeNS (bil24-seat-picker.js:389-394). It is a fixed string,
// not a resolvable document — changing it silently breaks every site.
const SBT10NamespaceURI = "http://www.w3.org/2015/sbt/1.0"

// Namespaces the imported decor fragment can still reference. seating's
// importer re-serialises decor with the `inkscape:` / `sodipodi:` prefixes
// intact (svg_import.go knownNamespacePrefixes), so the root element MUST
// declare them: an undeclared prefix makes the document namespace-ill-formed
// and a browser's XML parser refuses the whole plan, not just the decor.
const (
	inkscapeNamespaceURI = "http://www.inkscape.org/namespaces/inkscape"
	sodipodiNamespaceURI = "http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
)

// sbt/1.0 seat states (spec §8). The alphabet is intentionally two-valued:
// the site distinguishes only "can I click this seat" from "I cannot".
const (
	sbt10StateFree  = 1
	sbt10StateTaken = 4
)

// RenderSBT10SVG renders the sbt/1.0 seating plan for one session. Pure
// function so the HTTP route (feature #501) and the contract tests share the
// exact wire projection.
//
//	g                  canonical geometry (canvas + categories + sections)
//	seats              live session_seats snapshot; source of sbt:id (
//	                   system_seat_id) and sbt:state
//	tiers              ticket_tiers snapshot; source of sbt:price
//	categoryIDs        category index → compatibility_id_map id of the tier
//	                   bound to that category (kind='category_price'). A
//	                   category with no mapped id is emitted with sbt:id="0"
//	                   so the attribute surface never varies by row.
//	seatStatusVersion  monotonic session cursor, emitted as sbt:statusVersion
//	                   so the site can tell a cached SVG from a fresh one.
func RenderSBT10SVG(
	g seating.Geometry,
	seats []gen.SessionSeatRow,
	tiers []gen.TicketTierRow,
	categoryIDs map[int]int64,
	seatStatusVersion int64,
) []byte {
	seatGeomByKey := seatKeyIndex(g)
	categoryToTier := resolveCategoryTiers(g, seats, tiers, seatGeomByKey)

	liveByKey := make(map[string]gen.SessionSeatRow, len(seats))
	for _, s := range seats {
		liveByKey[s.SeatKey] = s
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")

	// Root element. No width/height on purpose (see the file docstring);
	// viewBox alone drives the picker's coordinate transform.
	fmt.Fprintf(&buf,
		`<svg xmlns="%s" xmlns:svg="%s" xmlns:sbt="%s" xmlns:inkscape="%s" xmlns:sodipodi="%s" viewBox="0 0 %s %s" sbt:statusVersion="%d">`+"\n",
		svgNamespaceURI, svgNamespaceURI, SBT10NamespaceURI,
		inkscapeNamespaceURI, sodipodiNamespaceURI,
		formatFloat(g.Canvas.Width), formatFloat(g.Canvas.Height),
		seatStatusVersion,
	)

	// ── <metadata>: one <sbt:category> per seated category, index ASC ────
	// GA categories are NOT listed here: they carry no clickable seats, so
	// the picker has nothing to bind them to (they render as decor below).
	sortedCats := append([]seating.Category(nil), g.Categories...)
	sort.SliceStable(sortedCats, func(i, j int) bool {
		return sortedCats[i].Index < sortedCats[j].Index
	})
	buf.WriteString(`  <metadata>` + "\n")
	for _, cat := range sortedCats {
		if cat.IsGA() {
			continue
		}
		price := int64(0)
		if tier, ok := categoryToTier[cat.Index]; ok {
			price = tier.PriceAmount
		}
		fmt.Fprintf(&buf,
			`    <sbt:category sbt:id="%d" sbt:index="%d" sbt:name=%s sbt:color=%s sbt:price="%s" sbt:class="cat-%d"/>`+"\n",
			categoryIDs[cat.Index],
			cat.Index,
			xmlAttrString(cat.Name),
			xmlAttrString(cat.Color),
			formatMajor(price),
			cat.Index,
		)
	}
	buf.WriteString(`  </metadata>` + "\n")

	// ── <g id="Decor">: backdrop + GA zones, never reservable ───────────
	decor := strings.TrimSpace(g.DecorSVG)
	gaZones := gaZonePolygons(sortedCats)
	if decor != "" || gaZones != "" {
		buf.WriteString(`  <g id="Decor">`)
		buf.WriteString(g.DecorSVG)
		buf.WriteString(gaZones)
		buf.WriteString(`</g>` + "\n")
	}

	// ── Seat groups: <g sbt:sect><g sbt:row><circle sbt:id …> ───────────
	// The picker resolves a seat's sector by walking up to the nearest
	// ancestor carrying sbt:sect, so the two-level nesting is normative.
	for _, sec := range g.Sections {
		var secBuf bytes.Buffer
		for _, row := range sec.Rows {
			var rowBuf bytes.Buffer
			for _, seat := range row.Seats {
				key := seat.Key
				if key == "" {
					key = seating.SeatKey(sec.Key, row.Key, seat.Number)
				}
				live, ok := liveByKey[key]
				if !ok {
					// No system_seat_id ⇒ no sbt:id ⇒ not a seat the site
					// may click. Skipped rather than emitted id-less.
					continue
				}
				fmt.Fprintf(&rowBuf,
					`        <circle sbt:id="%d" sbt:state="%d" sbt:cat="%d" sbt:seat=%s cx="%s" cy="%s" r="%s" fill=%s/>`+"\n",
					live.SystemSeatID,
					sbt10State(live.Status),
					seat.CategoryIndex,
					xmlAttrString(seat.Number),
					formatFloat(seat.X),
					formatFloat(seat.Y),
					formatFloat(seat.Radius),
					xmlAttrString(categoryColor(sortedCats, seat.CategoryIndex)),
				)
			}
			if rowBuf.Len() == 0 {
				// An entirely unmaterialised row would produce an empty
				// <g sbt:row> the picker has to skip; drop it instead.
				continue
			}
			fmt.Fprintf(&secBuf, `      <g sbt:row=%s>`+"\n", xmlAttrString(row.Name))
			secBuf.Write(rowBuf.Bytes())
			secBuf.WriteString(`      </g>` + "\n")
		}
		if secBuf.Len() == 0 {
			continue
		}
		fmt.Fprintf(&buf, `  <g sbt:sect=%s>`+"\n", xmlAttrString(sec.Name))
		buf.Write(secBuf.Bytes())
		buf.WriteString(`  </g>` + "\n")
	}

	buf.WriteString(`</svg>` + "\n")
	return buf.Bytes()
}

// sbt10State collapses the internal session_seats.status alphabet onto the
// two sbt/1.0 codes (spec §8): only "available" is free; held / sold /
// unavailable and any unknown status are taken, which is the fail-safe
// direction — a site never offers a seat it may not sell.
func sbt10State(status string) int {
	if status == "available" {
		return sbt10StateFree
	}
	return sbt10StateTaken
}

// categoryColor returns the fill of the category with the given index, or
// "" when the geometry does not declare it (the picker then falls back to
// its own palette).
func categoryColor(cats []seating.Category, index int) string {
	for _, c := range cats {
		if c.Index == index {
			return c.Color
		}
	}
	return ""
}

// gaZonePolygons renders every general-admission category polygon as an
// inert decor <polygon>. Deliberately carries no sbt:id: spec §8 says GA
// zones are decor, and the picker treats "has sbt:id" as "is a seat".
func gaZonePolygons(cats []seating.Category) string {
	var buf bytes.Buffer
	for _, c := range cats {
		if !c.IsGA() || len(c.Polygon) == 0 {
			continue
		}
		points := make([]string, 0, len(c.Polygon))
		for _, p := range c.Polygon {
			points = append(points, formatFloat(p.X)+","+formatFloat(p.Y))
		}
		fmt.Fprintf(&buf,
			`<polygon sbt:zone=%s points=%s fill=%s fill-opacity="0.35"/>`,
			xmlAttrString(c.Name),
			xmlAttrString(strings.Join(points, " ")),
			xmlAttrString(c.Color),
		)
	}
	return buf.String()
}

// formatMajor renders a minor-unit amount as the major-unit wire value with
// at most two decimals ("900", "12.5", "26.25") per the spec's money rule.
func formatMajor(minor int64) string {
	return strconv.FormatFloat(float64(minor)/100, 'f', -1, 64)
}
