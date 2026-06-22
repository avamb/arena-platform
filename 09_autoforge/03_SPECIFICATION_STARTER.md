# Specification Starter

Обновлено: 2026-06-21

## Назначение

Этот файл задает порядок старта master specification после критического аудита. Его цель - не заменить спецификацию, а сделать следующий заход механическим: прочитать входные документы, подтвердить P0 decisions, затем писать master spec без догадок.

## Read Order Before Writing Specification

1. `09_autoforge/00_AGENT_GUARDRAILS.md`
2. `09_autoforge/01_CLARIFICATION_REGISTER.md`
3. `09_autoforge/02_CRITICAL_ARCHITECTURE_AUDIT.md`
4. `09_autoforge/03_SPECIFICATION_STARTER.md`
5. `08_architecture/11_architecture_decision_log_ru.md`
6. `08_architecture/09_domain_state_machines_ru.md`
7. `08_architecture/10_compliance_security_privacy_ru.md`
8. `08_architecture/12_master_platform_specification_ru.md`
9. `08_architecture/13_backend_go_initial_specification_ru.md`
10. Existing architecture docs `00` through `08`

## Specification Can Start When

Specification writing can start when:

- P0 audit blockers are reflected in architecture docs.
- P0 decisions have at least `proposed default` and owner-confirmation questions.
- Domain state machines exist.
- Compliance/security/privacy constraints exist.
- First production scope is explicitly described as a proposed default.
- Open questions are grouped by blocking level.

Specification writing must still mark unconfirmed decisions as `proposed default` or `open decision`.

## Proposed First Production Profile

This profile is a proposed default for owner confirmation:

```text
Release type: production-grade staged launch, not MVP
Primary market pain: multi-seller quota fragmentation and gate control
Admission mode: General Admission first
Assigned seating: modeled in architecture, implementation deferred unless launch requires it
Scanner: existing scanner boundary, offline-capable design, platform events/webhooks
External barcode import: first-class flow before full financial reconciliation, with approval/review
WordPress: platform-native plugin, hosted checkout first, CPT event pages
Backend: Go primary backend, lightweight net/http-based modular monolith first
Payments: provider adapter, no direct provider coupling in core
Payouts: hybrid controlled payout with risk/reserve policy, not instant-by-default
Compliance: all-in pricing, PCI scope minimization, SCA-ready payments, WCAG target, privacy/consent
```

## Master Specification Outline

Recommended output file:

```text
08_architecture/12_master_platform_specification_ru.md
```

Required sections:

1. Product frame and non-goals
2. Confirmed decisions and open decisions
3. First production scope and staged capability model
4. Domain model
5. Tenancy, organizations, roles and permissions
6. Interfaces and sales channels
7. Catalog, events, sessions and venues
8. Inventory, reservations and checkout
9. Payments, refunds, disputes, payouts and reserves
10. Tickets, credentials, delivery and scanner validation
11. Complimentary tickets
12. External allocations and external barcode imports
13. WordPress integration
14. Bil24-compatible gateway
15. Event backbone, webhooks and notifications
16. Reporting and service billing
17. Compliance, security, privacy and accessibility
18. Superadmin, observability and operations
19. Deployment, HA, backup and environments
20. Migration plan and rollout gates
21. Testing strategy and Definition of Done
22. Feature backlog seed

## Feature Backlog Groups

Initial backlog should be grouped by architecture boundary:

```text
foundation
identity_permissions
organizations_sales_channels
catalog_events_sessions
inventory_reservations
checkout_payments
refunds_disputes
tickets_credentials_delivery
scanner_external_barcodes
external_allocations_reconciliation
wordpress_plugin
compatibility_gateway
event_backbone_webhooks
reports_billing
compliance_security_privacy
superadmin_observability
deployment_operations
testing_contracts
```

## Definition Of Done Requirements

Every feature in the first backlog should define:

- owner boundary
- user/interface actor
- permissions
- state transitions touched
- idempotency key behavior
- audit events
- domain events/webhooks
- data retention/privacy impact
- failure/recovery behavior
- tests required
- observability/logging required

## Blocking Questions To Ask First

Use these before writing detailed spec text:

1. Is first production scope GA-first with scanner/import-first?
2. Which first payment provider and payment methods are required?
3. Is hybrid payout/reserve policy accepted?
4. Which country/region compliance rules are first-launch mandatory?
5. Which two WordPress sites are first migration targets?
6. Is opt-in discovery/referral included in first spec or deferred?
7. Which users get initial platform superadmin?
8. Which observability/deployment target is first?
9. Should master spec be Russian, English or bilingual?
10. Which exact Go scaffold tooling is confirmed: router, migration tool, sqlc/DB access and ID strategy?

## Stop Conditions During Specification

Stop and ask if the spec text would:

- select DB, ID strategy, Go router/tooling or production deployment target without confirmation
- promise instant payouts by default
- make WordPress/WooCommerce source of truth
- make scanner part of core
- remove external barcode authority/federation
- implement assigned seating as first production scope without owner confirmation
- skip PCI/SCA/accessibility/privacy implications
- define refunds without ticket/scan/payment state machine
- merge platform operator and external ticketing operator
- give superadmin write access without step-up/audit policy

## Files That Must Be Updated During Specification

When master spec is created or changed, update:

- `08_architecture/11_architecture_decision_log_ru.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`
- `09_autoforge/02_CRITICAL_ARCHITECTURE_AUDIT.md` if blockers are resolved
- relevant architecture source docs `00` through `13`
- `08_architecture/12_master_platform_specification_ru.md`

## Ready Signal

The project is ready for master specification when the next assistant can answer:

```text
What is confirmed?
What is proposed default?
What is still open?
What must not be implemented yet?
Which state machines and compliance constraints govern the spec?
```

If any answer is missing, do not generate implementation tickets.
