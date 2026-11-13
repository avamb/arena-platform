package httpserver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The integration tests of venue_plan.generate read the generator's authoring
// SVG from testdata/venue_gen, so they do not depend on apps/venue-svg-generator
// (CI skips every Go job for a pull request that changes only that folder).
// This test keeps the copy equal to the generator's file: after the fixture is
// regenerated, copy it over.
func TestVenueGenFixtureCopyEqualsTheGeneratorsFile(t *testing.T) {
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "venue-svg-generator", "arena_export", "testdata", "synthetic_authoring.svg"))
	if err != nil {
		t.Fatalf("read the generator's fixture: %v", err)
	}
	cp, err := os.ReadFile(filepath.Join("testdata", "venue_gen", "synthetic_authoring.svg"))
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if !bytes.Equal(norm(src), norm(cp)) {
		t.Fatal("httpserver/testdata/venue_gen/synthetic_authoring.svg differs from apps/venue-svg-generator/arena_export/testdata/synthetic_authoring.svg: copy the generator's file over it")
	}
}
