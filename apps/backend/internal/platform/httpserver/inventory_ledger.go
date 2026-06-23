// inventory_ledger.go — supplemental helpers for the inventory ledger (feature #130).
//
// Core handlers (handleListInventory, handleInitInventory, handleReserveCapacity,
// handleReleaseCapacity, handleConfirmCapacity) and request/response types live
// in inventory.go.  This file adds pure utility functions used by handler code
// and tests.
package httpserver

// checkCapacityInvariant returns true when reserving `amount` units would NOT
// violate the GA capacity invariant:
//
//	held + sold + amount ≤ total  (when total IS NOT NULL)
//
// Rules:
//   - nil total → unlimited capacity → always passes
//   - held + sold + amount ≤ total → passes
//   - held + sold + amount >  total → fails (over-capacity)
//
// This pure function is used by unit and concurrency tests to verify the
// invariant logic independently of the database layer.  The actual database
// enforcement uses SELECT FOR UPDATE in a CTE (see inventory_ledger.sql).
func checkCapacityInvariant(total *int32, held, sold, amount int32) bool {
	if total == nil {
		return true // unlimited — always passes
	}
	return held+sold+amount <= *total
}
