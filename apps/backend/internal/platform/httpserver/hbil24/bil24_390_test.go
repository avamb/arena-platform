// bil24_390_test.go — feature #390 (PR2-32): SCAN_TICKET credential
// enforcement.
//
// The 2026-07-19 adversarial audit found that PR2-25 added fid/token
// enforcement to RESERVATION and UN_RESERVE but left SCAN_TICKET — which
// mutates state via MarkBarcodeScanned — completely unauthenticated. These
// tests prove the guard now runs BEFORE any barcode lookup or mutation:
//
//   - missing token   → resultCode -4 (Unauthorized)
//   - missing fid     → resultCode -4
//   - wrong token     → resultCode -4
//   - no hash stored  → resultCode -4
//   - correct token   → proceeds past auth (fails later on the empty
//     ticketId with -2 InvalidRequest, which proves the auth gate passed
//     without touching the barcode tables)
//   - requireToken=false preserves the legacy unauthenticated behaviour
//     (the production config now forbids that combination — see
//     config.validateProduction and pr2_32_390_test.go in platform/config).
//
// Feature #472 (W1-A1c) rewired the SCAN_TICKET handler to the unified
// authenticateCommand path (fid = display_number, spec §5.2). The
// UUID-shaped fid + validateScanTicketToken pair was deleted with
// GetSalesChannelByIDGlobal, so these tests now speak the display_number
// wire form via a fakeChannelLookup — the same fake that auth_471_test
// uses for GET_ALL_ACTIONS.
package hbil24

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// buildScanHandler wires a Handler for SCAN_TICKET auth tests. barcodeQ is a
// non-nil *gen.Queries over a nil DBTX: the auth guard must reject before any
// barcode query executes, so a DB call would panic and fail the test loudly.
//
// displayNumber is the numeric fid the caller must send. When tokenHash is
// empty the channel's settings carry an enabled=true gateway block with no
// token_hash, so the "no hash configured" branch fires under
// requireToken=true.
func buildScanHandler(t *testing.T, displayNumber int64, orgID uuid.UUID, tokenHash string, requireToken bool) *Handler {
	t.Helper()
	channelID := uuid.New()
	var settings json.RawMessage
	if tokenHash == "" {
		settings = json.RawMessage(`{"gateway":{"enabled":true}}`)
	} else {
		var err error
		settings, err = json.Marshal(map[string]any{
			"gateway": map[string]any{"enabled": true, "token_hash": tokenHash},
		})
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
	}
	ch := gen.SalesChannelRow{
		ID:            channelID,
		OrgID:         orgID,
		DisplayNumber: displayNumber,
		Name:          "test-channel",
		Settings:      settings,
	}
	lookup := &fakeChannelLookup{
		byDisplayNumber: map[int64]gen.SalesChannelRow{displayNumber: ch},
	}
	return New(
		nil, nil, nil, nil,
		gen.New(nil), // barcodeQ non-nil so the handler passes its nil-guard
		nil, nil, nil,
		ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	).WithChannelLookup(lookup).WithRequireToken(requireToken)
}

func TestBil24_390_ScanTicket_MissingToken_Rejected(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","fid":"1271","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("missing token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_MissingFid_Rejected(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","token":"scan-secret","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("missing fid should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_WrongToken_Rejected(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wrong-token","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("wrong token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_NoHashConfigured_Rejected(t *testing.T) {
	// tokenHash="" — the fake channel stores no gateway_token_hash.
	h := buildScanHandler(t, 1271, uuid.New(), "", true)

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"any-token","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("channel without gateway_token_hash should return %d, got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_UnknownFid_Rejected(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), mustBcryptHash(t, "scan-secret"), true)

	// fid is a valid int64 but not the configured display_number → ErrNoRows → -4.
	body := `{"command":"SCAN_TICKET","fid":"9999","token":"scan-secret","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("unknown fid should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_CorrectToken_PassesAuth(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), mustBcryptHash(t, "scan-secret"), true)

	// Correct credentials but empty ticketId: the handler must get PAST the
	// auth gate and fail on the ticketId validation (-2 InvalidRequest)
	// without touching the barcode tables (barcodeQ has a nil DBTX — any
	// query would panic).
	body := `{"command":"SCAN_TICKET","fid":"1271","token":"scan-secret"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("correct token with empty ticketId should return %d (InvalidRequest, past auth), got %d; resp: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

func TestBil24_390_ScanTicket_RequireTokenOff_LegacyPath(t *testing.T) {
	h := buildScanHandler(t, 1271, uuid.New(), "", false)

	// requireToken=false: no credential check runs; the empty ticketId is the
	// first rejection. (Production config forbids this combination.)
	body := `{"command":"SCAN_TICKET"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("requireToken=false with empty ticketId should return %d (InvalidRequest), got %d; resp: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}
