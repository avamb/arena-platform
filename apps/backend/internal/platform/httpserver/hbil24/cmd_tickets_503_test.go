// cmd_tickets_503_test.go — feature #503 (W1-B6b, spec §11): SCAN_TICKET
// must resolve tickets by their platform-minted EAN-13 barcode number, not
// just the legacy Bil24 barcode format.
//
// GetBarcodeByExternalRefAny (used by handleBil24ScanTicket) already
// searches across every barcode authority keyed by external_ref alone, so
// an EAN-13 code (authority=platform, minted by feature #502's issuance
// path or feature #503's tickets.backfill_ean13 job) is resolved exactly
// like any other barcode with no additional production code required.
// This test proves that end-to-end using the same in-memory ScanQuerier
// fake the #472 org-scope tests use, with a real checksum-valid EAN-13
// code as the external_ref.
package hbil24

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

// TestBil24_503_ScanTicket_ResolvesByEAN13 proves SCAN_TICKET succeeds when
// the WordPress side sends the platform's EAN-13 number as ticketId.
func TestBil24_503_ScanTicket_ResolvesByEAN13(t *testing.T) {
	org := uuid.New()
	tokenHash := mustBcryptHash(t, "wp-secret")

	// A real platform EAN-13 code: prefix "21" + a system_ticket_id body +
	// GS1 check digit, exactly what tickets.go (issuance) and
	// backfill.go (tickets.backfill_ean13) mint.
	eanCode := ean13.Encode("21", 4242)
	if !ean13.Valid(eanCode) {
		t.Fatalf("test fixture ean13.Encode(21, 4242) = %q is not checksum-valid", eanCode)
	}

	h, _ := buildScanCrossOrgHandler(t, org, org, tokenHash, eanCode)

	body := `{"command":"SCAN_TICKET","fid":"1271","token":"wp-secret","ticketId":"` + eanCode + `"}`
	resp := postJSON(t, h, body)

	rc := mustResultCode(t, resp)
	if rc != ResultCodeOK {
		t.Fatalf("EAN-13 scan must return %d (OK), got %d; resp=%v", ResultCodeOK, rc, resp)
	}
	if scanStatus, _ := resp["scanStatus"].(string); scanStatus != "OK" {
		t.Errorf("expected scanStatus=OK, got %q", scanStatus)
	}
	if platformID, _ := resp["platformTicketId"].(float64); int64(platformID) != 9001 {
		t.Errorf("expected platformTicketId=9001, got %v", resp["platformTicketId"])
	}
}
