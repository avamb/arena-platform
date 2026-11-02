//go:build integration

// scenario02_scan_ticket_p0_test.go — ASY-12 of the P0 gates
// (docs/testing/runs/2026-11-01-p0-1/results.md, section 4): SCAN_TICKET on
// the live wire, on a ticket the harness just bought through the gateway.
// SEND_TICKETS_TO_EMAIL (scenario 02, step 9) and CHECK_KDP (promo_491_test.go,
// promo_0108_test.go) already run live; SCAN_TICKET had only its goldens.

package compat_bil24_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// sc2ScanTicketAtTheGate scans the order's first ticket by its EAN-13: the
// first scan admits, a second scan of the same code and an unknown code are
// refused, and the barcode is stored as scanned.
func sc2ScanTicketAtTheGate(t *testing.T, base string, st *harnessState, orderID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var code string
	if err := st.Pool.QueryRow(ctx, `
		SELECT b.external_ref FROM barcodes b JOIN tickets t ON t.id = b.ticket_id
		 WHERE t.order_id = $1 ORDER BY t.ordinal LIMIT 1`, orderID).Scan(&code); err != nil {
		t.Fatalf("barcode of the bought ticket: %v", err)
	}
	if len(code) != 13 {
		t.Fatalf("barcode %q is not an EAN-13", code)
	}
	scan := func(ticketID string) map[string]interface{} {
		req, _ := loadWPFixture(t, "SCAN_TICKET", "basic")
		req["fid"] = st.ChannelFID
		req["token"] = st.ChannelToken
		req["ticketId"] = ticketID
		return postBil24(t, base, req)
	}

	first := scan(code)
	sc2RequireOK(t, "SCAN_TICKET", first)
	if got, _ := first["scanStatus"].(string); got != "OK" {
		t.Errorf("SCAN_TICKET scanStatus = %v, want OK", first["scanStatus"])
	}
	if got, _ := first["ticketId"].(string); got != code {
		t.Errorf("SCAN_TICKET ticketId = %v, want the scanned code %s", first["ticketId"], code)
	}
	var status string
	if err := st.Pool.QueryRow(ctx, `SELECT status FROM barcodes WHERE external_ref = $1`, code).Scan(&status); err != nil {
		t.Fatalf("barcode status: %v", err)
	}
	if status != "scanned" {
		t.Errorf("barcode status after the scan = %q, want scanned", status)
	}

	again := scan(code)
	if rc, _ := again["resultCode"].(float64); rc != -2 {
		t.Errorf("second SCAN_TICKET of the same code resultCode = %v, want -2 (already scanned); description %v",
			again["resultCode"], again["description"])
	}
	unknown := scan("2999999999999")
	if rc, _ := unknown["resultCode"].(float64); rc != -3 {
		t.Errorf("SCAN_TICKET of an unknown code resultCode = %v, want -3; description %v",
			unknown["resultCode"], unknown["description"])
	}
}
