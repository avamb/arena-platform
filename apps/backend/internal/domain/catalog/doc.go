// Package catalog is the pure-domain layer for the catalog bounded context
// (feature #183).
//
// Aggregates owned by this layer:
//
//   - Event       — a dated occurrence organized by one organization at an
//     optional venue. Owns the event-status lifecycle
//     (draft → published → cancelled|archived) and the i18n-translatable
//     name/description value objects.
//   - Session     — a scheduled instance of an Event with a start/end window,
//     capacity, and its own status lifecycle
//     (draft → scheduled → cancelled|completed). Owns the temporal-overlap
//     rule between Sessions of the same Event.
//   - TicketTier  — a price tier under a Session. Owns the pricing-mode
//     invariants (free / fixed / pwyw) plus PWYW bound rules.
//
// Layer contract (enforced by tests/staticanalysis/catalog_layout_183_test.go):
//
//   - internal/domain/catalog — pure domain rules: aggregate value types,
//     status enums, transition guards, pricing invariants, sentinel errors.
//     No I/O, no SQL, no HTTP, no logging, no time-of-day side effects, no
//     imports of adapters/* or platform/httpserver/*.
//
//   - internal/app/catalog    — application orchestrators: workflows that
//     combine the domain rules with persistence and adapters (e.g.
//     "publish event → cascade to sessions", "update session capacity →
//     propagate to inventory ledger", "create tier → validate pricing mode →
//     write audit"). Imports internal/domain/catalog and persistence
//     interfaces; does NOT import the HTTP layer.
//
//   - internal/platform/httpserver/{events,sessions,ticket_tiers}.go —
//     thin HTTP handlers. They parse the request, call into the app layer,
//     and translate results to HTTP responses + JSON bodies. The handlers
//     still own the DTO<->row mapping for now; subsequent increments will
//     migrate DTO mapping next to the use-cases.
//
// This package is intentionally minimal in the establishment increment of
// feature #183. It currently hosts the pure status-transition tables for
// Event and Session, the Session-overlap predicate, and the pricing-mode
// validator — all extracted from the corresponding HTTP handlers so the layer
// is provably alive (a layout gate that contains zero callable code would
// silently rot). Further extractions (event-publish workflow, capacity
// propagation orchestration, tier sale-window guards) are planned incremental
// follow-ups, each in its own PR with green tests at every step.
package catalog
