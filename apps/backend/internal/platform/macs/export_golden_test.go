// export_golden_test.go — golden-file test for the MACS export builder (AB-50h).
//
// Asserts that buildExport produces JSON with the required field presence
// and types that match what the MACS importer expects.
package macs

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestBuildExport_GoldenFieldTypes verifies that the export output has the
// field presence and types required by the MACS importer. It compares the
// structure of buildExport output against the stored golden file.
func TestBuildExport_GoldenFieldTypes(t *testing.T) {
	// Build a representative export using the same helper from export_unit_test.go.
	row := goldenBaseRow()
	row.promoCodeName = strPtr("SUMMER10")
	// 3 tickets in one order with a promo discount.
	row1 := row
	row1.systemTicketID = 1001
	row1.seatSystemID = 1001
	row1.ordinal = 1

	row2 := row
	row2.systemTicketID = 1002
	row2.seatSystemID = 1002
	row2.ordinal = 2

	row3 := row
	row3.systemTicketID = 1003
	row3.seatSystemID = 1003
	row3.ordinal = 3

	export := buildExport([]exportRow{row1, row2, row3})

	// Marshal to JSON.
	got, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	// Parse the golden file to verify field presence.
	goldenPath := "testdata/sample_tickets.json"
	goldenBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file %s: %v", goldenPath, err)
	}

	var golden []map[string]any
	if err := json.Unmarshal(goldenBytes, &golden); err != nil {
		t.Fatalf("parse golden file: %v", err)
	}

	var actual []map[string]any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatalf("parse actual export: %v", err)
	}

	// Assert field presence on the order.
	orderFields := []string{
		"id", "date", "status", "currency", "sum", "discount",
		"charge", "totalSum", "ticketQuantity", "user", "ticketList",
		"seatList", "gatewayOrderList",
	}
	if len(actual) == 0 {
		t.Fatal("expected at least 1 order in export")
	}
	order := actual[0]
	for _, f := range orderFields {
		if _, ok := order[f]; !ok {
			t.Errorf("order missing required field: %q", f)
		}
	}

	// Assert field presence on a ticket.
	ticketList, _ := order["ticketList"].([]any)
	if len(ticketList) == 0 {
		t.Fatal("expected at least 1 ticket in order")
	}
	ticket, _ := ticketList[0].(map[string]any)
	ticketFields := []string{
		"id", "seatId", "orderId", "seatLocation", "category", "tariff",
		"price", "discount", "charge", "totalPrice", "discountReason",
		"barcode", "barcodeFormat", "actionEvent", "holderStatus",
	}
	for _, f := range ticketFields {
		if _, ok := ticket[f]; !ok {
			t.Errorf("ticket missing required field: %q", f)
		}
	}

	// Assert field presence on actionEvent.
	ae, _ := ticket["actionEvent"].(map[string]any)
	aeFields := []string{"id", "cityName", "venueName", "actionName", "actionLegalOwner", "showTime", "gateway"}
	for _, f := range aeFields {
		if _, ok := ae[f]; !ok {
			t.Errorf("actionEvent missing required field: %q", f)
		}
	}

	// Assert discountReason format: "Промокод {code}".
	dr, _ := ticket["discountReason"].(string)
	if dr != "Промокод SUMMER10" {
		t.Errorf("ticket.discountReason = %q, want %q", dr, "Промокод SUMMER10")
	}
	orderDR, _ := order["discountReason"].(string)
	if orderDR != "Промокод SUMMER10" {
		t.Errorf("order.discountReason = %q, want %q", orderDR, "Промокод SUMMER10")
	}

	// Assert per-ticket discount is prorated (not 0).
	discount, _ := ticket["discount"].(float64)
	charge, _ := ticket["charge"].(float64)
	price, _ := ticket["price"].(float64)
	totalPrice, _ := ticket["totalPrice"].(float64)
	if discount <= 0 {
		t.Errorf("ticket.discount = %v, want > 0 (promo applied)", discount)
	}
	if charge <= 0 {
		t.Errorf("ticket.charge = %v, want > 0", charge)
	}
	if price <= 0 {
		t.Errorf("ticket.price = %v, want > 0", price)
	}
	if totalPrice != charge {
		t.Errorf("ticket.totalPrice = %v, want == charge (%v)", totalPrice, charge)
	}

	// Assert numeric types (JSON numbers).
	if _, ok := order["id"].(float64); !ok {
		t.Errorf("order.id should be a number, got %T", order["id"])
	}
	if _, ok := ticket["id"].(float64); !ok {
		t.Errorf("ticket.id should be a number, got %T", ticket["id"])
	}
	if _, ok := ticket["holderStatus"].(float64); !ok {
		t.Errorf("ticket.holderStatus should be a number, got %T", ticket["holderStatus"])
	}

	// Compare golden file structure (field names only, not values) — BINDING.
	// The golden file is the canonical spec of what MACS requires; a divergence
	// is a defect, not a note. Update testdata/sample_tickets.json when the
	// export shape changes deliberately.
	if len(golden) == 0 {
		t.Error("golden file is empty — testdata/sample_tickets.json must contain at least one order")
	} else {
		goldenOrder := golden[0]
		for _, f := range orderFields {
			if _, ok := goldenOrder[f]; !ok {
				t.Errorf("golden file missing required order field %q — update testdata/sample_tickets.json", f)
			}
		}
		// Check golden ticket fields.
		goldenTicketList, _ := goldenOrder["ticketList"].([]any)
		if len(goldenTicketList) > 0 {
			goldenTicket, _ := goldenTicketList[0].(map[string]any)
			for _, f := range ticketFields {
				if _, ok := goldenTicket[f]; !ok {
					t.Errorf("golden file ticket missing required field %q — update testdata/sample_tickets.json", f)
				}
			}
			goldenAE, _ := goldenTicket["actionEvent"].(map[string]any)
			for _, f := range aeFields {
				if _, ok := goldenAE[f]; !ok {
					t.Errorf("golden file actionEvent missing required field %q — update testdata/sample_tickets.json", f)
				}
			}
		}
	}

	t.Logf("golden-file field presence OK (binding); export has %d order(s), %d ticket(s)", len(actual), len(ticketList))
}

