// seat_reservations_test.go — feature #309 (Wave SEAT-C1) unit tests for
// the pure-Go helpers that back the seated POST /v1/reservations branch.
//
// The concurrency contract itself (SELECT … FOR UPDATE + conditional
// UPDATEs) is proven by testcontainers-backed integration tests that live
// under the migrations harness; the coverage here targets the deterministic
// pieces that run outside a transaction so they exercise no PostgreSQL:
//
//   - normalizeSeatKeys — trim / reject-empty / dedupe / ASC sort
//   - seatConflicts     — merges the requested set against the SELECT result
//     and surfaces both unknown and non-available seats
package hcheckout

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestSeatC1_NormalizeSeatKeys pins each branch of the pre-transaction
// canonicaliser so the 400-response codes stay stable for API consumers.
func TestSeatC1_NormalizeSeatKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      []string
		wantOut []string
		wantDup string
		wantErr error
	}{
		{
			name:    "nil in yields nil out",
			in:      nil,
			wantOut: nil,
		},
		{
			name:    "empty in yields nil out",
			in:      []string{},
			wantOut: nil,
		},
		{
			name:    "single key round-trips",
			in:      []string{"Parter|A|1"},
			wantOut: []string{"Parter|A|1"},
		},
		{
			name:    "multiple keys sorted ASC",
			in:      []string{"Parter|A|3", "Parter|A|1", "Parter|A|2"},
			wantOut: []string{"Parter|A|1", "Parter|A|2", "Parter|A|3"},
		},
		{
			name:    "rejects empty seat_key",
			in:      []string{"Parter|A|1", ""},
			wantErr: errEmptySeatKey,
		},
		{
			name:    "rejects duplicate seat_key",
			in:      []string{"Parter|A|1", "Parter|A|1"},
			wantDup: "Parter|A|1",
			wantErr: errDuplicateSeatKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, dup, err := normalizeSeatKeys(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if dup != tc.wantDup {
				t.Fatalf("dupKey = %q, want %q", dup, tc.wantDup)
			}
			if !reflect.DeepEqual(got, tc.wantOut) {
				t.Fatalf("out = %v, want %v", got, tc.wantOut)
			}
		})
	}
}

// TestSeatC1_SeatConflicts covers the four states the merge can produce:
//
//   - all requested keys resolved + available → no conflicts;
//   - a requested key missing from the SELECT result → "unknown";
//   - a resolved row whose status is not 'available' → status echoed back;
//   - mixed conflicts + available seats → conflicts still fully listed.
func TestSeatC1_SeatConflicts(t *testing.T) {
	t.Parallel()

	requested := []string{"Parter|A|1", "Parter|A|2", "Parter|A|3"}
	locked := []gen.SessionSeatRow{
		{ID: uuid.New(), SeatKey: "Parter|A|1", Status: "available"},
		{ID: uuid.New(), SeatKey: "Parter|A|3", Status: "held"},
		// Parter|A|2 intentionally omitted → "unknown"
	}

	got := seatConflicts(requested, locked)
	if len(got) != 2 {
		t.Fatalf("expected 2 conflicts, got %d: %+v", len(got), got)
	}

	byKey := map[string]string{}
	for _, c := range got {
		byKey[c["seat_key"]] = c["status"]
	}
	if byKey["Parter|A|2"] != "unknown" {
		t.Errorf("Parter|A|2 status = %q, want unknown", byKey["Parter|A|2"])
	}
	if byKey["Parter|A|3"] != "held" {
		t.Errorf("Parter|A|3 status = %q, want held", byKey["Parter|A|3"])
	}

	// Sanity: all available → zero conflicts.
	all := []gen.SessionSeatRow{
		{ID: uuid.New(), SeatKey: "Parter|A|1", Status: "available"},
		{ID: uuid.New(), SeatKey: "Parter|A|2", Status: "available"},
	}
	if c := seatConflicts([]string{"Parter|A|1", "Parter|A|2"}, all); len(c) != 0 {
		t.Errorf("expected no conflicts for all-available, got %+v", c)
	}
}

// TestSeatC1_SentinelDistinctness guards against a future refactor that
// aliases these sentinels — the caller uses errors.Is() to branch on the
// specific 400 response so they MUST remain distinct.
func TestSeatC1_SentinelDistinctness(t *testing.T) {
	t.Parallel()

	if errors.Is(errEmptySeatKey, errDuplicateSeatKey) {
		t.Fatal("errEmptySeatKey and errDuplicateSeatKey must be distinct sentinels")
	}
	if errors.Is(errAdmissionSessionNotFound, errAdmissionQuantityNotSupported) {
		t.Fatal("errAdmissionSessionNotFound and errAdmissionQuantityNotSupported must be distinct sentinels")
	}
}
