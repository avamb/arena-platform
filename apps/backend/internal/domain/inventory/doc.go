// Package inventory is the pure-domain layer for the inventory bounded context
// (feature #184).
//
// Aggregates owned by this layer:
//
//   - Reservation         — a seat-hold owned by a buyer (or anonymous
//     session) for a window of time. Owns the reservation-state lifecycle
//     (draft → active → converted | expired | cancelled, plus the
//     short-circuit draft → cancelled). Owns the TTL precedence rule
//     (channel override → org default → system fallback) as an abstract
//     resolver contract; the concrete persistence-backed resolver lives
//     in the application layer.
//   - ExternalAllocation  — a quota block held by a partner organisation
//     (reseller / box-office) against the platform's inventory. Owns the
//     allocation-status lifecycle (pending → active → reconciled, plus the
//     active → disputed → reconciled side-path and the pending → reconciled
//     short-circuit) and the terminal-state predicate.
//
// The InventoryLedger is the persistent capacity counter that backs both
// aggregates (capacity_total / capacity_held / capacity_sold). The atomic
// arithmetic on those columns is implemented in SQL (sqlc-generated
// ReserveCapacity / ReleaseCapacity / ConfirmCapacity) and lives in the
// adapters layer; the domain layer's contract is to coordinate those
// primitives via the use-cases in internal/app/inventory.
//
// Layer contract (enforced by tests/staticanalysis/inventory_layout_184_test.go):
//
//   - internal/domain/inventory — pure domain rules: state enums, transition
//     guards, terminal-state predicates, sentinel errors. No I/O, no SQL,
//     no HTTP, no logging, no time-of-day side effects, no imports of
//     adapters/* or platform/httpserver/*.
//
//   - internal/app/inventory    — application orchestrators: workflows that
//     combine the domain rules with persistence and adapters (e.g.
//     "create reservation → resolve TTL → reserve capacity → insert row",
//     "activate allocation → reserve capacity", "reconcile allocation →
//     confirm consumed + release remainder"). Imports
//     internal/domain/inventory and persistence interfaces; does NOT import
//     the HTTP layer.
//
//   - internal/platform/httpserver/{reservations,external_allocations,
//     inventory,reservation_processor}.go — thin HTTP / worker handlers.
//     They parse the request, call into the app layer, and translate results
//     to HTTP responses + JSON bodies. The handlers still own the DTO<->row
//     mapping for now; subsequent increments will migrate DTO mapping next
//     to the use-cases.
//
// This package is intentionally minimal in the establishment increment of
// feature #184. It currently hosts the pure status-transition tables for
// Reservation and ExternalAllocation (plus the terminal-state predicate),
// all extracted from the corresponding HTTP handlers so the layer is
// provably alive (a layout gate that contains zero callable code would
// silently rot). Further extractions — the create/activate/cancel
// reservation workflows, the create/activate/reconcile allocation
// workflows, the TTL-resolver port + concrete adapter, and migration of
// the background ReservationProcessor into internal/app/inventory — are
// planned incremental follow-ups, each in its own PR with green tests at
// every step.
package inventory
