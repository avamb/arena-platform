// Package billing holds the pure-domain layer for the platform billing
// ledger (feature #187 — "DDD split: billing / reporting").
//
// This package contains only pure invariants, state-machine descriptions,
// and side-effect-free helpers used by the billing application services and
// the HTTP adapters. It does NOT import any I/O, persistence, HTTP, or
// third-party adapter packages (such as Stripe). All such concerns live in:
//
//   - internal/app/billing      — orchestration / use-cases (post-invoice,
//                                 push-invoice-to-stripe, handle-webhook).
//   - internal/adapters/...     — concrete adapters (postgres, stripebilling,
//                                 etc.); use-cases depend on ports, not on
//                                 adapter packages directly.
//   - internal/platform/httpserver — thin HTTP transport that calls into the
//                                    application layer.
//
// The split is incremental. This first step extracts the invoice
// state-transition table and the billing-period helper, which are pure
// functions over primitive types and therefore safe to relocate without
// behavioural risk. Subsequent increments will progressively migrate
// orchestration logic out of platform/httpserver/billing_ledger.go and
// adapters/stripebilling into internal/app/billing.
package billing
