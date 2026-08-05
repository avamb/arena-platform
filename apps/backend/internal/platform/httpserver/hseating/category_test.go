// category_test.go — AB-39 (feature #429) unit tests for the per-seat
// price category assignment endpoint.
//
// The tests cover the pure-Go helpers: request validation, seat
// partitioning (target / unknown / conflict), and JSON decoding. Live
// PostgreSQL coverage of the full transaction (409 conflict envelope,
// seat_status_version bump, audit event) lives in a separate
// integration-tagged file.
package hseating

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestAB39_ValidateCategoryPatchRequest pins the pre-transaction
// validator so the 4xx error codes stay stable for the admin UI.
func TestAB39_ValidateCategoryPatchRequest(t *testing.T) {
	t.Parallel()

	goodTier := uuid.New().String()

	cases := []struct {
		name         string
		req          categoryPatchRequest
		wantOK       bool
		wantStatus   int
		wantContains string
	}{
		{
			name:         "reject empty tier_id",
			req:          categoryPatchRequest{TierID: "", SeatKeys: []string{"A|1|1"}},
			wantStatus:   http.StatusBadRequest,
			wantContains: "seating.invalid_tier_id",
		},
		{
			name:         "reject non-uuid tier_id",
			req:          categoryPatchRequest{TierID: "not-a-uuid", SeatKeys: []string{"A|1|1"}},
			wantStatus:   http.StatusBadRequest,
			wantContains: "seating.invalid_tier_id",
		},
		{
			name:         "reject empty seat_keys",
			req:          categoryPatchRequest{TierID: goodTier, SeatKeys: nil},
			wantStatus:   http.StatusBadRequest,
			wantContains: "seating.no_seat_keys",
		},
		{
			name:   "accept minimal payload",
			req:    categoryPatchRequest{TierID: goodTier, SeatKeys: []string{"A|1|1"}},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPatch, "/x", nil)
			_, ok := validateCategoryPatchRequest(rec, r, tc.req)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v want %v; body=%s", ok, tc.wantOK, rec.Body.String())
			}
			if tc.wantOK {
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantContains)) {
				t.Fatalf("body missing %q: %s", tc.wantContains, rec.Body.String())
			}
		})
	}
}

// TestAB39_ValidateCategoryPatchRequest_TooManySeatKeys pins the 422
// too_many_seat_keys guard so an operator with a very large hall gets a
// clear error rather than a truncated silent success. Split out from
// the parallel table so the huge slice is only built when this case runs.
func TestAB39_ValidateCategoryPatchRequest_TooManySeatKeys(t *testing.T) {
	t.Parallel()

	keys := make([]string, categoryMaxSeats+1)
	for i := range keys {
		keys[i] = "k"
	}
	req := categoryPatchRequest{TierID: uuid.New().String(), SeatKeys: keys}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/x", nil)
	if _, ok := validateCategoryPatchRequest(rec, r, req); ok {
		t.Fatal("expected too_many_seat_keys rejection")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d want 422", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("seating.too_many_seat_keys")) {
		t.Fatalf("body missing too_many_seat_keys: %s", rec.Body.String())
	}
}

// TestAB39_PartitionSeatKeys covers the AB-39 contract that:
//   - keys resolving to session_seats with status held or sold move to
//     conflicts, blocking the whole request via a 409;
//   - keys resolving to available or unavailable move to target;
//   - unknown keys move to unknown (soft-report, do not block);
//   - duplicates in the input dedupe on the way through.
func TestAB39_PartitionSeatKeys(t *testing.T) {
	t.Parallel()

	held := gen.SessionSeatRow{ID: uuid.New(), SeatKey: "held", Status: "held"}
	sold := gen.SessionSeatRow{ID: uuid.New(), SeatKey: "sold", Status: "sold"}
	avail := gen.SessionSeatRow{ID: uuid.New(), SeatKey: "avail", Status: "available"}
	unavail := gen.SessionSeatRow{ID: uuid.New(), SeatKey: "unavail", Status: "unavailable"}
	seatByKey := map[string]gen.SessionSeatRow{
		"held":    held,
		"sold":    sold,
		"avail":   avail,
		"unavail": unavail,
	}

	target, unknown, conflicts := partitionSeatKeys(
		[]string{"avail", "unavail", "held", "sold", "typo", "avail", "typo"},
		seatByKey,
	)

	// target: avail + unavail (sorted).
	if len(target) != 2 || target[0] != "avail" || target[1] != "unavail" {
		t.Fatalf("target = %v want [avail unavail]", target)
	}
	// conflicts: held + sold (sorted).
	if len(conflicts) != 2 || conflicts[0] != "held" || conflicts[1] != "sold" {
		t.Fatalf("conflicts = %v want [held sold]", conflicts)
	}
	// unknown: typo (deduped).
	if len(unknown) != 1 || unknown[0] != "typo" {
		t.Fatalf("unknown = %v want [typo]", unknown)
	}
}

// TestAB39_ReadRequest_StrictDecode ensures the decoder rejects unknown
// fields — a typo in the payload should surface immediately, not silently
// pass through as an empty reassignment.
func TestAB39_ReadRequest_StrictDecode(t *testing.T) {
	t.Parallel()

	body := bytes.NewBufferString(`{"tier_id":"01929d0e-0e47-7000-8000-000000000701","seat_keys":["A|1|1"],"extra":true}`)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/x", body)
	if _, ok := readCategoryPatchRequest(rec, r); ok {
		t.Fatal("expected decode failure on unknown field")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("seating.invalid_body")) {
		t.Fatalf("body missing seating.invalid_body: %s", rec.Body.String())
	}
}

// TestAB39_ReadRequest_HappyPath pins the request wire shape so the
// admin UI cannot rename `tier_id` or `seat_keys` without breaking the
// contract.
func TestAB39_ReadRequest_HappyPath(t *testing.T) {
	t.Parallel()

	tid := uuid.New().String()
	body := bytes.NewBufferString(`{"tier_id":"` + tid + `","seat_keys":["A|1|1","A|1|2"]}`)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/x", body)
	got, ok := readCategoryPatchRequest(rec, r)
	if !ok {
		t.Fatalf("decode failed: %s", rec.Body.String())
	}
	if got.TierID != tid {
		t.Fatalf("tier_id = %q want %q", got.TierID, tid)
	}
	if len(got.SeatKeys) != 2 || got.SeatKeys[1] != "A|1|2" {
		t.Fatalf("seat_keys = %v", got.SeatKeys)
	}
}
