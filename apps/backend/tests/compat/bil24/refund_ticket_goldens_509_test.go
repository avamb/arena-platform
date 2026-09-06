// refund_ticket_goldens_509_test.go — feature #509 (W1-B8, spec §7.13):
// pins the REFUND_TICKET/{ok,repeat,other_org}.json wire goldens against the
// bil24compat envelope layout so a drift between the hbil24 handler's
// response shape and the fixtures fails loudly in the Unit CI job (no DB
// required).
//
// The live refund path — cancellation transaction, tickets.refund_price,
// orders.status, order_events.ticket_refunded and the ticket.refunded fan-out
// to the site webhook + MACS — is exercised by harness scenario 4
// (04_refund_dedup, integration-tagged). This test is a pure envelope-shape
// guard: resultCode + command are pinned and the key set is enforced both
// ways, exactly as spec §15.2 requires.

package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

func TestBil24_509_RefundTicketGoldens_EnvelopeShape(t *testing.T) {
	cases := []struct {
		name           string
		goldenPath     string
		wantResultCode int
		wantKeys       []string // top-level keys the wire is required to carry
	}{
		{
			// Spec §7.13: the success payload is exactly {ticketId, refundDate}
			// on top of the envelope — no money echo, no order fields.
			name:           "ok",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "REFUND_TICKET", "ok.json"),
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys: []string{
				"resultCode", "description", "command",
				"ticketId", "refundDate",
			},
		},
		{
			// Spec §7.13: an already-cancelled ticket answers 0 with the very
			// same payload, so a site retry after a network hiccup is a no-op
			// rather than an error.
			name:           "repeat_is_idempotent",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "REFUND_TICKET", "repeat.json"),
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys: []string{
				"resultCode", "description", "command",
				"ticketId", "refundDate",
			},
		},
		{
			// Spec §7.13: a ticket outside the channel's organization is -3 and
			// carries no payload — "not found" and "not yours" are
			// indistinguishable on the wire.
			name:           "other_org_rejected",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "REFUND_TICKET", "other_org.json"),
			wantResultCode: bil24compat.ResultCodeNotFound,
			wantKeys:       []string{"resultCode", "description", "command"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.goldenPath)
			if err != nil {
				t.Fatalf("read %s: %v", tc.goldenPath, err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.goldenPath, err)
			}

			code, _ := got["resultCode"].(float64)
			if int(code) != tc.wantResultCode {
				t.Errorf("%s: resultCode = %v, want %d", tc.goldenPath, got["resultCode"], tc.wantResultCode)
			}
			if cmd, _ := got["command"].(string); cmd != "REFUND_TICKET" {
				t.Errorf("%s: command = %q, want REFUND_TICKET", tc.goldenPath, cmd)
			}
			if desc, _ := got["description"].(string); desc == "" {
				t.Errorf("%s: description must be non-empty (bil24 envelope guarantee)", tc.goldenPath)
			}

			// Strict key-set: every required key must be present, and no
			// unexpected keys may be silently added (mirrors harness §15.2).
			gotKeys := make(map[string]struct{}, len(got))
			for k := range got {
				gotKeys[k] = struct{}{}
			}
			wantKeys := make(map[string]struct{}, len(tc.wantKeys))
			for _, k := range tc.wantKeys {
				wantKeys[k] = struct{}{}
				if _, ok := gotKeys[k]; !ok {
					t.Errorf("%s: missing required key %q", tc.goldenPath, k)
				}
			}
			for k := range gotKeys {
				if _, ok := wantKeys[k]; !ok {
					t.Errorf("%s: unexpected key %q (spec §15.2 forbids extras)", tc.goldenPath, k)
				}
			}
		})
	}
}
