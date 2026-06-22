# AutoForge Agent Guardrails

Обновлено: 2026-06-21

## Назначение

Этот документ задает обязательные правила для AutoForge при подготовке спецификации, feature backlog и будущей реализации новой ticketing platform.

AutoForge может задавать вопросы и уточнять детали. Это не только разрешено, но обязательно, если решение влияет на архитектуру, безопасность, данные, платежи, права доступа, API contract или миграцию.

## Non-Negotiable Rules

1. Не начинать coding phase, пока master specification и initial feature backlog не подтверждены человеком.
2. Не называть staged launch словом `MVP`. Мы делаем production-grade систему с постепенным включением возможностей.
3. Не использовать Bil24/TixGear API как внутреннее ядро новой платформы.
4. Использовать Bil24/TixGear только как reference для доменной логики, команд, edge cases и миграционной совместимости.
5. Не переносить текущую WordPress/WooCommerce/Bil24 интеграцию как целевую архитектуру.
6. Для наших WordPress-сайтов целевой путь - новый platform-native WordPress-плагин.
7. Bil24-compatible API - отдельный adapter/gateway, не core API.
8. Payment providers не подключаются напрямую в ticketing core. Нужен независимый payment provider layer.
9. Auth не должен зависеть только от подтвержденного email.
10. Scanner service остается отдельным сервисом/границей, интегрируется через webhooks/events.
11. External apps должны иметь возможность полного управления платформой через API, если права это разрешают.
12. Venue - переиспользуемая физическая сущность. SeatingPlan - отдельная защищенная сущность с владельцем, версиями и правами.
13. Организатор не может изменять чужой seating plan. Он может использовать read-only plan, если доступ разрешен, либо сделать fork/copy.
14. Все mutating operations должны проектироваться с idempotency, audit log и permission checks.
15. Любые финансовые totals должны приходить из платформы как source of truth, а не пересчитываться внешним приложением.
16. Архитектура должна сохранять путь к large-venue/high-demand режиму: static schema отдельно от dynamic seat status, precomputed/precompressed layout assets, dynamic seat-status cache, atomic reservations, backpressure.
17. Interface taxonomy должна явно различать public checkout, agent sales, box office/POS, external ticketing systems, organizer/operator backoffice, management API clients, scanner service и developer/API console.
18. Complimentary invitation tickets - отдельный no-payment issuance flow. Их нельзя проектировать как обычную оплату с нулевой суммой без явных permissions, inventory impact, reason, delivery state и audit log.
19. Интеграции должны быть event-driven: изменения catalog/events/sessions, inventory, orders, payments, refunds, tickets, scans, billing и reports должны публиковать durable events/webhooks, чтобы клиенты не поллили API постоянно.
20. Service billing для клиентов/организаторов/агентов отделен от ticket payments. Approved tariffs, usage, invoices, collection attempts и Stripe Billing adapter проектируются как отдельный billing subsystem.
21. Post-event reports должны генерироваться автоматически и доставляться по ролям/отношениям. Если organizer и agent - одно и то же лицо или организация, отчетность не дублируется.
22. Scanner service - уже существующая отдельная система с приложением и `macs.arenasoldout.com` surface. Текущая синхронизация с Bil24 идет по webhooks; новая платформа должна сохранить webhook/event-driven sync и не встраивать scanner logic в core.
23. Scanner service должна поддерживать platform tickets, complimentary tickets, legacy imports, external-platform quota tickets и сторонние barcode batches.
24. External quota allocation must be modeled explicitly. Организаторы могут отдавать квоты сторонним платформам, а платформа должна показывать allocations, reported sales, returned quota, imported barcodes, scans, settlements и reconciliation exceptions в одном окне.
25. Unstructured external data ingestion может использовать дешевые/холодные AI normalization jobs или MCP/connector services, но результат не может без validation, confidence checks, review policy и audit менять inventory, settlement или scan decisions.
26. `platform_superadmin` и platform operations console обязательны для production operations. Cross-tenant доступ, logs/errors/load/health visibility, support access и break-glass actions должны быть permission-gated, MFA/step-up aware и неизменно аудируемы.
27. Bil24-style business roles must be explicit: `agent`, `organizer`, `platform_operator`, `external_ticketing_operator`, and `platform_superadmin`/`superoperator`. Do not collapse external ticketing operator into internal platform operator.
28. `02_CRITICAL_ARCHITECTURE_AUDIT.md`, `03_SPECIFICATION_STARTER.md`, `../08_architecture/09_domain_state_machines_ru.md`, `../08_architecture/10_compliance_security_privacy_ru.md`, `../08_architecture/11_architecture_decision_log_ru.md` и `../08_architecture/13_backend_go_initial_specification_ru.md` являются обязательными источниками перед master specification.
29. Master specification не может перейти в backlog, пока не определены lifecycle/state machines для reservation, order, payment, refund, ticket, credential, complimentary issuance, external allocation, scan decision, payout и invoice.
30. Instant/direct payouts are forbidden as an implicit default. Любой payout design должен явно закрыть KYB/KYC, refund/dispute exposure, reserve/hold policy, negative balance policy, settlement ledger, audit и provider fallback.
31. Payment checkout, organizer payout, service billing и external settlement - разные финансовые потоки. Их нельзя объединять в одну сущность или один provider-specific workflow.
32. Compliance, security, privacy, accessibility и API threat model - часть архитектуры, а не post-launch hardening. Master specification must reference `../08_architecture/10_compliance_security_privacy_ru.md`.
33. Ticket identity and ticket credential are separate concepts. Нельзя проектировать билет как только static QR/PDF, если модель не оставляет путь к Wallet pass, rotating QR, NFC, revocation, credential replacement и authority-based scanner validation.
34. First production profile must be explicit. AutoForge не должен молча выбирать сложную assigned-seat-first стратегию, instant payout strategy или fully automated AI ingestion без owner decision.
35. Backend core primary language is Go. AutoForge must not scaffold backend core in PHP, Node/TypeScript, Python, Java, .NET or Rust without a new explicit owner decision.
36. Go backend should start as a lightweight `net/http`-based modular monolith. Do not introduce a heavy framework, ORM-first domain model, or premature microservice split without decision record.