// goldenBaseRow is a copy of baseRow() from export_unit_test.go for use here.
// (Both files are in package macs, so strPtr/i64Ptr are accessible.)
func goldenBaseRow() exportRow {
	eventID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	ticketID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	csID := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	venueID := uuid.MustParse("00000000-0000-0000-0000-000000000005")
	tierID := uuid.MustParse("00000000-0000-0000-0000-000000000006")
	return exportRow{
		ticketID:          ticketID,
		systemTicketID:    1001,
		checkoutSessionID: csID,
		tierID:            &tierID,
		holderEmail:       strPtr("buyer@example.com"),
		ticketStatus:      "active",
		issuedAt:          nil,
		seatKey:           nil,
		seatSector:        nil,
		seatRow:           nil,
		seatNumber:        nil,
		ordinal:           1,
		cancelledAt:       nil,
		refundDate:        nil,
		refundPrice:       nil,
		orderTotal:        2700,
		orderSubtotal:     3000,
		orderDiscount:     300,
		orderCurrency:     "RUB",
		paymentProvider:   strPtr("yookassa"),
		orderCompletedAt:  time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
		orderUserID:       nil,
		sessionStartAt:    time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC),
		eventID:           eventID,
		eventName:         "Summer Fest",
		orgLegalName:      "ООО Организатор",
		orgName:           "Организатор",
		venueID:           venueID,
		venueName:         "Arena Hall",
		cityID:            nil,
		cityName:          strPtr("Moscow"),
		seatSystemID:      1001,
		barcodeStr:        nil,
		tierName:          strPtr("Standard"),
		tierPrice:         i64Ptr(1000),
		soldPrice:         1000,
		promoCodeName:     nil,
		venueTimezone:     nil,
	}
}
