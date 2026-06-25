// Package reporting is the application-layer namespace for the
// event-reporting bounded context (feature #187 — "DDD split: billing /
// reporting").
//
// Use-cases that will progressively migrate into this package include:
//
//   - generate-event-report     (orchestrate aggregation queries + line writes,
//                                transition state pending -> generating -> ready)
//   - deliver-event-report      (resolve recipients, render email, send via port)
//
// Ports defined here describe the adapter contracts (aggregation query
// surface, email sender, recipient resolver). The worker handlers currently
// at internal/platform/reporting and internal/platform/reportdelivery will
// migrate into this package one job type at a time; the worker dispatch
// glue remains in the platform layer.
//
// This file is the package skeleton established in feature #187. It locks
// the canonical layout under a static-analysis gate so subsequent moves are
// mechanical and reviewable.
package reporting
