// Package billing is the application-layer namespace for the platform
// billing-ledger bounded context (feature #187 — "DDD split: billing /
// reporting").
//
// Use-cases that will progressively migrate into this package include:
//
//   - post-invoice              (generate a draft invoice from usage records)
//   - issue-invoice             (draft -> issued)
//   - pay-invoice               (issued -> paid)
//   - void-invoice              (draft/issued -> void)
//   - push-invoice-to-stripe    (call the stripebilling adapter via a port)
//   - handle-stripe-webhook     (invoice.paid / invoice.payment_failed -> domain events)
//
// Ports defined here describe the adapter contracts (e.g. a
// PlatformInvoicePusher port that the stripebilling adapter satisfies).
// HTTP transport lives in internal/platform/httpserver and depends on this
// package, not the other way around. Domain rules and the invoice
// state-transition table live in internal/domain/billing.
//
// This file is the package skeleton established in feature #187. It locks
// the canonical layout under a static-analysis gate so subsequent moves
// (one use-case at a time) are mechanical and reviewable.
package billing
