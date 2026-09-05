// sbt10_svg_test.go — feature #500. Unit-level rules of the sbt/1.0
// encoder that the Akropolis golden (tests/compat/bil24) cannot express
// cheaply: the state alphabet, GA-as-decor, the money projection, and the
// "no system_seat_id ⇒ no seat" rule.
package hseating

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// sbt10TestGeometry is a two-seat Parter row plus one GA zone: the
// smallest shape that exercises every branch of the encoder.
func sbt10TestGeometry() seating.Geometry {
	return seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 400, Height: 240},
		Categories: []seating.Category{
			{Index: 1, Name: "Parter", Color: "#e53935"},
			{
				Index: 2, Name: "Floor", Color: "#3366cc",
				Kind: seating.KindGeneralAdmission, Capacity: 50,
				Polygon: []seating.Point{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}},
			},
		},
		Sections: []seating.Section{{
			Key:  "parter",
			Name: "Parter",
			Rows: []seating.Row{{
				Key:  "3",
				Name: "3",
				Seats: []seating.Seat{
					{Key: "parter|3|11", Number: "11", X: 10, Y: 20, Radius: 6, CategoryIndex: 1},
					{Key: "parter|3|12", Number: "12", X: 22, Y: 20, Radius: 6, CategoryIndex: 1},
				},
			}},
		}},
	}
}

func sbt10TestSeat(key string, systemSeatID int64, status string, tierID *uuid.UUID) gen.SessionSeatRow {
	return gen.SessionSeatRow{
		ID:           uuid.New(),
		SeatKey:      key,
		SectorName:   "Parter",
		RowName:      "3",
		Status:       status,
		TierID:       tierID,
		SystemSeatID: systemSeatID,
	}
}

// TestRenderSBT10SVG_StateAlphabet pins the two-value state alphabet:
// only "available" is free, everything else — including a status the
// encoder has never heard of — is taken. Fail-safe direction: a site must
// never be told it may sell a seat it may not.
func TestRenderSBT10SVG_StateAlphabet(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"available", `sbt:state="1"`},
		{"held", `sbt:state="4"`},
		{"sold", `sbt:state="4"`},
		{"unavailable", `sbt:state="4"`},
		{"some_future_status", `sbt:state="4"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.status, func(t *testing.T) {
			out := string(RenderSBT10SVG(
				sbt10TestGeometry(),
				[]gen.SessionSeatRow{sbt10TestSeat("parter|3|11", 1731, tc.status, nil)},
				nil, map[int]int64{1: 1000000020}, 42,
			))
			if !strings.Contains(out, tc.want) {
				t.Errorf("status %q: render misses %s\n%s", tc.status, tc.want, out)
			}
		})
	}
}

// TestRenderSBT10SVG_SeatAttributes checks the normative circle surface:
// sbt:id is the system_seat_id (never the row uuid), sbt:cat is the
// category INDEX, and the seat sits under <g sbt:sect>/<g sbt:row>.
func TestRenderSBT10SVG_SeatAttributes(t *testing.T) {
	out := string(RenderSBT10SVG(
		sbt10TestGeometry(),
		[]gen.SessionSeatRow{sbt10TestSeat("parter|3|12", 1731, "available", nil)},
		nil, map[int]int64{1: 1000000020}, 7,
	))

	for _, want := range []string{
		`sbt:statusVersion="7"`,
		`viewBox="0 0 400 240"`,
		`<g sbt:sect="Parter">`,
		`<g sbt:row="3">`,
		`sbt:id="1731"`,
		`sbt:cat="1"`,
		`sbt:seat="12"`,
		`fill="#e53935"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render misses %s\n%s", want, out)
		}
	}
	// width/height are deliberately not emitted (spec §8).
	if strings.Contains(out, ` width="`) || strings.Contains(out, ` height="`) {
		t.Errorf("render emits width/height on the root\n%s", out)
	}
	// Seat 11 has no session_seats row, so it has no sbt:id and must not
	// be rendered as a ghost seat.
	if strings.Contains(out, `sbt:seat="11"`) {
		t.Errorf("unmaterialised seat 11 was rendered\n%s", out)
	}
}

