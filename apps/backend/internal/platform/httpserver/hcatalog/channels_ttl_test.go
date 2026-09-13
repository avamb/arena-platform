// channels_ttl_test.go — unit tests for the sales-channel PATCH
// `reservation_ttl_override` tri-state semantics (absent=keep,
// null=clear-to-org-default, value=set) and its positive-integer
// validation.
//
// Background: UpdateSalesChannel used to assign
// `reservation_ttl_override = $8` unconditionally in SQL, so any partial
// update that omitted the field (e.g. the gateway-credential PUT, which only
// ever touches `settings`) silently wiped a configured hold TTL back to the
// organization default. The fix reuses the package-level optionalInt32 type
// (see events.go / events_metadata_ab45d_test.go) for
// updateChannelRequest.ReservationTTLOverride so the handler can tell
// "key absent" from "key present as null" from "key present with a value",
// and threads an explicit set_reservation_ttl_override flag down to the SQL
// layer (channels.sql / channels.sql.go) instead of overloading NULL.
//
// All tests here are pure unit tests — no live PostgreSQL required.
package hcatalog

import (
	"encoding/json"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// updateChannelRequest.ReservationTTLOverride: absent / null / value / invalid
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateChannelRequest_ReservationTTLOverride_Absent_NotPresent(t *testing.T) {
	var req updateChannelRequest
	if err := json.Unmarshal([]byte(`{"name":"New Name"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.ReservationTTLOverride.Present {
		t.Error("absent reservation_ttl_override: Present should be false")
	}
	if req.ReservationTTLOverride.Value != nil {
		t.Errorf("absent reservation_ttl_override: Value should be nil, got %v", req.ReservationTTLOverride.Value)
	}
}

func TestUpdateChannelRequest_ReservationTTLOverride_Null_PresentNilValue(t *testing.T) {
	var req updateChannelRequest
	if err := json.Unmarshal([]byte(`{"reservation_ttl_override":null}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.ReservationTTLOverride.Present {
		t.Error("null reservation_ttl_override: Present should be true")
	}
	if req.ReservationTTLOverride.Value != nil {
		t.Errorf("null reservation_ttl_override: Value should be nil, got %v", req.ReservationTTLOverride.Value)
	}
}

func TestUpdateChannelRequest_ReservationTTLOverride_Value_PresentWithValue(t *testing.T) {
	var req updateChannelRequest
	if err := json.Unmarshal([]byte(`{"reservation_ttl_override":300}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.ReservationTTLOverride.Present {
		t.Error("value reservation_ttl_override: Present should be true")
	}
	if req.ReservationTTLOverride.Value == nil || *req.ReservationTTLOverride.Value != 300 {
		t.Errorf("value reservation_ttl_override: got %v, want 300", req.ReservationTTLOverride.Value)
	}
}

func TestUpdateChannelRequest_ReservationTTLOverride_Invalid_NonIntegerRejected(t *testing.T) {
	var req updateChannelRequest
	err := json.Unmarshal([]byte(`{"reservation_ttl_override":"not-a-number"}`), &req)
	if err == nil {
		t.Fatal("expected unmarshal error for a non-integer reservation_ttl_override, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// resolveInt32 applied to the three PATCH states (mirrors AB-45d's own
// resolveInt32 coverage — reservation_ttl_override reuses the same helper).
// ─────────────────────────────────────────────────────────────────────────────

func TestResolveInt32_ReservationTTLOverride_AbsentKeepsExisting(t *testing.T) {
	existing := ptrInt32(120)
	opt := optionalInt32{Present: false}
	got := resolveInt32(opt, existing)
	if got != existing {
		t.Errorf("absent: got %v, want existing pointer %v", got, existing)
	}
}

func TestResolveInt32_ReservationTTLOverride_NullClearsExisting(t *testing.T) {
	existing := ptrInt32(120)
	opt := optionalInt32{Present: true, Value: nil}
	got := resolveInt32(opt, existing)
	if got != nil {
		t.Errorf("null: got %v, want nil", got)
	}
}

func TestResolveInt32_ReservationTTLOverride_ValueSetsNew(t *testing.T) {
	existing := ptrInt32(120)
	opt := optionalInt32{Present: true, Value: ptrInt32(300)}
	got := resolveInt32(opt, existing)
	if got == nil || *got != 300 {
		t.Errorf("value: got %v, want 300", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidateReservationTTLOverride: nil / positive / zero / negative
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateReservationTTLOverride_Nil_Valid(t *testing.T) {
	if msg := ValidateReservationTTLOverride(nil); msg != "" {
		t.Errorf("nil should be valid (org default), got error %q", msg)
	}
}

func TestValidateReservationTTLOverride_Positive_Valid(t *testing.T) {
	if msg := ValidateReservationTTLOverride(ptrInt32(1)); msg != "" {
		t.Errorf("1 should be valid, got error %q", msg)
	}
	if msg := ValidateReservationTTLOverride(ptrInt32(86400)); msg != "" {
		t.Errorf("86400 should be valid, got error %q", msg)
	}
}

func TestValidateReservationTTLOverride_Zero_Rejected(t *testing.T) {
	if msg := ValidateReservationTTLOverride(ptrInt32(0)); msg == "" {
		t.Error("0 should be rejected")
	}
}

func TestValidateReservationTTLOverride_Negative_Rejected(t *testing.T) {
	if msg := ValidateReservationTTLOverride(ptrInt32(-1)); msg == "" {
		t.Error("-1 should be rejected")
	}
	if msg := ValidateReservationTTLOverride(ptrInt32(-3600)); msg == "" {
		t.Error("-3600 should be rejected")
	}
}
