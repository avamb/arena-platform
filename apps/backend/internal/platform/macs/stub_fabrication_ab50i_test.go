// stub_fabrication_ab50i_test.go — unit tests for AB-50i: stub importer
// fabrication of missing orderId, status="PAID", and barcodeFormat="EAN-13".
//
// These tests run without a live database — they only use the in-process
// stub HTTP server.
package macs_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs/stub"
)

// validTicket builds a minimal well-formed ticket payload for the stub's /import/tickets.
func ab50iValidTicket(id int64) map[string]any {
	return map[string]any{
		"id":      id,
		"seatId":  id,
		"barcode": "AB50i-barcode",
		"actionEvent": map[string]any{
			"id":               int64(1),
			"cityName":         "Prague",
			"venueName":        "O2 Arena",
			"actionName":       "AB50i Test Concert",
			"actionLegalOwner": "OrgName",
			"showTime":         "2026-09-01T20:00:00",
		},
	}
}

func ab50iImport(t *testing.T, recv *stub.Receiver, payload any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(payload)
	resp, err := http.Post(recv.ImportURL(), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /import/tickets: %v", err)
	}
	return resp
}

// TestStubFabricationAB50i_OrderID_FabricatedWhenMissing verifies that when
// both the order.id and ticket.orderId are zero/absent, the stub fabricates a
// non-zero OrderID for the stored ticket.
func TestStubFabricationAB50i_OrderID_FabricatedWhenMissing(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	ticket := ab50iValidTicket(8001)
	// orderId deliberately absent (will be 0 after JSON decode)
	payload := []map[string]any{{
		// order.id also absent
		"ticketList": []map[string]any{ticket},
	}}

	resp := ab50iImport(t, recv, payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", resp.StatusCode)
	}

	tk := recv.TicketByID(8001)
	if tk == nil {
		t.Fatal("ticket id=8001 not in stub store after import")
	}
	if tk.OrderID == 0 {
		t.Error("OrderID should be fabricated (non-zero) when absent from import payload")
	}
}

// TestStubFabricationAB50i_OrderID_InheritedFromOrder verifies that when
// ticket.orderId is absent but order.id is set, the stub uses the order ID.
func TestStubFabricationAB50i_OrderID_InheritedFromOrder(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	ticket := ab50iValidTicket(8005)
	// ticket.orderId absent — should inherit from order.id=9005
	payload := []map[string]any{{
		"id":         int64(9005),
		"ticketList": []map[string]any{ticket},
	}}

	resp := ab50iImport(t, recv, payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", resp.StatusCode)
	}

	tk := recv.TicketByID(8005)
	if tk == nil {
		t.Fatal("ticket id=8005 not in stub store after import")
	}
	if tk.OrderID != 9005 {
		t.Errorf("OrderID = %d, want 9005 (inherited from order.id)", tk.OrderID)
	}
}

// TestStubFabricationAB50i_OrderID_PreservedWhenPresent verifies that an
// explicitly set ticket.orderId is preserved unchanged.
func TestStubFabricationAB50i_OrderID_PreservedWhenPresent(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	ticket := ab50iValidTicket(8002)
	ticket["orderId"] = int64(777)
	payload := []map[string]any{{
		"id":         int64(777),
		"ticketList": []map[string]any{ticket},
	}}

	resp := ab50iImport(t, recv, payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", resp.StatusCode)
	}

	tk := recv.TicketByID(8002)
	if tk == nil {
		t.Fatal("ticket id=8002 not in stub store after import")
	}
	if tk.OrderID != 777 {
		t.Errorf("OrderID = %d, want 777 (explicitly provided)", tk.OrderID)
	}
}

// TestStubFabricationAB50i_Status_AlwaysPAID verifies that the stub always
// stores Status="PAID" regardless of what the import payload says.
func TestStubFabricationAB50i_Status_AlwaysPAID(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	ticket := ab50iValidTicket(8003)
	ticket["orderId"] = int64(9003)
	payload := []map[string]any{{
		"id":         int64(9003),
		"ticketList": []map[string]any{ticket},
	}}

	resp := ab50iImport(t, recv, payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", resp.StatusCode)
	}

	tk := recv.TicketByID(8003)
	if tk == nil {
		t.Fatal("ticket id=8003 not in stub store after import")
	}
	if tk.Status != "PAID" {
		t.Errorf("Status = %q, want \"PAID\" (always fabricated)", tk.Status)
	}
}

// TestStubFabricationAB50i_BarcodeFormat_AlwaysEAN13 verifies that the stub
// always stores BarcodeFormat="EAN-13" regardless of what the import payload says.
func TestStubFabricationAB50i_BarcodeFormat_AlwaysEAN13(t *testing.T) {
	recv := stub.New()
	defer recv.Close()

	ticket := ab50iValidTicket(8004)
	ticket["orderId"] = int64(9004)
	payload := []map[string]any{{
		"id":         int64(9004),
		"ticketList": []map[string]any{ticket},
	}}

	resp := ab50iImport(t, recv, payload)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: want 200, got %d", resp.StatusCode)
	}

	tk := recv.TicketByID(8004)
	if tk == nil {
		t.Fatal("ticket id=8004 not in stub store after import")
	}
	if tk.BarcodeFormat != "EAN-13" {
		t.Errorf("BarcodeFormat = %q, want \"EAN-13\" (always fabricated)", tk.BarcodeFormat)
	}
}