## Clarification-First Protocol

AutoForge должен работать в режиме уточнений до тех пор, пока ключевые неизвестные не закрыты.

Если вопрос влияет на одно из направлений ниже, агент обязан остановиться и спросить:

- core domain model
- database/storage architecture
- identity/auth/roles
- Bil24-style role mapping
- venue/seating ownership
- payment flow
- payout risk, reserves, disputes and settlement
- checkout flow
- ticket issuance
- ticket credentials, wallet/NFC/QR strategy and screenshot/offline policy
- complimentary invitation ticket issuance
- box office/POS flow
- external-ticketing-system mode
- refunds
- scanner integration
- scanner barcode authority federation
- external quota allocation
- external data ingestion and AI/MCP normalization
- platform superadmin access and observability
- notification/webhook event catalog
- social publishing workflow
- service billing and invoice flow
- Stripe Billing integration
- post-event reporting and recipient deduplication
- WordPress plugin contract
- external API contract
- compatibility API behavior
- deployment/HA/security
- compliance, privacy, accessibility, all-in pricing and API threat model
- migration of existing sites
- large venue performance and hot-sale mode

Разрешенные предположения:

- можно предложить default decision
- можно явно пометить его как `proposed default`
- нельзя выдавать proposed default за подтвержденное решение
- нельзя начинать реализацию спорной части без подтверждения

Формат вопросов:

```text
Question:
Decision needed:
Recommended default:
Impact if different:
Blocking level: blocking | can proceed with placeholder | later detail
```

## Specification Workflow

AutoForge должен пройти этапы:

1. Read guardrails.
2. Read critical architecture audit.
3. Read specification starter.
4. Read architecture decision log and separate `accepted`, `proposed`, and `needs_owner_decision`.
5. Read domain state machines.
6. Read compliance/security/privacy baseline.
7. Read initial Go backend specification.
8. Read architecture documents.
9. Read clarification register.
10. Ask grouped clarification questions.
11. Update assumptions/decisions in project docs.
12. Produce master platform specification in `../08_architecture/12_master_platform_specification_ru.md`.
13. Produce feature backlog seed.
14. Produce Definition of Done.
15. Only then start implementation planning.

Before producing implementation tickets, AutoForge must report:

- which P0 decisions are accepted;
- which proposed defaults are still unconfirmed;
- which blocking questions remain;
- which areas are intentionally out of the first production profile;
- which state machines and compliance requirements each backlog group depends on.

## Architecture Boundaries

### Platform Core

Core owns:

