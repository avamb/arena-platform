// Package catalog is the application-layer orchestrator for the catalog
// bounded context: Event, Session, TicketTier (feature #183).
//
// Layer contract (enforced by tests/staticanalysis/catalog_layout_183_test.go):
//
//   - internal/domain/catalog  — pure domain rules: aggregate value types,
//     status enums, transition guards, pricing-mode invariants, sentinel
//     errors. No I/O, no SQL, no HTTP, no logging.
//
//   - internal/app/catalog     — application orchestrators (this package):
//     workflows that combine the domain rules with persistence and adapters
//     (e.g. "publish event → cascade to sessions", "update session capacity
//     → propagate to inventory ledger", "create tier → validate pricing
//     mode → write audit"). Imports internal/domain/catalog and persistence
//     interfaces; does NOT import the HTTP layer.
//
//   - internal/platform/httpserver/{events,sessions,ticket_tiers}.go —
//     thin HTTP handlers. They parse the request, call into this package,
//     and translate results to HTTP responses + JSON bodies.
//
// This package is intentionally empty in the establishment increment of
// feature #183 (the package was created to lock the canonical layout target
// behind a CI gate). Future increments will migrate orchestration code out
// of the HTTP layer into named service types here, one workflow at a time,
// preserving green tests at each step. The order of migration is expected
// to be:
//
//  1. Event lifecycle service (create / update / publish / cancel / archive
//     / delete).
//  2. Session lifecycle service (create / update with capacity propagation
//     hook / status transition / overlap reporting).
//  3. TicketTier lifecycle service (create / update / delete with sale-window
//     guards).
//
// Each step ends with a green `go test ./...` run and an unchanged generated
// OpenAPI client.
package catalog
