// Package macs — stub import validation tests (no integration build tag).
//
// TestMACS_AB50e_StubImportValidation uses only the stub HTTP server (no live
// database) so it does not require the integration build tag. It was moved
// out of macs_ab50e_integration_test.go (AB-50g, feature #444) to allow it
// to run in the normal unit-test pass.
//
// AB-50g restores strict validation: seatId > 0, cityName and venueName are
// now required (were previously optional / fabricated). This file verifies
// the new strict behaviour.
package macs_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
)

// TestMACS_AB50e_StubImportValidation verifies the stub's required-field
// validation at /import/tickets without a live database (pure stub test).
func TestMACS_AB50e_StubImportValidation(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	httpPost := func(body any) *http.Response {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(recv.ImportURL(), "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST /import/tickets: %v", err)
		}
		return resp
	}

	// Ticket missing barcode → 422.
	noBarcodeTicket := []map[string]any{{
		"id": 1,
		"ticketList": []map[string]any{{
			"id":     1,
			"seatId": 1,
			// barcode deliberately absent
			"actionEvent": map[string]any{
				"id":               1,
				"cityName":         "Prague",
				"venueName":        "O2 Arena",
				"actionName":       "Concert",
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp := httpPost(noBarcodeTicket)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("missing barcode: want 422, got %d", resp.StatusCode)
	}

	// Ticket with seatId=0 → 422 (AB-50g: seatId > 0 is now required).
	zeroSeatIDTicket := []map[string]any{{
		"id": 1,
		"ticketList": []map[string]any{{
			"id":      99010,
			"seatId":  0, // zero → invalid
			"barcode": "1234567890123",
			"actionEvent": map[string]any{
				"id":               1,
				"cityName":         "Prague",
				"venueName":        "O2 Arena",
				"actionName":       "Concert",
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp = httpPost(zeroSeatIDTicket)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("seatId=0: want 422 (required and must be > 0), got %d", resp.StatusCode)
	}

	// Ticket with empty actionEvent.cityName → 422
	// (AB-50g: cityName is now required; the old permissive fabrication was removed).
	noCityTicket := []map[string]any{{
		"id": 1,
		"ticketList": []map[string]any{{
			"id":      99001,
			"seatId":  99001,
			"barcode": "1234567890",
			"actionEvent": map[string]any{
				"id":               1,
				"cityName":         "", // blank → now required → 422
				"venueName":        "O2 Arena",
				"actionName":       "Concert",
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp = httpPost(noCityTicket)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("missing cityName: want 422 (required since AB-50g), got %d", resp.StatusCode)
	}

	// Ticket with empty actionEvent.venueName → 422
	// (AB-50g: venueName is now required).
	noVenueNameTicket := []map[string]any{{
		"id": 1,
		"ticketList": []map[string]any{{
			"id":      99002,
			"seatId":  99002,
			"barcode": "1234567890",
			"actionEvent": map[string]any{
				"id":               1,
				"cityName":         "Prague",
				"venueName":        "", // blank → now required → 422
				"actionName":       "Concert",
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp = httpPost(noVenueNameTicket)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("missing venueName: want 422 (required since AB-50g), got %d", resp.StatusCode)
	}

	// Ticket missing actionEvent.actionName (truly required) → 422.
	noActionNameTicket := []map[string]any{{
		"id": 2,
		"ticketList": []map[string]any{{
			"id":      99003,
			"seatId":  99003,
			"barcode": "0987654321",
			"actionEvent": map[string]any{
				"id":               2,
				"cityName":         "Prague",
				"venueName":        "O2 Arena",
				"actionName":       "", // blank → invalid (required)
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp = httpPost(noActionNameTicket)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("missing actionName: want 422, got %d", resp.StatusCode)
	}

	// Well-formed ticket with all required fields → 200 and stored.
	validExport := []map[string]any{{
		"id":   1,
		"date": "2026-09-01T10:00:00Z",
		"ticketList": []map[string]any{{
			"id":      99,
			"seatId":  99,
			"orderId": 1,
			"barcode": "9876543210",
			"actionEvent": map[string]any{
				"id":               42,
				"cityName":         "Prague",
				"venueName":        "O2 Arena",
				"actionName":       "Concert",
				"actionLegalOwner": "OrgName",
				"showTime":         "2026-09-01T20:00:00",
			},
		}},
	}}
	resp = httpPost(validExport)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid import: want 200, got %d", resp.StatusCode)
	}
	tk := recv.TicketByID(99)
	if tk == nil {
		t.Fatal("ticket id=99 not in stub store after import")
	}
	if tk.Barcode != "9876543210" {
		t.Errorf("stub ticket barcode = %q, want %q", tk.Barcode, "9876543210")
	}
}
