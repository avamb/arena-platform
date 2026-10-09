// session_sale_times.go — a session's sales end and doors-open times
// (migration 0128) on the REST create / update routes.
//
// sales_end_at is always set on the row: the 0128 trigger defaults it to
// start_at and carries it along when the start moves. A client names it only
// to choose another moment — some events sell after the start, for those who
// come late. doors_open_at is optional and display-only, never after the
// start. Both are written by gen.SetSessionSaleTimes AFTER the insert /
// update, in the same transaction for an update.
package hcatalog

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// saleTimesInput is what a request asked for. SalesEndAt nil keeps the
// stored value; SetDoors false leaves doors_open_at alone, true with a nil
// DoorsOpenAt clears it.
type saleTimesInput struct {
	SalesEndAt  *time.Time
	DoorsOpenAt *time.Time
	SetDoors    bool
}

// any reports whether the request touched either time.
func (in saleTimesInput) any() bool { return in.SalesEndAt != nil || in.SetDoors }

// saleTimesError is a 400 the handler writes verbatim.
type saleTimesError struct {
	code, message, field string
}

// parseSaleTimes reads `sales_end_at` (RFC 3339 string, empty or absent =
// keep) and `doors_open_at` (absent = keep, null or "" = clear, RFC 3339 =
// set) and checks the doors against the session's effective start.
func parseSaleTimes(salesEnd *string, doorsRaw json.RawMessage, start time.Time) (saleTimesInput, *saleTimesError) {
	var in saleTimesInput
	if salesEnd != nil && strings.TrimSpace(*salesEnd) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*salesEnd))
		if err != nil {
			return in, &saleTimesError{"session.invalid_sales_end_at", "sales_end_at must be an RFC 3339 timestamp", "sales_end_at"}
		}
		t = t.UTC()
		in.SalesEndAt = &t
	}
	raw := bytes.TrimSpace(doorsRaw)
	if len(raw) > 0 {
		in.SetDoors = true
		if !bytes.Equal(raw, []byte("null")) {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return in, &saleTimesError{"session.invalid_doors_open_at", "doors_open_at must be an RFC 3339 timestamp or null", "doors_open_at"}
			}
			if strings.TrimSpace(s) != "" {
				t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
				if err != nil {
					return in, &saleTimesError{"session.invalid_doors_open_at", "doors_open_at must be an RFC 3339 timestamp or null", "doors_open_at"}
				}
				t = t.UTC()
				in.DoorsOpenAt = &t
			}
		}
	}
	if in.DoorsOpenAt != nil && in.DoorsOpenAt.After(start) {
		return in, &saleTimesError{"session.doors_after_start", "doors_open_at must not be after start_at", "doors_open_at"}
	}
	return in, nil
}