- organizations
- users, identities, memberships, roles, permissions
- countries, cities, venues
- seating plans and versions
- events and sessions
- inventory and availability
- reservations
- orders
- payments as normalized platform records
- issued tickets and barcodes
- external quota allocations
- external data ingestion records
- complimentary invitation ticket issuance
- refunds
- webhooks
- service billing
- post-event reports
- audit log
- observability and operational health records

Core must not depend on:

- WordPress
- WooCommerce
- AllPay plugin
- Bil24/TixGear API
- Java desktop applications
- a specific frontend framework

### External Applications

External applications include:

- WordPress plugin
- embeddable widget
- web admin app
- mobile app
- Telegram bot
- box office / POS interface
- external ticketing system
- developer/API console
- scanner service
- partner dashboards
- future professional tools

They consume Platform API. They do not own core business truth.

### Payment Layer

Payment providers are adapters behind a stable platform payment interface.

Provider-specific integrations may include:

- AllPay
- Stripe
- YooKassa
- PayPal
- local bank/acquiring providers

Core entities should model:

- payment intent
- capture
- refund
- settlement
- dispute
- provider webhook event
- ledger entry

Payouts must be modeled separately from checkout payments. Direct-to-organizer settlement can be implemented only after the risk controls from `../08_architecture/10_compliance_security_privacy_ru.md` and `../08_architecture/11_architecture_decision_log_ru.md` are accepted.

### Event Notifications And Webhooks

AutoForge must not design polling as the primary sync mechanism for connected clients. Polling may exist for recovery, but normal updates should use durable domain events, subscriptions, webhooks, and replay.

Required rules:

- domain events are persisted before delivery
- webhook payloads are signed and idempotent
- subscribers can filter by event types and resource ownership
- delivery failures are retried and observable
- manual replay exists for authorized users
- social publishing only consumes public/marketing-safe catalog events
- private order, payment, refund, ticket, scan, and personal data events must not be published to social channels

Required source: `../08_architecture/06_event_notifications_billing_reporting_ru.md`.

### Scanner Federation And External Allocations

AutoForge must treat scanner service as an independent validation system. It must not assume every valid barcode was issued by the platform core.

Required rules:

- current Bil24-to-scanner webhook contract must be captured before designing replacement events
- new platform scanner sync should stay webhook/event-driven
- scanner validation uses explicit ticket authorities and barcode namespaces
- platform tickets, complimentary tickets, legacy imports, external quota tickets, guest lists, and partner barcode batches are separate authorities/sources
- raw barcode collisions must be handled safely
- imported external barcode batches are immutable after activation except through explicit replacement/revocation batches
- scan events must return to the platform with authority/source metadata for reporting and reconciliation
- external quota allocations reduce platform-sellable inventory while allocated
- external sales/returns consume or release from allocated quota, not from native platform checkout orders
- organizers need one window for platform sales, POS, complimentary tickets, external allocations, reported external sales, scans, settlements, and exceptions
- unstructured external reports must go through ingestion, schema validation, confidence scoring, review policy, and audit

Required source: `../08_architecture/07_external_allocations_scanner_ingestion_ru.md`.

### Platform Superadmin And Observability

AutoForge must model `platform_superadmin` as a separate high-trust platform role, not as a normal organizer/admin extension.

Required rules:

- superadmin can see all organizations, organizers, agents, sales channels, events, orders, tickets, reports, billing, scanner sync, external allocations, integrations, logs, errors, load, health, jobs, queues, and audit
- cross-tenant views and actions are audited
- write/support actions are separate permissions from read-all access
- raw logs and sensitive data are masked by default
- sensitive log access, role changes, credential changes, support impersonation, and break-glass access require explicit permissions
- MFA and step-up authentication must be supported for high-risk actions
- support impersonation is scoped, temporary, audited, and preferably read-only by default

Required source: `../08_architecture/08_platform_superadmin_observability_ru.md`.

### Bil24-style Business Roles

AutoForge must preserve the core business role taxonomy:

- `agent`
- `organizer`
- `platform_operator`
- `external_ticketing_operator`
- `platform_superadmin` / `superoperator`

Rules:

- agent, organizer, operator, and superoperator are first-class business roles in the specification
- `external_ticketing_operator` is a third-party ticketing operator/system with own process/customer/payment/reporting flows
- `platform_operator` is an internal moderation/support/operations role
- `platform_superadmin`/`superoperator` is cross-tenant and observability-focused
- one user may hold several roles through memberships
- reports and notifications must deduplicate when one identity has multiple roles

