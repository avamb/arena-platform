# Architecture Decision Log

Обновлено: 2026-06-21

## Назначение

Этот файл собирает решения, которые нужно подтвердить перед master specification. Статус `proposed` означает: это сильный default для старта спецификации, но его должен подтвердить человек. Статус `accepted` можно ставить только после явного подтверждения.

## Status Values

```text
proposed
accepted
rejected
superseded
needs_owner_decision
```

## P0 Decisions For Specification Start

| ID | Decision | Status | Proposed Default | Why |
|---|---|---|---|---|
| ADR-001 | First production scope | proposed | GA-first, scanner/import-first, assigned seating modeled but not fully implemented | Matches current risk review and strongest market pain: multi-seller quotas and gate control |
| ADR-002 | Coding phase gate | accepted | No coding before approved master specification and initial backlog | Already non-negotiable in guardrails |
| ADR-003 | DB stack | proposed | PostgreSQL + Redis + object storage | Fits transactional core, JSONB flexibility, cache/locks and media assets |
| ADR-004 | Backend language and framework shape | accepted | Go as primary backend language; lightweight `net/http`-based modular monolith; exact router proposed default = `chi` | Confirmed by owner on 2026-06-21; backend must be fast, explicit and operationally simple |
| ADR-005 | ID strategy | proposed | Public IDs as strings; internal IDs to confirm; compatibility IDs as external refs | Avoid JS numeric limits and legacy coupling |
| ADR-006 | Tenant model | proposed | Platform -> Organization -> Membership -> SalesChannel -> Event/Session | Aligns roles, reporting, sales channels and permissions |
| ADR-007 | User multi-membership | proposed | One user can hold multiple org memberships and roles | Required for organizer/agent overlap and report dedupe |
| ADR-008 | Payment provider layer | accepted | Providers are adapters; core owns normalized payment records | Already confirmed in architecture brief |
| ADR-009 | First ticket payment provider | needs_owner_decision | Choose based on first market/site; do not hardcode AllPay or Stripe into core | Provider affects payment methods, SCA, payouts and operations |
| ADR-010 | Payout risk policy | proposed | Hybrid payout: delayed/reserve for new organizers, faster payouts for trusted organizers | Resolves instant payout conflict and chargeback/cancellation risk |
| ADR-011 | WordPress checkout mode | proposed | Hosted checkout first; embedded second | Reduces PCI/plugin complexity while preserving future embedded UX |
| ADR-012 | WooCommerce mode | proposed | Bypass WooCommerce for new orders; keep historical orders read-only | Keeps WordPress from owning core order/payment/ticket state |
| ADR-013 | Event pages in WordPress | proposed | Custom Post Type `arena_event` for SEO-visible pages | Preserves SEO/editorial workflow |
| ADR-014 | Scanner first implementation | proposed | Existing scanner boundary, imported barcode batches, signed platform events, offline-capable design | Targets current operational pain without embedding scanner in core |
| ADR-015 | External barcode imports before reconciliation | proposed | Allowed when approved and scoped; financial status remains reported/estimated | Enables event-day operations while preserving reconciliation integrity |
| ADR-016 | External allocations first type | proposed | Quantity/category quotas first; assigned-seat allocation later by real case | Matches GA-first launch and lowers first scope |
| ADR-017 | Complimentary tickets | proposed | Organizer workflow without payment, consumes inventory, issues normal tickets/barcodes, audited | Already core architecture direction |
| ADR-018 | Compliance baseline | proposed | Dedicated compliance/security/privacy architecture required before master spec | Needed for pricing, PCI, SCA, accessibility, privacy and API security |
| ADR-019 | Accessibility target | proposed | WCAG 2.2 AA for buyer-facing platform-controlled flows | Market/legal baseline for EU-facing e-commerce and good product practice |
| ADR-020 | Webhook payload policy | proposed | Lightweight signed notification + fetch full resource by ID/version | Reduces privacy/payload risk and supports idempotency |
| ADR-021 | Social publishing | proposed | Approval-based by default | Prevents accidental public posts and data leakage |
| ADR-022 | Service billing provider | proposed | Stripe Billing can be first adapter; platform owns internal billing | Keeps provider abstraction |
| ADR-023 | Invoice collection mode | proposed | Draft review first for new clients, auto-collection after agreement/payment method | Reduces finance/support risk |
| ADR-024 | Superadmin model | proposed | Rare platform owner/operator accounts, MFA, step-up for high-risk actions | Reduces cross-tenant/security risk |
| ADR-025 | Support impersonation | proposed | Scoped read-only "view as" first; elevated only with explicit permission/reason/audit | Balances support and privacy |
| ADR-026 | Observability stack | proposed | Provider-neutral abstraction; implementation may start with Grafana/Prometheus/Loki/Tempo or deployment tooling | Keeps product-level ops console independent |
| ADR-027 | Ticket credential model | proposed | Ticket identity is separate from credential; static QR/PDF and external barcode imports first; Wallet/rotating QR/NFC reserved by model | Prevents scanner and delivery from being hard-coded around one QR format |
| ADR-028 | Offline scanner trust model | proposed | Scan decisions use explicit authority/source metadata; raw barcode value alone is never globally trusted | Prevents barcode collisions and supports platform, legacy, complimentary and external credentials |
| ADR-029 | API security model | proposed | Scoped service accounts/API keys first, signed webhooks, resource-aware ABAC, rate limits, OAuth/app marketplace later | Provides production security without blocking first integrations |
| ADR-030 | Launch jurisdictions | needs_owner_decision | Do not assume global legal baseline | Pricing, taxes, payment methods, SCA, privacy and accessibility depend on region |
| ADR-031 | All-in pricing posture | proposed | Platform-owned totals with clear fee/tax breakdown and all-in presentation where required | Reduces compliance and buyer-trust risk |
| ADR-032 | Source-of-truth precedence | proposed | Before approval: guardrails + decision log + clarification register override older research conflicts; after approval: master spec wins for implementation | Prevents stale strategy notes from driving implementation |

