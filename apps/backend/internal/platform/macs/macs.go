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
// Design note: a ticket cancelled with refund_mode = 'none' (comp revocation)
// still reaches MACS as status 4 — the best available label given the four
// integer states. The real long-term fix (adding a 5th "revoked" state) belongs
// on the MACS backlog, not here.
const (
	StatusValid    = 1 // active ticket; MACS admits bearer
	StatusUsed     = 2 // MACS-side only; we never emit this
	StatusBlocked  = 3 // unavailable / admin-blocked
	StatusRefunded = 4 // cancelled or revoked; MACS denies bearer
)

// TicketStatus returns the MACS integer status for a given platform ticket
// status string. Unknown states default to StatusRefunded (safe: deny at door).
func TicketStatus(platformStatus string) int {
	switch platformStatus {
	case "active":
		return StatusValid
	case "cancelled":
		return StatusRefunded
	default:
		return StatusRefunded
	}
}

// SystemSlug is the machine-readable slug under which this platform is
// registered in the ticket_systems table and in MACS's importer registry.
const SystemSlug = "macs"