Required source: `../08_architecture/03_platform_management_api_and_permissions_ru.md`.

### Service Billing And Stripe

Service billing is not ticket checkout. AutoForge must model billing accounts, approved tariff versions, usage records, invoices, invoice lines, collection attempts, credits, provider refs, and provider webhooks.

Stripe can be the first billing provider adapter, but internal tariff/usage/invoice state remains platform-owned.

Before implementation, AutoForge must confirm:

- first tariff model
- invoice approval vs automatic collection
- billing period
- Stripe account setup assumptions
- dunning and failed payment workflow

### Post-Event Reporting

AutoForge must design role-aware post-event reports before implementation tickets for reports are generated.

Required rules:

- reports have a configurable cutoff after event/session end
- report packages are built by relationship and permissions
- organizer/agent duplicates are merged when the same user or organization holds both roles
- delivery logs explain which role sections were included
- reports distinguish revenue, tickets, refunds, complimentary tickets, scans, no-shows, agent/channel attribution, and billing inputs

### WordPress Integration

Default target:

- no mandatory WooCommerce dependency
- custom post type for event pages
- local catalog cache
- live availability before reservation
- platform-hosted or platform-owned embedded checkout
- webhooks back to WordPress
- optional WooCommerce mode later as adapter

### Compliance, Security, Privacy And Market Readiness

AutoForge must treat compliance and security as first-class requirements.

Required rules:

- checkout totals must support legally required fee/tax disclosure and all-in price presentation
- PCI scope must stay minimized; card data must not touch platform servers unless a deliberate PCI decision is approved
- SCA/3DS outcomes must be represented in payment state
- consent, marketing pixels and analytics must not be treated as implicit checkout behavior
- accessibility acceptance criteria must be present for public checkout and admin interfaces
- API design must address OWASP API Top 10 classes, including object-level authorization, broken authentication, excessive data exposure, unrestricted resource consumption and unsafe third-party API consumption
- retention, deletion/export, log masking and sensitive support access must be specified before production launch

Required source: `../08_architecture/10_compliance_security_privacy_ru.md`.

### Domain State Machines

AutoForge must not infer lifecycle states ad hoc from UI screens or database fields.

Required rules:

- every implementation ticket that changes reservations, orders, payments, refunds, tickets, credentials, scans, external allocations, payouts or invoices must reference the relevant state machine
- terminal states must be explicit
- retries must be idempotent
- invalid transitions must be rejected and audited where relevant
- UI status labels may differ from internal states, but must map back to documented state machines

Required source: `../08_architecture/09_domain_state_machines_ru.md`.

### Compatibility API

Bil24-compatible API is useful for:

- migration safety
- third-party integrations
- rollback
- contract testing against existing behavior

It must not define internal service names or internal database model.

### Interface Taxonomy

AutoForge must preserve interface-family differences from the architecture docs. A POS/cash desk is not the same as public checkout, and an external ticketing system is not the same as a WordPress widget.

Before generating implementation tickets for an interface, AutoForge must identify:

- interface type
- acting user/organization/sales channel/service account
- payment ownership
- catalog ownership
- inventory allocation policy
- allowed issue modes
- delivery methods
- required audit events

Required source: `../08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`.

### Complimentary Invitation Tickets

Complimentary tickets are valid issued tickets without payment.

Required rules:

- organizer issuance must be simple when the role has the required permission
- no payment provider transaction is created for no-payment issuance
- assigned-seat invitations consume seats
- general-admission invitations consume capacity
- batch issuance must be idempotent
- issuance reason and audit trail are mandatory
- reports must separate revenue tickets from complimentary invitation tickets
- scanner validation must accept non-revoked complimentary tickets like other issued tickets

Before implementing this area, AutoForge must confirm organizer role permissions, quotas/limits, delivery methods, and revocation policy.

### Large Venue / Hot Sale Mode

Initial implementation may not include full stadium-scale optimization, but AutoForge must not design data models or APIs that block it later.

Required architectural direction:

- static seating geometry and dynamic seat statuses are separate concepts
- schema/layout payloads can be precomputed, versioned, compressed, and served from cache/CDN
- seat statuses can be cached per event/session and updated by point changes
- clients can request full status snapshots and later deltas by version
- reservation writes are atomic and idempotent
- high-demand events can enable waiting room, queues, rate limits, and bounded worker pools

