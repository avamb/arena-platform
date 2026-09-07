// cmd_order_493_test.go — feature #493 (W1-B1c, spec §7.8) unit test for the
// GET_ORDER_INFO `userMessage` field. Pure over bil24ErrorWithUserMessage —
// no DB, no Handler — so it stays fast and package-local like the sibling
// bil24_476_order_test.go slice.
package hbil24

import "testing"

// TestBil24_493_ErrorWithUserMessage_DuplicatesDescription pins spec §7.8:
// every GET_ORDER_INFO error response carries a `userMessage` field that
// duplicates `description`, alongside the unchanged resultCode/command
// contract from bil24Error.
func TestBil24_493_ErrorWithUserMessage_DuplicatesDescription(t *testing.T) {
	resp := bil24ErrorWithUserMessage("GET_ORDER_INFO", ResultCodeNotFound, "order not found")

	if resp.ResultCode != ResultCodeNotFound {
		t.Errorf("ResultCode = %d, want %d", resp.ResultCode, ResultCodeNotFound)
	}
	if resp.Description != "order not found" {
		t.Errorf("Description = %q, want %q", resp.Description, "order not found")
	}
	if resp.Command != "GET_ORDER_INFO" {
		t.Errorf("Command = %q, want GET_ORDER_INFO", resp.Command)
	}
	msg, ok := resp.Data["userMessage"]
	if !ok {
		t.Fatal("Data has no userMessage key")
	}
	if msg != "order not found" {
		t.Errorf("userMessage = %v, want it to duplicate description %q", msg, "order not found")
	}
}
