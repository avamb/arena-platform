// cancel_test.go — AB-49 unit tests for the operator ticket cancellation
// request validation. The transactional core (seat release, capacity
// restore, guards) is covered by the live-DB integration suite in
// ticket_cancel_ab49_integration_test.go (package httpserver).
package htickets

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAB49_ReadCancelRequest pins the 400-level validation contract so
// the admin UI error handling stays stable.
func TestAB49_ReadCancelRequest(t *testing.T) {
	t.Parallel()

	amount := int64(2500)
	zero := int64(0)
	_ = amount

	cases := []struct {
		name         string
		body         string
		wantOK       bool
		wantContains string
	}{
		{
			name:         "reject invalid JSON",
			body:         `{`,
			wantContains: "ticket.invalid_body",
		},
		{
			name:         "reject unknown fields",
			body:         `{"reason":"x","refund_mode":"none","extra":1}`,
			wantContains: "ticket.invalid_body",
		},
		{
			name:         "reject empty reason",
			body:         `{"reason":"","refund_mode":"none"}`,
			wantContains: "ticket.reason_required",
		},
		{
			name:         "reject unknown refund_mode",
			body:         `{"reason":"x","refund_mode":"partial"}`,
			wantContains: "ticket.invalid_refund_mode",
		},
		{
			name:         "reject automatic without amount",
			body:         `{"reason":"x","refund_mode":"automatic"}`,
			wantContains: "ticket.invalid_refund_amount",
		},
		{
			name:         "reject automatic with zero amount",
			body:         `{"reason":"x","refund_mode":"automatic","refund_amount":0}`,
			wantContains: "ticket.invalid_refund_amount",
		},
		{
			name:   "accept none",
			body:   `{"reason":"comp ticket","refund_mode":"none"}`,
			wantOK: true,
		},
		{
			name:   "accept manual",
			body:   `{"reason":"refund via stripe dashboard","refund_mode":"manual"}`,
			wantOK: true,
		},
		{
			name:   "accept automatic with amount",
			body:   `{"reason":"customer request","refund_mode":"automatic","refund_amount":2500}`,
			wantOK: true,
		},
	}
	_ = zero

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(tc.body))
			got, ok := readCancelTicketRequest(rec, r)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v want %v; body=%s", ok, tc.wantOK, rec.Body.String())
			}
			if !tc.wantOK {
				if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantContains)) {
					t.Fatalf("response missing %q: %s", tc.wantContains, rec.Body.String())
				}
				return
			}
			if got.Reason == "" {
				t.Fatal("accepted request lost its reason")
			}
		})
	}
}

// TestAB49_ReleaseOutcome pins the ledger-scope rule: any released
// session_seats row means the reservation confirmed session-level
// capacity (nil tier restore); no rows means legacy per-tier restore.
func TestAB49_ReleaseOutcome(t *testing.T) {
	t.Parallel()

	if (TicketReleaseOutcome{}).RowReleased() {
		t.Fatal("empty outcome must not report a released row")
	}
	if !(TicketReleaseOutcome{SeatReleased: true}).RowReleased() {
		t.Fatal("seat release must count as a released row")
	}
	if !(TicketReleaseOutcome{GAUnitReleased: true}).RowReleased() {
		t.Fatal("GA unit release must count as a released row")
	}
}
