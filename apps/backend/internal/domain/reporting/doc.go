// Package reporting holds the pure-domain layer for the event-reporting
// bounded context (feature #187 — "DDD split: billing / reporting").
//
// Scope:
//
//   - Report definitions and the canonical line categories (sales, refunds,
//     complimentary, scans, commissions, payouts).
//   - Schedule descriptors (when post-event report generation fires).
//   - Delivery contracts (recipient resolution rules, email envelope shape).
//
// This package is pure: no I/O, no persistence, no email transport. All such
// concerns live in:
//
//   - internal/app/reporting              — orchestration (generate report,
//     deliver report).
//   - internal/platform/reporting         — worker handler currently hosting
//     the event.generate_report job
//     (to be migrated incrementally).
//   - internal/platform/reportdelivery    — worker handler currently hosting
//     the report.deliver job
//     (to be migrated incrementally).
//
// This file is the package skeleton established in feature #187. It locks
// the canonical layout under a static-analysis gate so subsequent moves
// (one helper / one rule at a time) are mechanical and reviewable.
package reporting
