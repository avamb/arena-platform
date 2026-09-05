// i18n_goldens_478_test.go — feature #478. Pins the two localized
// RESERVATION seat_taken goldens against the live i18n bundle so any
// drift between the bundle text and the wire byte surface fails loudly.
//
// The goldens live at testdata/wp/golden/RESERVATION/seat_taken_ru.json
// and seat_taken_he.json. They carry a `resultCode: 101` envelope with
// the `description` field pre-rendered from spec section 6's
// bil24.seat_taken key using the fixture params
// {sector:"Parter", row:"3", number:"12"}.
//
// Non-integration: only exercises the adapter package + i18n bundle,
// no DB / HTTP server needed. Runs in the CI Unit job.

package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// TestBil24_478_ReservationSeatTakenGoldensMatchBundle reads the two
// localized goldens and verifies each envelope reproduces exactly what
// bil24compat.OK / Error + LocalizeDescription produce for the fixture
// params. The seat_taken key uses {{.sector}}/{{.row}}/{{.number}}
// template substitution — a drift between the golden and the bundle
// text means either the bundle was changed without regenerating the
// golden or the golden text is wrong.
func TestBil24_478_ReservationSeatTakenGoldensMatchBundle(t *testing.T) {
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatalf("i18n.NewBundle: %v", err)
	}

	params := map[string]any{"sector": "Parter", "row": "3", "number": "12"}

	cases := []struct {
		locale     string
		goldenPath string
	}{
		{"ru", filepath.Join("testdata", "wp", "golden", "RESERVATION", "seat_taken_ru.json")},
		{"he", filepath.Join("testdata", "wp", "golden", "RESERVATION", "seat_taken_he.json")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.locale, func(t *testing.T) {
			raw, err := os.ReadFile(tc.goldenPath)
			if err != nil {
				t.Fatalf("read %s: %v", tc.goldenPath, err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.goldenPath, err)
			}

			// Envelope shape checks: resultCode=101, command=RESERVATION.
			if code, _ := got["resultCode"].(float64); int(code) != bil24compat.ResultCodeUserVisible {
				t.Errorf("%s: resultCode = %v, want %d", tc.goldenPath, got["resultCode"], bil24compat.ResultCodeUserVisible)
			}
			if cmd, _ := got["command"].(string); cmd != "RESERVATION" {
				t.Errorf("%s: command = %q, want %q", tc.goldenPath, cmd, "RESERVATION")
			}

			// Description must equal what the bundle produces for the
			// negotiated locale + spec fixture params.
			wantDesc := bil24compat.LocalizeDescription(
				bundle.LocalizerFor(tc.locale),
				"bil24.seat_taken",
				"seat is already taken",
				params,
			)
			gotDesc, _ := got["description"].(string)
			if gotDesc != wantDesc {
				t.Errorf("%s: description drift\n  got:  %q\n  want: %q",
					tc.goldenPath, gotDesc, wantDesc)
			}
		})
	}
}
