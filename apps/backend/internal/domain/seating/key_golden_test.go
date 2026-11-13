package seating

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The golden checksums below were computed with the importers as they were
// BEFORE the non-Latin key resolution (key_resolve.go) existed. A plan whose
// names never collide must import byte-identically, so these values must
// never move: a change here means stored seat keys of live sessions would
// change too.

type keyGoldenCase struct {
	name      string
	path      []string
	sbt       bool
	canonical string // sha256 hex of CanonicalJSON
	seatKeys  string // sha256 hex of the sorted seat keys joined by "\n"
}

var keyGoldenCases = []keyGoldenCase{
	{
		name:      "Palac_Akropolis",
		path:      []string{"testdata", "Palac_Akropolis.svg"},
		canonical: "6c160725c578378d9bd1c25f885ae9627b046ff68ff015fe012b38cd900f5837",
		seatKeys:  "2eb462979f743c87c75bd527c1b494f1b27efbdb87c84791e32b5037875dd711",
	},
	{
		name:      "Palac_Akropolis_GA",
		path:      []string{"testdata", "Palac_Akropolis_GA.svg"},
		canonical: "a8f7c80caa2c8e4550804d73e6b1762d500aea82d377e5cfa39bb488ed974fb7",
		seatKeys:  "6e39078062a150baf12ff782781edaca1a88226982f7318280515bd3e3cb48bd",
	},
	{
		name:      "palac_akropolis.sbt",
		path:      []string{"..", "..", "..", "tests", "compat", "bil24", "testdata", "wp", "svg", "palac_akropolis.sbt.svg"},
		sbt:       true,
		canonical: "16ba69da578212fc80c147eb78f2097ca1dde6cd9ec77fc275383e923462cebb",
		seatKeys:  "2eb462979f743c87c75bd527c1b494f1b27efbdb87c84791e32b5037875dd711",
	},
	{
		name:      "synthetic_authoring",
		path:      []string{"..", "..", "..", "..", "venue-svg-generator", "arena_export", "testdata", "synthetic_authoring.svg"},
		canonical: "574617c37fdf268fccf30c4b1baebfdf9dc8008155db9d6329b7ffd593a8e193",
		seatKeys:  "0d9df3d467c341816637d40b2a08d441e7e8341fce28099a17da8a6021f83ac3",
	},
}

func goldenDigests(t *testing.T, g Geometry) (string, string) {
	t.Helper()
	buf, err := CanonicalJSON(g)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sum := sha256.Sum256(buf)
	var keys []string
	for _, sec := range g.Sections {
		for _, row := range sec.Rows {
			for _, seat := range row.Seats {
				keys = append(keys, seat.Key)
			}
		}
	}
	sort.Strings(keys)
	ks := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:]), hex.EncodeToString(ks[:])
}

func TestImport_GoldenChecksumsUnchanged(t *testing.T) {
	t.Parallel()
	for _, tc := range keyGoldenCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(tc.path...))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var g Geometry
			var errs ValidationErrors
			if tc.sbt {
				var plan SBTPlan
				plan, _, errs = ImportSBTSVG(raw)
				g = plan.Geometry
			} else {
				g, _, errs = ImportSVG(raw)
			}
			if len(errs) != 0 {
				t.Fatalf("import errors: %v", errs)
			}
			canonical, seatKeys := goldenDigests(t, g)
			if canonical != tc.canonical {
				t.Errorf("CanonicalJSON sha256 = %s, want %s", canonical, tc.canonical)
			}
			if seatKeys != tc.seatKeys {
				t.Errorf("seat keys sha256 = %s, want %s", seatKeys, tc.seatKeys)
			}
		})
	}
}