Before implementing seating/inventory APIs, AutoForge must read `../08_architecture/04_large_venue_performance_strategy_ru.md`.

## Data And ID Rules

1. IDs must support very large scale.
2. New public APIs should treat IDs as strings.
3. Legacy numeric-looking IDs may be supported in compatibility layer.
4. Internal model should preserve separate IDs for event, session, venue, seat, ticket, order, sales channel, organization.
5. Never overload one ID to mean multiple domain concepts.
6. Imported legacy IDs must be stored as external references, not as primary architecture constraints.

## Security And Permissions

1. Use RBAC plus resource-aware ABAC.
2. API credentials must be scoped.
3. Service accounts and external apps must not get broad admin rights by default.
4. Every management API mutation must be audited.
5. Every webhook must be signed and idempotent.
6. Every payment callback must be verified and idempotent.
7. Secrets must not be stored in frontend-accessible configuration.

## Database And Scaling Guardrails

Current direction:

- use open-source components
- PostgreSQL is the preferred default for relational core plus JSONB where appropriate
- no paid object database dependency
- design for small initial load and later scale-out
- support hot standby / reserve strategy

Before implementation, AutoForge must ask for confirmation on:

- DB stack
- ID generation strategy
- multi-tenant boundaries
- deployment topology
- HA/failover model
- backup/PITR expectations

## Testing Guardrails

Every feature should define tests before being marked complete.

Required test categories where applicable:

- unit tests
- API contract tests
- permission tests
- idempotency tests
- migration/import tests
- external allocation reconciliation tests
- scanner barcode-authority tests
- AI/MCP ingestion validation tests
- platform superadmin permission tests
- Bil24-style role mapping tests
- observability/log masking tests
- support impersonation audit tests
- webhook retry tests
- payment callback tests
- billing invoice generation tests
- report recipient deduplication tests
- concurrency tests for inventory/reservations
- regression fixtures for existing Vino&Co behavior

## Documentation Guardrails

When AutoForge creates or changes architecture, it must update docs in the project.

Required records:

- decision made
- reason
- alternatives considered
- impact
- open questions
- affected features

No hidden architecture decisions inside code only.

## Stop Conditions

AutoForge must pause and ask before proceeding if:

- a requirement conflicts with existing architecture docs
- a feature requires choosing a payment provider
- a feature changes ownership or permission rules
- a feature collapses or renames agent/organizer/operator/external-ticketing-operator/superoperator semantics
- a feature changes public API contract
- a feature changes ticket/inventory semantics
- a feature changes complimentary invitation ticket semantics
- a feature changes POS/cash desk or external-ticketing-system ownership rules
- a feature changes scanner authority, external barcode import, or scan reconciliation behavior
- a feature changes external quota allocation, external sales import, or external settlement reconciliation
- a feature allows AI/MCP ingestion output to affect operational state without review/audit policy
- a feature changes platform_superadmin permissions, cross-tenant access, support impersonation, observability visibility, log masking, or break-glass behavior
- a feature changes notification/webhook semantics
- a feature changes billing, invoice, Stripe, tariff, or collection behavior
- a feature changes post-event report recipients or deduplication behavior
- a feature enables instant/direct payouts, changes reserves, settlement risk, disputes, chargebacks, payout timing, negative balances, or KYB/KYC assumptions
- a feature changes compliance posture, privacy data handling, accessibility requirements, all-in pricing, PCI scope, SCA/3DS, consent, tracking or API threat model
- a feature changes lifecycle/state machines or introduces a new domain state not documented in `../08_architecture/09_domain_state_machines_ru.md`
- a feature changes ticket credential strategy, barcode authority, Wallet/NFC/rotating QR path, revocation or offline validation assumptions
- a feature changes storage or deployment architecture
- a feature needs destructive migration of existing WordPress/Vino&Co data
- tests reveal uncertainty about expected business behavior

## First Output Expected From AutoForge

Before coding, AutoForge should produce:

1. List of understood confirmed decisions.
2. List of blocking clarification questions.
3. List of non-blocking assumptions.
4. Proposed master specification outline.
5. Proposed feature backlog groups.
6. P0 decision table from `../08_architecture/11_architecture_decision_log_ru.md`.
7. State-machine coverage map from `../08_architecture/09_domain_state_machines_ru.md`.
8. Compliance/security/privacy coverage map from `../08_architecture/10_compliance_security_privacy_ru.md`.

Only after human approval should it generate implementation tickets.
