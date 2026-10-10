// Package payments is the application-layer orchestrator for checkout, payment
// intents, and refunds (feature #185).
//
// Layer contract (enforced by tests/staticanalysis/payments_layout_185_test.go):
//
//   - internal/domain/payments  — pure domain rules: PaymentProvider interface,
//     request/response value types, the module contract and Registry, webhook signature
//     verification helpers, sentinel errors, MockProvider for tests. No I/O,
//     no SQL, no HTTP, no logging.
//
//   - internal/app/payments     — application orchestrators (this package):
//     workflows that combine the domain rules with persistence and adapters
//     (e.g. "create reservation → resolve provider → create intent → record
//     audit"). Imports internal/domain/payments and persistence interfaces;
//     does NOT import the HTTP layer.
//
//   - internal/adapters/{stripe,allpay,stripebilling} — concrete provider
//     adapters. Implement domain interfaces. Import internal/domain/payments
//     only.
//
//   - internal/platform/httpserver/{checkout,payment_intents,refunds}.go —
//     thin HTTP handlers. They parse the request, call into this package, and
//     translate results to HTTP responses + JSON bodies.
//
// Since PAY-01 this package holds ONE thing: modules.go, the list of
// connected payment modules (Modules) — the only place in the codebase where
// provider names appear in a list. Future increments will migrate
// orchestration code from the HTTP layer into named service types here, one
// workflow at a time, preserving green tests at each step.
package payments
