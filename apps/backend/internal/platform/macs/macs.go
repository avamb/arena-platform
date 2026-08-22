// Package macs is the ISOLATED adapter boundary for the Max Mobil Access
// Control System (MACS) external scanning integration.
//
// AB-50 design constraint (owner, 2026-08-22): ALL MACS-shaped mapping —
// integer status codes, integer id/seatId, envelope field names, webhook
// envelopes — lives HERE. Nothing MACS-specific may leak into the catalog or
// ticketing domain. When MACS changes, only files in this package change.
//
// Sub-feature map:
//
//	AB-50a (this sub-feature): migration + integer id scheme (migration 0088).
//	AB-50b: JSON export payload builder.
//	AB-50c: webhooks + HMAC signing + outbox delivery.
//	AB-50d: scanner status-gate + round-trip stub-receiver test.
package macs

// IntStatus maps our ticket states to the four MACS integer status values.
// The MACS system only knows these four integer states; everything else is
// our internal concern.
//
//	1 — PAID / valid (active ticket)
//	2 — USED (check-in recorded by MACS — we never write this value)
//	3 — BLOCKED (unavailable / manually blocked)
//	4 — REFUNDED (cancelled or revoked)
//
// MACS holderStatus values (from the MACS source, NOT Bil24's NEVER_USE):
// 0 not used, 1 checked in, 2 checked out, 3 refunded. MACS conflates
// usage and refund in this one field; the platform keeps them separate
// and collapses only at this boundary. 1 and 2 are MACS-side facts we
// never emit.
const (
	StatusNotUsed    = 0 // valid, not yet scanned — MACS admits the bearer
	StatusCheckedIn  = 1 // MACS-side only
	StatusCheckedOut = 2 // MACS-side only
	StatusRefunded   = 3 // cancelled / revoked / transferred — MACS denies
)

// TicketStatus maps a platform ticket status onto the MACS integer.
// Every non-active (terminal) platform state is a refund at the door;
// unknown values default to refunded (deny is the safe failure).
func TicketStatus(platformStatus string) int {
	if platformStatus == "active" {
		return StatusNotUsed
	}
	return StatusRefunded
}

// SystemSlug is the machine-readable slug under which this platform is
// registered in the ticket_systems table and in MACS's importer registry.
const SystemSlug = "macs"
