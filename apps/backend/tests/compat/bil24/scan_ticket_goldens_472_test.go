// scan_ticket_goldens_472_test.go — feature #472 (W1-A1c, spec §7.14):
// pins the SCAN_TICKET/basic.json and SCAN_TICKET/cross_org.json wire
// goldens against the bil24compat envelope layout so a drift between the
// hbil24 handler's response shape and the fixtures fails loudly in the
// Unit CI job (no DB required).
//
// The live cross-org handler branch itself is exercised by
// TestBil24_472_ScanTicket_CrossOrg_Rejected in the hbil24 package.
// This test is a pure envelope-shape guard: resultCode + command are
// pinned, and the description key set is enforced.

package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
)

func TestBil24_472_ScanTicketGoldens_EnvelopeShape(t *testing.T) {
	cases := []struct {
		name           string
		goldenPath     string
		wantResultCode int
		wantKeys       []string // top-level keys the wire is required to carry
	}{
		{
			name:           "basic_ok",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "SCAN_TICKET", "basic.json"),
			wantResultCode: bil24compat.ResultCodeOK,
			wantKeys: []string{
				"resultCode", "description", "command",
				"scanStatus", "ticketId", "platformTicketId",
			},
		},
		{
			name:           "cross_org_rejected",
			goldenPath:     filepath.Join("testdata", "wp", "golden", "SCAN_TICKET", "cross_org.json"),
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
			if cmd, _ := got["command"].(string); cmd != "SCAN_TICKET" {
				t.Errorf("%s: command = %q, want SCAN_TICKET", tc.goldenPath, cmd)
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
