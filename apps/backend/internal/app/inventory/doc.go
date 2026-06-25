// Package inventory is the application-layer orchestrator for the inventory
// bounded context: Reservation, ExternalAllocation, and the
// InventoryLedger primitives that back them (feature #184).
//
// Layer contract (enforced by tests/staticanalysis/inventory_layout_184_test.go):
//
//   - internal/domain/inventory  — pure domain rules: aggregate value types,
//     state enums, transition guards, terminal-state predicates, sentinel
//     errors. No I/O, no SQL, no HTTP, no logging.
//
//   - internal/app/inventory     — application orchestrators (this package):
//     workflows that combine the domain rules with persistence and
//     adapters. Examples:
//
//   - "create reservation"   — resolve TTL (channel override → org
//     default → system fallback), open a transaction, reserve capacity
//     (returns 409 on over-capacity), insert the reservation row, and
//     commit.
//
//   - "activate reservation" — guard the draft→active transition,
//     enforce the not-expired invariant, update the state.
//
//   - "cancel reservation"   — guard the transition, update the state,
//     release the held capacity (non-fatal release: the cancel
//     succeeds even if the ledger update fails — to be revisited
//     alongside the outbox-based event flow).
//
//   - "expire reservations"  — the background-worker workflow: poll
//     expired rows with FOR UPDATE SKIP LOCKED, release capacity,
//     transition to "expired", cascade to linked checkout sessions.
//
//   - "create / activate allocation" — same pattern, reserving platform
//     inventory for partner-org quota blocks.
//
//   - "reconcile allocation" — confirm consumed (held → sold), release
//     the remainder, and transition to "reconciled".
//
//     This package imports internal/domain/inventory and persistence
//     interfaces; it does NOT import the HTTP layer.
//
//   - internal/platform/httpserver/{reservations,external_allocations,
//     inventory,reservation_processor}.go — thin HTTP and worker handlers.
//     They parse the request, call into this package, and translate
//     results to HTTP responses + JSON bodies.
//
// This package is intentionally empty in the establishment increment of
// feature #184 (the package was created to lock the canonical layout
// target behind a CI gate). Future increments will migrate orchestration
// code out of the HTTP layer into named service types here, one workflow
// at a time, preserving green tests at each step. The order of migration
// is expected to be:
//
//  1. TTL-resolver port + concrete adapter (extract resolveReservationTTL
//     from httpserver/reservations.go; the lookup interfaces become ports
//     here, and the resolver becomes a method on a small service).
//  2. Reservation lifecycle service (create / activate / cancel) — the
//     POST/PATCH/DELETE handlers become thin adapters that delegate.
//  3. Reservation-expiry service (replaces httpserver/reservation_processor.go
//     in the worker binary; cascades to checkout via a port).
//  4. ExternalAllocation lifecycle service (create / activate / patch /
//     reconcile).
//
// Each step ends with a green `go test ./...` run and an unchanged
// generated OpenAPI client.
package inventory
