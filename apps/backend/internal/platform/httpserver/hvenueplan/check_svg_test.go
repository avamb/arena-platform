package hvenueplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return raw
}

// The draft path takes both dialects: an sbt plan (Forzum) and an authoring
// plan (Inkscape, PriceCategory).
func TestCheckPlanSVG_BothDialects(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		isSBT bool
	}{
		{"sbt", "../../../../tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg", true},
		{"authoring", "../../../domain/seating/testdata/Palac_Akropolis.svg", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.path)
			if got := seating.IsSBTSVG(raw); got != tc.isSBT {
				t.Fatalf("IsSBTSVG = %v, want %v", got, tc.isSBT)
			}
			g, _, planType, errs := checkPlanSVG(raw)
			if len(errs) > 0 {
				t.Fatalf("errors: %v", errs)
			}
			if g.SeatCount() == 0 {
				t.Fatalf("no places imported (plan type %q)", planType)
			}
		})
	}
}
