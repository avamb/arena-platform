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
//     ticketId with -3 InvalidRequest, which proves the auth gate passed
//     without touching the barcode tables)
//   - requireToken=false preserves the legacy unauthenticated behaviour
//     (the production config now forbids that combination — see
//     config.validateProduction and pr2_32_390_test.go in platform/config).
package hbil24

import (
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// buildScanHandler wires a Handler for SCAN_TICKET auth tests. barcodeQ is a
// non-nil *gen.Queries over a nil DBTX: the auth guard must reject before any
// barcode query executes, so a DB call would panic and fail the test loudly.
func buildScanHandler(t *testing.T, channelID, orgID uuid.UUID, tokenHash string, requireToken bool) *Handler {
	t.Helper()
	ctxQ := &fakeResCtxWithToken{
		sessionID: uuid.New(),
		orgID:     orgID,
		channelID: channelID,
		tokenHash: tokenHash,
	}
	return New(
		nil, nil, nil, nil,
		gen.New(nil), // barcodeQ non-nil so the handler passes its nil-guard
		nil, nil, nil,
		ReservationDeps{CtxQ: ctxQ},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	).WithRequireToken(requireToken)
}

func TestBil24_390_ScanTicket_MissingToken_Rejected(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","fid":"` + channelID.String() + `","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("missing token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_MissingFid_Rejected(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","token":"scan-secret","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("missing fid should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_WrongToken_Rejected(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, mustBcryptHash(t, "scan-secret"), true)

	body := `{"command":"SCAN_TICKET","fid":"` + channelID.String() +
		`","token":"wrong-token","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("wrong token should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_NoHashConfigured_Rejected(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	// tokenHash="" — the fake channel stores no gateway_token_hash.
	h := buildScanHandler(t, channelID, orgID, "", true)

	body := `{"command":"SCAN_TICKET","fid":"` + channelID.String() +
		`","token":"any-token","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("channel without gateway_token_hash should return %d, got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_UnknownFid_Rejected(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, mustBcryptHash(t, "scan-secret"), true)

	// fid is a valid UUID but not the configured channel → ErrNoRows → -4.
	body := `{"command":"SCAN_TICKET","fid":"` + uuid.New().String() +
		`","token":"scan-secret","ticketId":"BC-1"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeUnauthorized {
		t.Errorf("unknown fid should return %d (Unauthorized), got %d; resp: %v",
			ResultCodeUnauthorized, rc, resp)
	}
}

func TestBil24_390_ScanTicket_CorrectToken_PassesAuth(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, mustBcryptHash(t, "scan-secret"), true)

	// Correct credentials but empty ticketId: the handler must get PAST the
	// auth gate and fail on the ticketId validation (-3 InvalidRequest)
	// without touching the barcode tables (barcodeQ has a nil DBTX — any
	// query would panic).
	body := `{"command":"SCAN_TICKET","fid":"` + channelID.String() +
		`","token":"scan-secret"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("correct token with empty ticketId should return %d (InvalidRequest, past auth), got %d; resp: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

func TestBil24_390_ScanTicket_RequireTokenOff_LegacyPath(t *testing.T) {
	channelID, orgID := uuid.New(), uuid.New()
	h := buildScanHandler(t, channelID, orgID, "", false)

	// requireToken=false: no credential check runs; the empty ticketId is the
	// first rejection. (Production config forbids this combination.)
	body := `{"command":"SCAN_TICKET"}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("requireToken=false with empty ticketId should return %d (InvalidRequest), got %d; resp: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}
