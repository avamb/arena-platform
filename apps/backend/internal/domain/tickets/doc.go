// Package tickets is the pure-domain layer for the ticketing aggregates
// (feature #186).
//
// Aggregates owned by this layer:
//
//   - Ticket             — issued admission instance, including state
//     transitions (issued → valid → scanned/voided/refunded).
//   - ComplimentaryGrant — non-paid ticket grant with grant + revocation
//     invariants.
//   - BarcodeBatch       — pre-generated barcode pool (allocation, claim,
//     and uniqueness rules).
//   - PromoCode          — discount voucher with validity windows, usage
//     limits, and discount math.
//
// Layer contract (enforced by tests/staticanalysis/tickets_layout_186_test.go):
//
//   - internal/domain/tickets — pure domain rules: aggregate value types,
//     invariants, state-transition guards, pricing/discount math, sentinel
//     errors. No I/O, no SQL, no HTTP, no logging, no time-of-day side effects.
//
//   - internal/app/tickets    — application orchestrators: workflows that
//     combine the domain rules with persistence and adapters (e.g.
//     "issue ticket → reserve barcode → enqueue delivery"). Imports
//     internal/domain/tickets and persistence interfaces; does NOT import
//     the HTTP layer.
//
//   - internal/platform/httpserver/{tickets,complimentary,barcodes,
//     barcode_batches,promo_codes}.go — thin HTTP handlers. They parse the
//     request, call into the app layer, and translate results to HTTP
//     responses + JSON bodies.
//
// This package is intentionally minimal in the establishment increment of
// feature #186. It currently hosts the pure discount-math helper extracted
// from the promo_codes HTTP handler so the layer is provably alive (a layout
// gate that contains zero callable code would silently rot). Further
// extractions (ticket state machine, complimentary revocation rules,
// barcode-batch claim algorithm) are planned incremental follow-ups, each in
// its own PR with green tests at every step.
package tickets
