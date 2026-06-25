// Package bil24compat is the adapter boundary that translates the legacy
// Bil24 command-based JSON wire protocol to/from arena platform domain
// concepts.
//
// Feature #188: DDD-split — Bil24 compatibility adapter boundary.
//
// Layer contract
//
//   - This package owns the Bil24 wire-format surface only: the request/
//     response envelope, the wire result codes, the (locale-stable) error
//     descriptions used on the wire, and the legacy↔platform ID translation
//     helpers.
//   - It does NOT own any platform business logic. Orchestration of domain
//     use-cases (event listing, ticket-tier listing, checkout creation,
//     scan recording, …) lives in internal/app/* and is invoked by the
//     HTTP-layer handlers that mount this adapter.
//   - The Bil24 wire contract is external and frozen. Any behaviour change
//     here is a wire-level regression; the contract test suite under
//     apps/backend/tests/compat/bil24 must continue to pass byte-for-byte
//     across refactors.
//
// Why an adapter package, not a domain package?
//
//   - The Bil24 protocol is not part of arena's ubiquitous language; it is
//     a translation/compatibility surface for a legacy upstream that the
//     platform intends to retire.
//   - Placing the wire format under internal/adapters/ keeps the protocol
//     vocabulary (resultCode, actionId, categoryPriceId, …) off the
//     internal domain language and makes the dependency direction
//     explicit: adapter → app → domain, never the other way.
//
// Scope note (feature #188, step 1)
//
// This first step extracts the pure wire-format pieces — envelope types,
// result codes, ID translation helpers — into this package and re-exports
// them from the HTTP layer for backward compatibility with the existing
// httpserver-package tests (#157). Migration of the per-command handlers
// out of internal/platform/httpserver/bil24_compat.go into thin route
// registrations that delegate to use-cases under internal/app/* is the
// explicit follow-up; doing it in a single step would conflate the
// boundary move with use-case extraction and risk regressions in the
// frozen wire contract.
package bil24compat
