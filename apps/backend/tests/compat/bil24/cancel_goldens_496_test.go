// cancel_goldens_496_test.go — feature #496 (W1-B2c, spec §7.12): pins the
// CANCEL_RESERVATION/{basic,unknown}.json and CANCEL_ORDER/{basic,paid}.json
// wire goldens against the bil24compat envelope layout, mirroring
// refund_ticket_goldens_509_test.go. Pure envelope-shape guard: resultCode +
// command are pinned and the key set is enforced both ways, no DB required.
//
// The live cancel path — ordering.Cancel transition, hcheckout.ReleaseHold,
// and the "already paid -> use REFUND_TICKET" branch — is exercised by the
// package-local handler tests in internal/platform/httpserver/hbil24.

package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

func TestBil24_496_CancelGoldens_EnvelopeShape(t *testing.T) {
	cases := []struct {
		name           string
		goldenPath     string
		wantCommand    string
		wantResultCode int
		wantKeys       []string
	}{
		{
			// Spec §7.12: releasing the hold of an unpaid order is a plain
			// success — no extra payload beyond the envelope.
			name:           "cancel_reservation_basic",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "CANCEL_RESERVATION", "basic.json"),
			wantCommand:    "CANCEL_RESERVATION",
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys:       []string{"resultCode", "description", "command"},
		},
		{
			// Spec §7.12: an id that does not resolve to any order is STILL a
			// success — the legacy WP plugin never inspects the code here, it
			// just wants the round trip to finish so it can drop its cart.
			name:           "cancel_reservation_unknown_id_is_ok",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "CANCEL_RESERVATION", "unknown.json"),
			wantCommand:    "CANCEL_RESERVATION",
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys:       []string{"resultCode", "description", "command"},
		},
		{
			name:           "cancel_order_basic",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "CANCEL_ORDER", "basic.json"),
			wantCommand:    "CANCEL_ORDER",
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys:       []string{"resultCode", "description", "command"},
		},
		{
			// Spec §7.12: a paid (or refunded) order cannot be unwound by
			// CANCEL_ORDER — it must go through REFUND_TICKET instead.
			name:           "cancel_order_already_paid_uses_refund_ticket",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "CANCEL_ORDER", "paid.json"),
			wantCommand:    "CANCEL_ORDER",
			wantResultCode: bil24compat.ResultCodeUserVisible,
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
			if cmd, _ := got["command"].(string); cmd != tc.wantCommand {
				t.Errorf("%s: command = %q, want %s", tc.goldenPath, cmd, tc.wantCommand)
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
