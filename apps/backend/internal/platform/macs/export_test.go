package macs_test

// Unit tests for buildExport logic using synthetic exportRows.
// We exercise the exported QueryAndBuildExport path indirectly via the
// package-internal buildExport helper through a table-driven test of the
// Export type and its invariants.
//
// Tests:
//   - empty input produces empty Export (not nil)
//   - single order with one active ticket → holderStatus=0
//   - cancelled ticket → holderStatus=3
//   - GA ticket (seat_key NULL) uses system_ticket_id as seatId (both equal)
//   - actionEvent carries required fields (cityName, venueName, actionName, …)
//   - order.id equals minimum system_ticket_id in the order