// TestRenderSBT10SVG_GAZonesAreDecor pins spec §8: a GA category never
// appears in <metadata> and its polygon renders inside <g id="Decor">
// without sbt:id, because the picker treats "has sbt:id" as "is a seat".
func TestRenderSBT10SVG_GAZonesAreDecor(t *testing.T) {
	out := string(RenderSBT10SVG(
		sbt10TestGeometry(),
		[]gen.SessionSeatRow{sbt10TestSeat("parter|3|11", 1731, "available", nil)},
		nil, map[int]int64{1: 1000000020, 2: 1000000030}, 42,
	))

	if strings.Contains(out, `sbt:index="2"`) {
		t.Errorf("GA category leaked into <metadata>\n%s", out)
	}
	polygon := `<polygon sbt:zone="Floor" points="0,0 10,0 10,10" fill="#3366cc" fill-opacity="0.35"/>`
	if !strings.Contains(out, polygon) {
		t.Errorf("GA zone polygon missing\n%s", out)
	}
	decor := out[strings.Index(out, `<g id="Decor">`):]
	decor = decor[:strings.Index(decor, `</g>`)]
	if strings.Contains(decor, "sbt:id") {
		t.Errorf("decor carries sbt:id, the picker would treat it as a seat\n%s", decor)
	}
}

// TestRenderSBT10SVG_PriceIsMajorUnits pins the money rule: the DB stores
// minor units, the wire carries major units with at most two decimals.
func TestRenderSBT10SVG_PriceIsMajorUnits(t *testing.T) {
	cases := []struct {
		minor int64
		want  string
	}{
		{90000, "900"},
		{1250, "12.5"},
		{2625, "26.25"},
		{0, "0"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			tierID := uuid.New()
			out := string(RenderSBT10SVG(
				sbt10TestGeometry(),
				[]gen.SessionSeatRow{sbt10TestSeat("parter|3|11", 1731, "available", &tierID)},
				[]gen.TicketTierRow{{ID: tierID, Name: "Parter", PricingMode: "fixed", PriceAmount: tc.minor, Currency: "CZK"}},
				map[int]int64{1: 1000000020}, 42,
			))
			if !strings.Contains(out, `sbt:price="`+tc.want+`"`) {
				t.Errorf("minor %d: want sbt:price=%q\n%s", tc.minor, tc.want, out)
			}
		})
	}
}

// TestRenderSBT10SVG_UnmappedCategoryIDIsZero keeps the attribute surface
// invariant: a category with no compatibility_id_map row still emits
// sbt:id, as "0", rather than dropping the attribute.
func TestRenderSBT10SVG_UnmappedCategoryIDIsZero(t *testing.T) {
	out := string(RenderSBT10SVG(
		sbt10TestGeometry(),
		[]gen.SessionSeatRow{sbt10TestSeat("parter|3|11", 1731, "available", nil)},
		nil, nil, 42,
	))
	if !strings.Contains(out, `<sbt:category sbt:id="0" sbt:index="1"`) {
		t.Errorf("unmapped category did not fall back to sbt:id=\"0\"\n%s", out)
	}
}

// TestRenderSBT10SVG_EmptyRowsAndSectionsAreDropped: a section whose seats
// are all unmaterialised produces no empty <g sbt:sect> the picker has to
// skip.
func TestRenderSBT10SVG_EmptyRowsAndSectionsAreDropped(t *testing.T) {
	out := string(RenderSBT10SVG(sbt10TestGeometry(), nil, nil, map[int]int64{1: 1}, 42))
	if strings.Contains(out, "sbt:sect") || strings.Contains(out, "sbt:row") {
		t.Errorf("empty section/row groups were emitted\n%s", out)
	}
	// The category metadata is still there — the site needs the legend
	// even when no seat is currently bound.
	if !strings.Contains(out, `sbt:index="1"`) {
		t.Errorf("category metadata dropped with the seats\n%s", out)
	}
}

// TestRenderSBT10SVG_DeclaresDecorNamespaces: the decor fragment is
// spliced in verbatim and may carry inkscape:/sodipodi: attributes. An
// undeclared prefix makes the whole document namespace-ill-formed and a
// browser refuses to render it, so the root must declare them.
func TestRenderSBT10SVG_DeclaresDecorNamespaces(t *testing.T) {
	g := sbt10TestGeometry()
	g.DecorSVG = `<g inkscape:label="#backdrop"><rect x="0" y="0" width="4" height="4"/></g>`
	out := string(RenderSBT10SVG(g, nil, nil, map[int]int64{1: 1}, 42))
	for _, want := range []string{
		`xmlns:inkscape="` + inkscapeNamespaceURI + `"`,
		`xmlns:sodipodi="` + sodipodiNamespaceURI + `"`,
		`xmlns:sbt="` + SBT10NamespaceURI + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("root misses %s\n%s", want, out)
		}
	}
}
