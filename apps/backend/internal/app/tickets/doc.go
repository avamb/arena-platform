// Package tickets is the application-layer orchestrator for the ticketing
// aggregates: Ticket, ComplimentaryGrant, BarcodeBatch, PromoCode
// (feature #186).
//
// Layer contract (enforced by tests/staticanalysis/tickets_layout_186_test.go):
//
//   - internal/domain/tickets  — pure domain rules: aggregate value types,
//     invariants, state-transition guards, pricing/discount math, sentinel
//     errors. No I/O, no SQL, no HTTP, no logging.
//
//   - internal/app/tickets     — application orchestrators (this package):
//     workflows that combine the domain rules with persistence and adapters
//     (e.g. "issue ticket → claim barcode → enqueue delivery", "revoke
//     complimentary grant → cascade ticket invalidation → write audit",
//     "redeem promo → reserve usage → record outcome"). Imports
//     internal/domain/tickets and persistence interfaces; does NOT import
//     the HTTP layer.
//
//   - internal/platform/httpserver/{tickets,complimentary,barcodes,
//     barcode_batches,promo_codes}.go — thin HTTP handlers. They parse the
//     request, call into this package, and translate results to HTTP
//     responses + JSON bodies.
//
// This package is intentionally empty in the establishment increment of
// feature #186 (the package was created to lock the canonical layout target
// behind a CI gate). Future increments will migrate orchestration code from
// the HTTP layer into named service types here, one workflow at a time,
// preserving green tests at each step.
package tickets
