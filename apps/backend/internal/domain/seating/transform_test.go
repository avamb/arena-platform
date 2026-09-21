package seating

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestParseTransform(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		in           string
		x, y         float64
		wantX, wantY float64
	}{
		{"empty", "", 3, 4, 3, 4},
		{"translate", "translate(10,-5)", 3, 4, 13, -1},
		{"translate one arg", "translate(10)", 3, 4, 13, 4},
		{"scale", "scale(2)", 3, 4, 6, 8},
		{"scale xy", "scale(2 3)", 3, 4, 6, 12},
		{"rotate 90", "rotate(90)", 1, 0, 0, 1},
		{"rotate about a point", "rotate(90,79.084297,67.32521)", 69.30184, 84.91177, 61.49774, 57.54275},
		{"matrix", "matrix(0,-1,1,0,11.759087,146.409507)", 61.49774, 57.54275, 69.30184, 84.91177},
		{"list composes left to right", "translate(10,0) scale(2)", 3, 4, 16, 8},
		{"unknown function is skipped", "perspective(4) translate(1,1)", 0, 0, 1, 1},
		{"malformed arguments are skipped", "translate(a,b) scale(2)", 3, 4, 6, 8},
	}
	for _, tc := range cases {
		gotX, gotY := parseTransform(tc.in).apply(tc.x, tc.y)
		if !almost(gotX, tc.wantX) || !almost(gotY, tc.wantY) {
			t.Errorf("%s: %q maps (%v,%v) to (%v,%v), want (%v,%v)",
				tc.name, tc.in, tc.x, tc.y, gotX, gotY, tc.wantX, tc.wantY)
		}
	}
}

// sbtTransformedPlan is the shape Bil24 serves: every seat carries the same
// cy and the row is put in place by the group's transform.
const sbtTransformedPlan = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 300 400">
<metadata><sbt:category sbt:id="1" sbt:index="1" sbt:name="First" sbt:color="#ff0000" sbt:price="10"/></metadata>
<g transform="translate(100,0)">
  <g sbt:sect="Balcony left" sbt:row="1" transform="rotate(90,50,50)">
    <circle sbt:id="11" sbt:state="1" sbt:cat="1" sbt:seat="1" cx="60" cy="50" r="4"/>
    <circle sbt:id="12" sbt:state="1" sbt:cat="1" sbt:seat="2" cx="70" cy="50" r="4" transform="scale(2)"/>
  </g>
</g>
<g sbt:sect="Parter" sbt:row="1">
  <circle sbt:id="21" sbt:state="1" sbt:cat="1" sbt:seat="1" cx="10" cy="20" r="3"/>
</g>
</svg>`

func TestImportSBTSVG_ResolvesAncestorTransforms(t *testing.T) {
	t.Parallel()
	plan, _, errs := ImportSBTSVG([]byte(sbtTransformedPlan))
	if len(errs) != 0 {
		t.Fatalf("import errors: %v", errs)
	}
	want := map[int64][3]float64{
		// rotate(90,50,50) sends (60,50) to (50,60); the outer group adds 100.
		11: {150, 60, 4},
		// the circle's own scale(2) runs first: (140,100) -> (0,140) -> +100.
		12: {100, 140, 8},
		// no transform anywhere: the attributes are kept as written.
		21: {10, 20, 3},
	}
	seen := 0
	for _, sec := range plan.Geometry.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				w, ok := want[s.ExternalID]
				if !ok {
					continue
				}
				seen++
				if !almost(s.X, w[0]) || !almost(s.Y, w[1]) || !almost(s.Radius, w[2]) {
					t.Errorf("seat %d at (%v,%v) r=%v, want (%v,%v) r=%v",
						s.ExternalID, s.X, s.Y, s.Radius, w[0], w[1], w[2])
				}
			}
		}
	}
	if seen != len(want) {
		t.Fatalf("resolved %d seats, want %d", seen, len(want))
	}
}

// A plan already flattened by ops/bil24-export/to-bundle.mjs carries the
// absolute position on the circle plus the INVERSE of its ancestors'
// transform, so both a transform-blind and a transform-aware parser land on
// the same spot.
func TestImportSBTSVG_BakedPlanStaysPut(t *testing.T) {
	t.Parallel()
	const baked = `<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0" viewBox="0 0 320 430">
<metadata><sbt:category sbt:id="1" sbt:index="1" sbt:name="First" sbt:color="#ff0000" sbt:price="10"/></metadata>
<g sbt:sect="Balcony left" sbt:row="no" transform="rotate(90,79.084297,67.32521)">
  <circle sbt:id="2874571138" sbt:state="0" sbt:cat="1" sbt:seat="1" cx="61.49774" cy="57.54275" r="3.88779" transform="matrix(0,-1,1,0,11.759087,146.409507)"/>
</g>
</svg>`
	plan, _, errs := ImportSBTSVG([]byte(baked))
	if len(errs) != 0 {
		t.Fatalf("import errors: %v", errs)
	}
	s := plan.Geometry.Sections[0].Rows[0].Seats[0]
	if !almost(s.X, 61.49774) || !almost(s.Y, 57.54275) || !almost(s.Radius, 3.88779) {
		t.Errorf("baked seat moved to (%v,%v) r=%v", s.X, s.Y, s.Radius)
	}
}

// The owner's Inkscape source of Palác Akropolis writes ONE cy for all 90
// seats and places the rows with group transforms. Imported, the seats must
// spread over the hall and stay inside the canvas.
func TestImportSVG_PalacAkropolisGA_RowsKeepTheirPlace(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("testdata", "Palac_Akropolis_GA.svg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	g, _, errs := ImportSVG(raw)
	if len(errs) != 0 {
		t.Fatalf("import errors: %v", errs)
	}
	ys := map[int]bool{}
	seats := 0
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, s := range row.Seats {
				seats++
				ys[int(math.Round(s.Y))] = true
				if s.X < 0 || s.X > g.Canvas.Width || s.Y < 0 || s.Y > g.Canvas.Height {
					t.Errorf("seat %s at (%v,%v) is outside the %vx%v canvas",
						s.Key, s.X, s.Y, g.Canvas.Width, g.Canvas.Height)
				}
			}
		}
	}
	if seats != 90 {
		t.Fatalf("imported %d seats, want 90", seats)
	}
	if len(ys) < 10 {
		t.Errorf("90 seats sit on %d distinct heights; the rows collapsed", len(ys))
	}
	checked := false
	// Bil24's GET_SCHEMA puts Balcony center, row 1, seat 1 at (87.90018, 161.34857).
	for _, sec := range g.Sections {
		if sec.Key != "balcony-center" {
			continue
		}
		for _, row := range sec.Rows {
			if row.Key != "1" {
				continue
			}
			for _, s := range row.Seats {
				if s.Number != "1" {
					continue
				}
				checked = true
				if math.Abs(s.X-87.90018) > 0.01 || math.Abs(s.Y-161.34857) > 0.01 {
					t.Errorf("balcony center 1/1 at (%v,%v), want (87.90018,161.34857)", s.X, s.Y)
				}
			}
		}
	}
	if !checked {
		t.Fatalf("balcony center row 1 seat 1 not found in the imported plan")
	}
}