## Product Strategy Conflicts To Resolve

### Direct Payouts

Conflict:
- Market research frames instant Stripe Connect payouts as a core differentiator.
- Risk review rejects uncontrolled instant payouts.

Decision direction:
- Keep "fast payouts for trusted organizers" as product promise.
- Reject "instant payouts for everyone by default".
- Add payout risk policy to architecture and pricing/contract terms.

### Data Sovereignty vs Discovery

Conflict:
- Strategy positions ArenaSoldOut/Tixgear as sovereign B2B partner.
- Risk review says pure SaaS without discovery is weak for smaller organizers.

Decision direction:
- First spec should include a choice:
  - white-label only
  - opt-in discovery index
  - referral/affiliate channels
  - marketplace later

### Assigned Seating vs Scanner-Led Launch

Conflict:
- Architecture preserves assigned seating and large-venue path.
- Risk review recommends GA-first and scanner/import-first.

Decision direction:
- GA-first implementation.
- Keep seating entities and compatibility path.
- Do not build full seating editor before first production unless a launch customer requires it.

## Required Owner Confirmations Before Master Spec

1. Go scaffold tooling: exact router, migration tool, SQL access generator and pinned Go version. Backend language/framework family is accepted as Go.
2. First production country/region.
3. First payment provider and payment methods.
4. First two sites to migrate.
5. First production scope: GA-only or GA plus assigned seats.
6. Payout risk policy.
7. Whether opt-in discovery/referral is in first spec.
8. Initial superadmin users and MFA policy.
9. Observability/deployment target.
10. Spec language: English, Russian or bilingual.
11. Ticket credential methods for first release.
12. API security model and first external integration auth.
13. Source-of-truth precedence for conflicting docs.

## Decision Update Protocol

When updating a decision:

```text
ID:
Status:
Decision:
Reason:
Alternatives considered:
Affected docs:
Open follow-ups:
```

No master specification section should depend on an unmarked assumption. If a decision is not confirmed, the spec must say `proposed default` or `open decision`.
