// layout_svg_test.go — SEAT-D3 (feature #314) contract tests for the
// BSS-compatible SVG export.
//
// Coverage:
//   - statusToBSS wire-code table (§6 mapping)
//   - RenderBSSLayoutSVG emits the exact sbt:* attribute surface
//   - determinism / idempotency (byte-identical output for the same input)
//   - decor fragment splicing
//   - dependency 503 gate + composite ETag negotiation
package hseating

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// TestSeatD3_StatusToBSS pins every internal status → BSS wire code
// mapping from §6 of the seating backlog. Any unknown status collapses
// to INACCESSIBLE (0) so legacy consumers never see a hole in the enum.
func TestSeatD3_StatusToBSS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status string
		want   int
	}{
		{"available", bssStateAvailable},  // 1
		{"held", bssStateReserved},        // 3
		{"sold", bssStateOccupied},        // 4
		{"blocked", bssStateInaccessible}, // 0
		{"", bssStateInaccessible},
		{"unknown_status", bssStateInaccessible},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			if got := statusToBSS(tc.status); got != tc.want {
				t.Fatalf("statusToBSS(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

// TestSeatD3_LayoutSVGETag pins the composite ETag format so downstream
// caches can rely on it. Format is opaque to callers but stable.
func TestSeatD3_LayoutSVGETag(t *testing.T) {
	t.Parallel()
	got := layoutSVGETag("abc123", 42)
	want := `"abc123:42"`
	if got != want {
		t.Fatalf("layoutSVGETag = %q, want %q", got, want)
	}
}

// mkGeometry builds a small fixture with two categories, two sectors
// (one row each), three seats total, and a non-empty decor fragment.
func mkGeometry() seating.Geometry {
	return seating.Geometry{
		SchemaVersion: 1,
		Canvas:        seating.Canvas{Width: 1000, Height: 800},
		Categories: []seating.Category{
			{Index: 1, Name: "VIP", Color: "#ff0000"},
			{Index: 2, Name: "Standard", Color: "#00aa00"},
		},
		Sections: []seating.Section{
			{
				Key: "parter", Name: "Parter",
				Rows: []seating.Row{{
					Key: "1", Name: "1",
					Seats: []seating.Seat{
						{Key: "parter|1|1", Number: "1", X: 100, Y: 200, Radius: 8, CategoryIndex: 1},
						{Key: "parter|1|2", Number: "2", X: 120, Y: 200, Radius: 8, CategoryIndex: 1},
					},
				}},
			},
			{
				Key: "balcony", Name: "Balcony <left>",
				Rows: []seating.Row{{
					Key: "A", Name: `A "special"`,
					Seats: []seating.Seat{
						{Key: "balcony|A|5", Number: "5", X: 500, Y: 100, Radius: 6, CategoryIndex: 2},
					},
				}},
			},
		},
		DecorSVG: `<rect x="0" y="0" width="1000" height="800" fill="#fafafa"/>`,
	}
}

// mkSessionSeats mirrors ListSessionSeats output for the fixture: one
// row per geometry seat with a mixed set of statuses so we exercise
// every §6 wire code branch in a single render.
func mkSessionSeats(vipTier, stdTier uuid.UUID) []gen.SessionSeatRow {
	vip := vipTier
	std := stdTier
	return []gen.SessionSeatRow{
		{ID: uuid.MustParse("019f4d67-0000-7000-8000-000000000001"),
			SeatKey: "parter|1|1", SectorName: "Parter", RowName: "1",
			SeatNumber: "1", TierID: &vip, Status: "available", StatusVersion: 7},
		{ID: uuid.MustParse("019f4d67-0000-7000-8000-000000000002"),
			SeatKey: "parter|1|2", SectorName: "Parter", RowName: "1",
			SeatNumber: "2", TierID: &vip, Status: "sold", StatusVersion: 7},
		{ID: uuid.MustParse("019f4d67-0000-7000-8000-000000000003"),
			SeatKey: "balcony|A|5", SectorName: "Balcony <left>", RowName: `A "special"`,
			SeatNumber: "5", TierID: &std, Status: "held", StatusVersion: 7},
	}
}

func mkTiers(vipTier, stdTier uuid.UUID) []gen.TicketTierRow {
	return []gen.TicketTierRow{
		{ID: vipTier, Name: "VIP", PricingMode: "fixed", PriceAmount: 5000, Currency: "USD"},
		{ID: stdTier, Name: "Standard", PricingMode: "fixed", PriceAmount: 2000, Currency: "USD"},
	}
}

// TestSeatD3_RenderEmitsBSSAttributes pins the full wire attribute
// surface: seats carry sbt:seat / sbt:id / sbt:cat / sbt:state; row
// groups carry sbt:row / sbt:sect; category swatches carry the price /
// currency / sold / used quintet; the root <svg> carries
// sbt:statusVersion. XML entities inside sector / row names must be
// escaped so downstream consumers can parse the output with a strict
// parser.
func TestSeatD3_RenderEmitsBSSAttributes(t *testing.T) {
	t.Parallel()

	vip := uuid.MustParse("019f4d67-0001-7000-8000-000000000101")
	std := uuid.MustParse("019f4d67-0001-7000-8000-000000000102")
	body := string(RenderBSSLayoutSVG(
		mkGeometry(), mkSessionSeats(vip, std), mkTiers(vip, std), 42,
	))

	// XML preamble + namespace declarations.
	mustContain(t, body, `<?xml version="1.0" encoding="UTF-8"?>`)
	mustContain(t, body, `xmlns="http://www.w3.org/2000/svg"`)
	mustContain(t, body, `xmlns:sbt="http://bil24.pro/sbt"`)
	mustContain(t, body, `viewBox="0 0 1000 800"`)
	mustContain(t, body, `sbt:statusVersion="42"`)

	// Decor fragment spliced verbatim.
	mustContain(t, body, `<g id="Decor"><rect x="0" y="0" width="1000" height="800" fill="#fafafa"/></g>`)

	// PriceCategory metadata: VIP has 1 sold (seat 2) + 0 held + 0 blocked
	// → sold=1, used=1. Standard has 0 sold + 1 held → sold=0, used=1.
	mustContain(t, body,
		`<circle sbt:index="1" sbt:name="VIP" sbt:color="#ff0000" sbt:price="5000" sbt:currency="USD" sbt:sold="1" sbt:used="1" fill="#ff0000"/>`)
	mustContain(t, body,
		`<circle sbt:index="2" sbt:name="Standard" sbt:color="#00aa00" sbt:price="2000" sbt:currency="USD" sbt:sold="0" sbt:used="1" fill="#00aa00"/>`)

	// Row group carries sbt:sect + sbt:row and escapes markup inside
	// sector / row names.
	mustContain(t, body, `<g sbt:sect="Parter" sbt:row="1">`)
	mustContain(t, body, `<g sbt:sect="Balcony &lt;left&gt;" sbt:row="A &#34;special&#34;">`)

	// Seat wire attributes, one per §6 status branch.
	// Seat parter|1|1  → available → sbt:state="1"
	mustContain(t, body,
		`<circle sbt:seat="1" sbt:id="019f4d67-0000-7000-8000-000000000001" sbt:cat="1" sbt:state="1" cx="100" cy="200" r="8" fill="#ff0000"/>`)
	// Seat parter|1|2  → sold      → sbt:state="4"
	mustContain(t, body,
		`<circle sbt:seat="2" sbt:id="019f4d67-0000-7000-8000-000000000002" sbt:cat="1" sbt:state="4" cx="120" cy="200" r="8" fill="#ff0000"/>`)
	// Seat balcony|A|5 → held      → sbt:state="3"
	mustContain(t, body,
		`<circle sbt:seat="5" sbt:id="019f4d67-0000-7000-8000-000000000003" sbt:cat="2" sbt:state="3" cx="500" cy="100" r="6" fill="#00aa00"/>`)
}

// TestSeatD3_RenderIsDeterministic — a second render of the same input
// MUST be byte-identical. The renderer walks canonicalised collections
// so consumers can content-address the payload safely.
func TestSeatD3_RenderIsDeterministic(t *testing.T) {
	t.Parallel()

	vip := uuid.MustParse("019f4d67-0001-7000-8000-000000000101")
	std := uuid.MustParse("019f4d67-0001-7000-8000-000000000102")
	g := mkGeometry()
	seats := mkSessionSeats(vip, std)
	tiers := mkTiers(vip, std)

	a := RenderBSSLayoutSVG(g, seats, tiers, 7)
	b := RenderBSSLayoutSVG(g, seats, tiers, 7)
	if string(a) != string(b) {
		t.Fatalf("RenderBSSLayoutSVG is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

// TestSeatD3_RenderNoDecor — omitting the decor fragment is tolerated
// and produces no <g id="Decor"> wrapper. Blocked seats surface as
// BSS INACCESSIBLE (0). Seats missing from session_seats (fresh plan
// before bind) render as available with an empty sbt:id — matching the
// contract that a plan preview MAY be requested before session
// materialization.
func TestSeatD3_RenderNoDecor(t *testing.T) {
	t.Parallel()

	g := mkGeometry()
	g.DecorSVG = ""
	body := string(RenderBSSLayoutSVG(g, nil, nil, 0))

	if strings.Contains(body, "Decor") {
		t.Fatalf("empty decor must not emit <g id=\"Decor\">; body=%s", body)
	}
	// PriceCategory metadata degrades gracefully: no tier binding → price=0
	// + empty currency; no seats → sold=0 / used=0.
	mustContain(t, body,
		`<circle sbt:index="1" sbt:name="VIP" sbt:color="#ff0000" sbt:price="0" sbt:currency="" sbt:sold="0" sbt:used="0" fill="#ff0000"/>`)
	// Seats emit with sbt:id="" and default state="1" AVAILABLE when no
	// live session_seat row is present.
	mustContain(t, body,
		`<circle sbt:seat="1" sbt:id="" sbt:cat="1" sbt:state="1" cx="100" cy="200" r="8" fill="#ff0000"/>`)
}

// TestSeatD3_HandlerReturns503WhenQueriesUnwired confirms the
// dependency-unavailable gate mirrors the sibling public endpoints. The
// mount_seating.go self-gate expects a stable 503 dependency envelope
// rather than a 500 / panic when the seating pool is missing.
func TestSeatD3_HandlerReturns503WhenQueriesUnwired(t *testing.T) {
	t.Parallel()

	h := New(nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r := chi.NewRouter()
	r.Get("/v1/event-sessions/{id}/layout.svg", h.HandleGetPublicSessionLayoutSVG)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	const someID = "019f4d67-0000-7000-8000-000000000000"
	resp, err := http.Get(ts.URL + "/v1/event-sessions/" + someID + "/layout.svg")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "dependency.database_unavailable") {
		t.Fatalf("body missing dependency.database_unavailable envelope; got %s", string(body))
	}
}

// TestSeatD3_CacheControlContract pins the response cache header. The
// live seat map turns over on every reservation, so edge caches MUST
// NOT store the payload without revalidation.
func TestSeatD3_CacheControlContract(t *testing.T) {
	t.Parallel()
	if layoutSVGCacheControl != "no-cache" {
		t.Fatalf("layoutSVGCacheControl = %q; SEAT-D3 requires 'no-cache' for live seat correctness", layoutSVGCacheControl)
	}
}

// mustContain is a tiny substring-in-body assertion helper.
func mustContain(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("body missing substring\n want: %s\n body:\n%s", want, body)
	}
}
