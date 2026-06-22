# Master Platform Specification Initial Draft

Обновлено: 2026-06-21

Статус: `initial draft`, не утверждено полностью.

Этот файл является начальным черновиком master specification для production-grade ticketing platform. Его нельзя считать готовой спецификацией до закрытия P0 решений из:

- `09_autoforge/01_CLARIFICATION_REGISTER.md`
- `09_autoforge/02_CRITICAL_ARCHITECTURE_AUDIT.md`
- `09_autoforge/03_SPECIFICATION_STARTER.md`
- `08_architecture/09_domain_state_machines_ru.md`
- `08_architecture/10_compliance_security_privacy_ru.md`
- `08_architecture/11_architecture_decision_log_ru.md`
- `08_architecture/13_backend_go_initial_specification_ru.md`

## 1. Purpose And Product Boundary

TBD:

- какую production-систему строим;
- какие интерфейсы входят в первый production profile;
- какие интерфейсы только резервируются архитектурно;
- какие legacy/Bil24/TixGear элементы являются reference, а не зависимостью.

## 2. Confirmed Decisions

Confirmed as of 2026-06-21:

- Backend core primary language: Go.
- Backend architecture shape: lightweight `net/http`-based modular monolith first.
- Scanner runtime is not part of backend core; scanner remains an external boundary integrated through APIs/events/imports.
- Business logic must not live in HTTP handlers.
- Backend scaffold must include API server, worker process, migration command, config validation, logging, health/readiness, PostgreSQL connection, OpenAPI skeleton, request ID/correlation middleware, idempotency boundary, audit/outbox boundary and test harness.

Required sources:

- `08_architecture/11_architecture_decision_log_ru.md`
- `08_architecture/13_backend_go_initial_specification_ru.md`

## 3. Explicit Non-Goals For First Production Profile

TBD:

- что не входит в первый production launch;
- что остается расширяемой архитектурной границей;
- какие решения нельзя реализовывать как временные хаки, потому что они блокируют рынок позже.

Backend non-goals for initial scaffold:

- no scanner runtime inside backend core;
- no frontend/admin UI;
- no WordPress plugin code;
- no payment provider adapter before payment provider decision;
- no payout execution before payout risk policy decision;
- no full seating editor before first production scope decision;
- no microservice split before modular monolith boundaries are proven.

## 4. Market And Compliance Baseline

TBD:

- all-in pricing / fee disclosure;
- PCI scope;
- SCA/3DS;
- privacy and retention;
- accessibility;
- API security baseline;
- fraud, bots and hot-sale protection.

Required source: `08_architecture/10_compliance_security_privacy_ru.md`.

## 5. Domain Model

TBD:

- organizations, users, memberships, roles;
- venues and seating plans;
- events and sessions;
- inventory, reservations, orders;
- payments, refunds, settlements;
- tickets, credentials, scans;
- complimentary issuance;
- external allocations and imports;
- reports, billing, observability.

Required source: `08_architecture/09_domain_state_machines_ru.md`.

## 6. State Machines And Invariants

TBD:

- reservation lifecycle;
- checkout/order/payment lifecycle;
- refund lifecycle;
- ticket and credential lifecycle;
- complimentary lifecycle;
- external allocation lifecycle;
- scanner decision lifecycle;
- payout and invoice lifecycle.

No implementation ticket may redefine these states without updating the architecture docs.

## 7. Interface Families

TBD:

- public checkout;
- WordPress plugin;
- organizer backoffice;
- operator backoffice;
- platform superadmin;
- box office/POS;
- external ticketing system;
- scanner service;
- developer/API console;
- management API clients.

Required sources:

- `08_architecture/02_wordpress_integration_contract_ru.md`
- `08_architecture/03_platform_management_api_and_permissions_ru.md`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `08_architecture/08_platform_superadmin_observability_ru.md`

## 8. API And Integration Contracts

TBD:

- platform-native API;
- management API;
- compatibility gateway;
- WordPress plugin contract;
- scanner webhooks/events;
- payment provider adapters;
- billing provider adapters;
- external allocation/import APIs.

Required sources:

- `08_architecture/01_api_compatibility_gateway_ru.md`
- `08_architecture/06_event_notifications_billing_reporting_ru.md`
- `08_architecture/07_external_allocations_scanner_ingestion_ru.md`

## 9. Backend Technology, Data, Storage, Scale

Accepted backend technology:

- language: Go;
- runtime shape: API process plus worker process;
- initial architecture: modular monolith with internal domain/application/adapters boundaries;
- HTTP foundation: Go standard `net/http`;
- router: `chi` proposed default, still confirm before scaffold;
- database access: PostgreSQL with `pgx`; `sqlc` proposed default;
- migrations: `goose` or Atlas, still confirm before scaffold;
- cache/locks: Redis where needed, PostgreSQL remains source of truth;
- API contract: OpenAPI-first.

Still TBD:

- database stack;
- ID strategy;
- tenancy boundaries;
- audit/event storage;
- inventory concurrency;
- seating geometry versus dynamic status;
- cache and large venue strategy;
- backup, restore, failover.

Required sources:

- `08_architecture/04_large_venue_performance_strategy_ru.md`
- `08_architecture/13_backend_go_initial_specification_ru.md`

## 10. Security, Permissions, Privacy

TBD:

- RBAC + ABAC;
- service accounts;
- API keys/OAuth;
- webhook signatures;
- payment callback verification;
- log masking;
- support impersonation;
- break-glass access;
- data subject rights;
- retention defaults.

Required source: `08_architecture/10_compliance_security_privacy_ru.md`.

## 11. First Production Backlog Groups

TBD:

- foundation;
- identity and organizations;
- catalog and inventory;
- checkout and payment abstraction;
- ticket issuance and credentials;
- scanner sync/import baseline;
- WordPress integration;
- organizer/operator/superadmin console;
- reporting and billing;
- observability and launch readiness.

Required source: `09_autoforge/03_SPECIFICATION_STARTER.md`.

## 12. Definition Of Done

TBD:

- tests;
- contract checks;
- audit events;
- observability;
- security checks;
- migration/reference fixtures;
- docs updated.

## 13. Open Questions

This section must link unresolved questions from `09_autoforge/01_CLARIFICATION_REGISTER.md` and must not silently convert assumptions into confirmed decisions.

## 14. Approval Gate

The master specification becomes implementation-ready only when:

1. P0 decisions are accepted or explicitly overridden.
2. Blocking clarification questions are closed or scoped out.
3. State machines and compliance baseline are referenced by feature groups.
4. First production profile is confirmed.
5. Feature backlog seed and Definition of Done are generated from this file.
