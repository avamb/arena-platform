package seating

import (
	"strings"
	"testing"
)

func TestCanonicalize_StableAcrossPermutations(t *testing.T) {
	t.Parallel()

	base := Geometry{
		Canvas: Canvas{Width: 100, Height: 200},
		Categories: []Category{
			{Index: 2, Name: "B", Color: "#00FF00"},
			{Index: 1, Name: "A", Color: "#FF0000"},
		},
		Sections: []Section{
			{Key: "b", Name: "B", Rows: []Row{
				{Key: "r2", Name: "2", Seats: []Seat{
					{Key: "b|r2|2", Number: "2", CategoryIndex: 1},
					{Key: "b|r2|1", Number: "1", CategoryIndex: 1},
				}},
			}},
			{Key: "a", Name: "A", Rows: []Row{
				{Key: "r1", Name: "1", Seats: []Seat{
					{Key: "a|r1|1", Number: "1", CategoryIndex: 2},
				}},
			}},
		},
	}
	perm := Geometry{
		Canvas: Canvas{Width: 100, Height: 200},
		Categories: []Category{
			{Index: 1, Name: "A", Color: "#ff0000"},
			{Index: 2, Name: "B", Color: "#00ff00"},
		},
		Sections: []Section{
			{Key: "a", Name: "A", Rows: []Row{
				{Key: "r1", Name: "1", Seats: []Seat{
					{Key: "a|r1|1", Number: "1", CategoryIndex: 2},
				}},
			}},
			{Key: "b", Name: "B", Rows: []Row{
				{Key: "r2", Name: "2", Seats: []Seat{
					{Key: "b|r2|1", Number: "1", CategoryIndex: 1},
					{Key: "b|r2|2", Number: "2", CategoryIndex: 1},
				}},
			}},
		},
	}
	sumA, err := Checksum(base)
	if err != nil {
		t.Fatalf("Checksum(base) failed: %v", err)
	}
	sumB, err := Checksum(perm)
	if err != nil {
		t.Fatalf("Checksum(perm) failed: %v", err)
	}
	if sumA != sumB {
		t.Fatalf("checksum mismatch after permutation: %s vs %s", sumA, sumB)
	}
	if len(sumA) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d chars", len(sumA))
	}
}

func TestCanonicalize_ForcesSchemaVersion(t *testing.T) {
	t.Parallel()
	g := Canonicalize(Geometry{})
	if g.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", g.SchemaVersion, SchemaVersion)
	}
	if g.Categories == nil || g.StandingZones == nil || g.Tables == nil {
		t.Fatalf("Canonicalize must produce non-nil slices, got %+v", g)
	}
}

func TestCanonicalJSON_IsSortedKeyed(t *testing.T) {
	t.Parallel()
	g := Geometry{
		Canvas:     Canvas{Width: 1, Height: 1},
		Categories: []Category{{Index: 1, Name: "A", Color: "#000000"}},
	}
	buf, err := CanonicalJSON(g)
	if err != nil {
		t.Fatalf("CanonicalJSON error: %v", err)
	}
	// Field declaration order is stable: schema_version must come first.
	if !strings.HasPrefix(string(buf), `{"schema_version":`) {
		t.Fatalf("canonical JSON must start with schema_version, got %s", string(buf))
	}
}

func TestSeatKey(t *testing.T) {
	t.Parallel()
	got := SeatKey("parter", "1", "5")
	want := "parter|1|5"
	if got != want {
		t.Fatalf("SeatKey = %q, want %q", got, want)
	}
}

func TestSeatCount(t *testing.T) {
	t.Parallel()
	g := Geometry{Sections: []Section{{Rows: []Row{
		{Seats: []Seat{{}, {}, {}}},
		{Seats: []Seat{{}}},
	}}}}
	if n := g.SeatCount(); n != 4 {
		t.Fatalf("SeatCount = %d, want 4", n)
	}
}
