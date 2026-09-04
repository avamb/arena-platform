// binding_test.go — feature #450 (W1-0) BINDING key-set test.
//
// Enforces that testdata/wp/bil24_orders_pseudonymized.json matches the
// 36/17/14-key inventory defined in
// 08_architecture/18_bil24_compat_wave1_specification_ru.md §9.3.
//
// This is a documentation-first contract:
//   - The bil24wire encoder (feature #463) MUST produce EXACTLY these key sets
//     for orders, tickets and their nested actionEvent objects.
//   - Any drift (missing or extra key) fails the test.
//
// The check runs WITHOUT the integration tag: it is a pure JSON reader over
// checked-in test data and is safe on the Unit job.
package compat_bil24_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The three key inventories from spec §9.3. Do NOT reorder or reword — these
// are the wire contract to WordPress and to legacy Bil24 clients. Any change
// must land as a spec update first.
var (
	spec93OrderKeys = []string{
		"id", "date", "status", "user", "agent", "frontend", "currency",
		"paymentMethod", "longReservation", "expiration", "processing",
		"sum", "discount", "charge", "totalSum", "ticketQuantity",
		"filteredSum", "filteredDiscount", "filteredCharge", "filteredTotalSum",
		"filteredTicketQuantity",
		"paymentBankMessage", "paymentBankId", "paymentBankStatus",
		"email", "phone", "fullName", "emailSent",
		"seatList", "gatewayOrderList", "acquiring", "ticketList",
		// The four §9.3 payment-provider fields that complete the 36-key
		// inventory. Present in the pseudonymized export (see the
		// TestCompatBil24_450_PseudonymizedFixture_KeySets union) and
		// must be emitted verbatim by bil24wire — legacy WP receivers
		// index into them by name (`paymentRRN` etc.).
		"paymentRRN", "paymentTerminalId", "paymentCardPAN", "paymentCardBank",
	}
	spec93TicketKeys = []string{
		"id", "seatId", "orderId", "seatLocation", "category", "tariff",
		"price", "discount", "charge", "totalPrice", "discountReason",
		"barcode", "barcodeFormat", "actionEvent", "holderStatus",
		"refundDate", "refundPrice",
	}
	spec93ActionEventKeys = []string{
		"id", "cityId", "cityName", "venueId", "venueName", "actionId",
		"actionName", "actionLegalOwner", "actionLegalOwnerInn",
		"actionKind", "currency", "showTime", "eTickets", "gateway",
	}
)

// TestCompatBil24_450_PseudonymizedFixture_Present verifies that the fixture
// file is checked in and non-empty.
func TestCompatBil24_450_PseudonymizedFixture_Present(t *testing.T) {
	path := filepath.Join("testdata", "wp", "bil24_orders_pseudonymized.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("pseudonymized fixture missing: %v", err)
	}
	if info.Size() < 1024 {
		t.Fatalf("pseudonymized fixture is suspiciously small: %d bytes", info.Size())
	}
}

// TestCompatBil24_450_PseudonymizedFixture_KeySets asserts that every order,
// ticket and nested actionEvent in the pseudonymized fixture uses exactly the
// spec §9.3 inventory — no extra keys, no missing keys.
//
// Feature #470 (W1-A1a) made this test strict: the pseudonymized export has
// been reconciled with the 36/17/14 inventory (spec §9.3), and any drift on
// either side (fixture regenerated with extra fields, or bil24wire encoder
// producing a wrong set) MUST fail this test loudly. Do NOT re-add t.Skip —
// binding is the wire contract with WordPress and legacy Bil24 clients.
func TestCompatBil24_450_PseudonymizedFixture_KeySets(t *testing.T) {
	path := filepath.Join("testdata", "wp", "bil24_orders_pseudonymized.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var orders []map[string]interface{}
	if err := json.Unmarshal(data, &orders); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(orders) == 0 {
		t.Fatalf("fixture must contain at least one order")
	}

	wantOrder := toSet(spec93OrderKeys)
	wantTicket := toSet(spec93TicketKeys)
	wantEvent := toSet(spec93ActionEventKeys)

	// Report the first N mismatches only — otherwise CI logs are unreadable.
	const maxReport = 5
	reported := 0
	report := func(format string, args ...interface{}) {
		if reported < maxReport {
			t.Errorf(format, args...)
		}
		reported++
	}

	for oi, o := range orders {
		if extra, missing := diffKeys(o, wantOrder); len(extra) > 0 || len(missing) > 0 {
			report("order[%d] id=%v: extra=%v missing=%v",
				oi, o["id"], extra, missing)
		}
		tickets, _ := o["ticketList"].([]interface{})
		for ti, raw := range tickets {
			tm, ok := raw.(map[string]interface{})
			if !ok {
				report("order[%d].ticketList[%d] is not an object: %T", oi, ti, raw)
				continue
			}
			if extra, missing := diffKeys(tm, wantTicket); len(extra) > 0 || len(missing) > 0 {
				report("order[%d].ticketList[%d] id=%v: extra=%v missing=%v",
					oi, ti, tm["id"], extra, missing)
			}
			ev, ok := tm["actionEvent"].(map[string]interface{})
			if !ok {
				report("order[%d].ticketList[%d].actionEvent is not an object: %T", oi, ti, tm["actionEvent"])
				continue
			}
			if extra, missing := diffKeys(ev, wantEvent); len(extra) > 0 || len(missing) > 0 {
				report("order[%d].ticketList[%d].actionEvent id=%v: extra=%v missing=%v",
					oi, ti, ev["id"], extra, missing)
			}
		}
	}
	if reported > maxReport {
		t.Errorf("... and %d more mismatches (truncated)", reported-maxReport)
	}
}

// toSet builds a keyed set for O(1) lookups.
func toSet(keys []string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

// diffKeys returns (extra, missing) between the actual object keys and the
// spec inventory. Returned slices are sorted for stable error messages.
func diffKeys(obj map[string]interface{}, want map[string]struct{}) (extra, missing []string) {
	got := make(map[string]struct{}, len(obj))
	for k := range obj {
		got[k] = struct{}{}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			extra = append(extra, k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}

// TestCompatBil24_450_PseudonymizedFixture_NoPII scans the pseudonymized
// fixture for values that look like un-scrubbed PII. This is a belt-and-braces
// guard: the file MUST have been pseudonymized before commit (see
// testdata/wp/README.md).
func TestCompatBil24_450_PseudonymizedFixture_NoPII(t *testing.T) {
	path := filepath.Join("testdata", "wp", "bil24_orders_pseudonymized.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	body := strings.ToLower(string(data))
	// Real Vino&Co emails / phones would not use these tokens; the
	// pseudonymizer must replace bare @ addresses with the "example.com" domain
	// or blank strings, and inn with 000000000000.
	suspect := []string{"@gmail.com", "@yandex.ru", "@mail.ru"}
	for _, s := range suspect {
		if strings.Contains(body, s) {
			t.Errorf("pseudonymized fixture leaks live-looking email substring %q", s)
		}
	}
}
