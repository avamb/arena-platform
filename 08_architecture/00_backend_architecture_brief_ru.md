# Backend Architecture Brief

Updated: 2026-06-21

## Frame

We are designing a fully new production backend. Bil24/TixGear architecture is a reference for domain logic and ticketing workflows, but the new platform core must not depend on Bil24/TixGear API or Java desktop applications.

The platform should be production-grade from the beginning, with capabilities enabled gradually. The staged rollout must be a configuration and product-scope decision, not an architectural limitation.

## Core Principles

1. Backend core owns the platform domain model.
2. External applications interact through stable APIs, webhooks, SDKs, and event streams.
3. Payment providers are plugged through an independent payment layer, never directly into the platform core.
4. Authorization is provider-agnostic and does not require email confirmation as the only path.
5. Web interfaces are primary for operators, organizers, agents, and support staff.
6. Native/professional desktop or mobile apps may exist later, but the architecture must not require them.
7. Scanning is an independent service integrated by webhooks/events.
8. Operational data can be hot, warm, or archived depending on its lifecycle.
9. Owned WordPress sites should migrate through a new clean WordPress integration plugin rather than preserving the current WordPress/WooCommerce adapter as-is.
10. Existing Bil24-style integrations can still be supported through a compatibility API facade for partner continuity, low-risk rollback, and third-party clients that cannot migrate immediately.
11. The platform API must support full management workflows for authorized external applications, including creation and maintenance of countries, cities, venues, venue coordinates, venue characteristics, seating plans, and event/session configuration.
12. Venues are reusable platform resources. Seating plans are protected resources with explicit ownership, visibility, versioning, and fork/copy rules so that one organizer cannot modify another organizer's seating plan.
13. The architecture must preserve a path to future large-venue and high-demand sale mode. Initial launch may not need stadium-scale throughput, but data, API, inventory, checkout, and cache decisions must not prevent precomputed schemas, dynamic seat-status cache, CDN delivery, and hot-sale backpressure later.
14. Interface type is a first-class architecture dimension. Public checkout, agent sales, box office/POS, external ticketing systems, backoffice clients, scanner services, and developer/API tooling have different ownership, payment, identity, and audit requirements.
15. Complimentary invitation tickets are a first-class no-payment ticket issuance path. They must be easy for authorized organizers to issue, but still consume inventory, issue normal tickets/barcodes, and record permissions, reason, delivery, and audit data.
16. Platform integrations should be event-driven. Catalog changes, inventory changes, order/payment/refund changes, ticket changes, scan events, billing events, and report events should publish durable notifications so clients do not need constant polling.
17. Platform service billing is separate from customer ticket payments. Approved tariffs, usage records, invoices, collection attempts, and Stripe Billing integration must be modeled as a billing subsystem with provider adapters.
18. Post-event reports must be generated automatically and delivered by recipient role and relationship. Reporting delivery must deduplicate cases where the same person or organization is both organizer and agent.
19. Scanner service is an existing independent ticket validation boundary with an app and `macs.arenasoldout.com` event/scanning surface. It currently syncs with Bil24 through webhooks; the new platform must preserve this event-driven webhook synchronization style.
20. Scanner validation must support platform tickets, complimentary tickets, legacy imports, external-platform quota tickets, and other third-party barcode lists without assuming all barcodes were sold by the platform.
21. External quota allocation to third-party platforms must be supported. Even if this is an older market practice, organizers need one platform view of native sales, external allocations, external reported sales, imported barcodes, scans, settlements, and reconciliation exceptions.
22. External data ingestion may use structured APIs, webhooks, files, emails, PDFs, screenshots, manual entry, low-cost asynchronous AI normalization, or MCP/connector services. AI/MCP output must pass schema validation, confidence checks, review policy, and audit before affecting reports or operational state.
23. A `platform_superadmin` role and platform operations console are required. Superadmins need one window for all organizers, agents, entities, logs, errors, load, health, jobs, webhooks, billing, scanner sync, external allocations, and audit, with strong security and immutable audit.
24. Bil24-style business roles must be explicit in the new model: agent, organizer, operator/platform operator, external ticketing operator, and superoperator/platform superadmin. These are business/permission concepts, not just UI labels.
25. Domain state machines are required before backlog generation for reservations, orders, payments, refunds, tickets, credentials, scanner decisions, external allocations, payouts and invoices.
26. Compliance, security, privacy, accessibility and API threat model are architecture inputs, not later hardening tasks.
27. Ticket identity and ticket credential must remain separate so static QR/PDF, Wallet, rotating QR, NFC and external barcode credentials can evolve without changing ticket ownership.
28. First production profile must be explicit. The proposed default is GA-first, scanner/import-first, assigned seating modeled but not full-editor P0, and controlled payouts rather than instant-by-default payouts.
29. Backend core primary language is Go. The initial backend shape is a lightweight `net/http`-based modular monolith with explicit domain/application/adapters boundaries.

## Confirmed Architectural Decisions

### Payment Layer

Payment integrations must be isolated behind a provider abstraction:

- `PaymentProvider`
- `PaymentIntent`
- `PaymentCapture`
- `Refund`
- `Payout`
- `Settlement`
- `Dispute`
- `WebhookEvent`

The core should not know Stripe, YooKassa, AllPay, PayPal, or bank-specific APIs directly. Providers publish normalized payment events into the platform event bus.

Checkout payment, service billing, external settlement and organizer payout are separate financial flows. Payout policy must include KYB/KYC, reserves/holds, refund/dispute exposure, negative balance handling and ledger audit before direct or instant payouts are enabled.

### Notification And Event Backbone Boundary

The platform must expose durable notifications and webhooks for integrations instead of requiring connected clients to poll the API constantly.

Required architectural properties:

- domain changes are written to an outbox with the transaction that produced them
- webhook deliveries are signed, idempotent, retried, and observable
- subscribers can filter by event type, organization, sales channel, and event/session where relevant
- payloads can be lightweight notifications; clients fetch full resource state by ID/version when needed
- catalog, inventory, order, payment, refund, ticket, scan, complimentary ticket, billing, and report events all use the same event backbone
- social publishing workflows consume approved catalog/marketing events, not private order/payment/refund data

See `06_event_notifications_billing_reporting_ru.md`.

### Billing Boundary

The platform needs a billing subsystem for service fees charged to clients, organizers, agents, or partners. This is not the same as ticket checkout payment processing.

Required architectural properties:

- approved tariffs are versioned
- usage records are auditable and reproducible
- invoices are generated from platform billing data
- Stripe Billing can be the first provider adapter for sending invoices and collecting service fees
- Stripe IDs are stored as provider references, not internal primary keys
- billing provider webhooks are verified and idempotent
- failed invoice collection triggers notification and dunning workflow

See `06_event_notifications_billing_reporting_ru.md`.

### Identity And Authorization

Auth must support multiple identity sources:

- email without mandatory confirmation where business rules allow it
- magic link / email verification as optional policy
- phone
- Telegram
- WhatsApp or other messenger identifiers where available
- OAuth/social login
- external partner identity
- anonymous checkout identity promoted later into a full account

The core identity model should separate:

- `User`
- `Identity`
- `AuthSession`
- `Account`
- `OrganizationMembership`
- `Role`
- `Permission`

The role taxonomy must explicitly cover:

- `agent`
- `organizer`
- `platform_operator`
- `external_ticketing_operator`
- `platform_superadmin` / `superoperator`

See `03_platform_management_api_and_permissions_ru.md`.

### Application Boundary

Primary clients:

- public event discovery and checkout web app
- organizer web console
- agent web console
- box office / POS cashier interface
- operator/admin web console
- platform superadmin / operations console
- support/reporting web console
- scanner service UI/app
- external ticketing system integrations
- developer/API console tooling
- future native apps and professional tools

The backend must expose APIs that are ergonomic for native/mobile apps, not only browser flows.

### Interface Taxonomy Boundary

The platform must explicitly distinguish interface families instead of treating every client as the same checkout frontend:

- `platform_public_checkout`: hosted checkout, embedded checkout, WordPress plugin, widgets, custom sites, Telegram Mini Apps, and future customer apps.
- `agent_sales_interface`: browser/mobile agent sales tools where the platform owns inventory, order, payment state, and ticket issuance.
- `box_office_pos`: in-person cashier/POS interfaces with shifts, cash or terminal payments, printing/fiscal needs, and anonymous buyers.
- `external_ticketing_system`: third-party ticketing systems with their own frontend, customer base, checkout, and sometimes their own acquiring.
- `organizer_backoffice`: organizer event, inventory, order, reporting, and complimentary ticket workflows.
- `operator_backoffice`: platform-level moderation, verification, support, payments, and cross-organization administration.
- `management_api_client`: scoped API clients, partner dashboards, automations, bots, and future professional tools.
- `scanner_service`: ticket validation and scan event flow.
- `developer_api_console`: sandbox and diagnostic interface for API/compatibility contracts.

Every externally exposed interface should map to a sales channel, service account, user session, or application credential with explicit scopes. Payment ownership, catalog ownership, inventory allocation, issue modes, delivery methods, and settlement policy must be explicit per channel where relevant.

See `05_interface_taxonomy_and_complimentary_tickets_ru.md`.

### Platform Management API Boundary

Authorized external applications must be able to manage platform resources through stable APIs, not only through the operator web console.

Required management surfaces:

- geography: countries, regions where needed, cities, time zones
- venues: canonical address, coordinates, localized names, characteristics, media, accessibility fields
- seating plans: sections, rows, seats, standing zones, tables, capacities, SVG/geometry, versions
- events and sessions: event metadata, venue assignment, seating plan assignment, sales settings

Authorization must combine role permissions with resource ownership. Organizers may create or reuse venues when their organization has the required permission, but they must not edit seating plans owned by another organization unless the plan is explicitly shared and editable for them. The default operation for modifying someone else's plan is fork/copy, not in-place edit.

### API Compatibility Boundary

The platform may expose a Bil24-compatible command API as a separate gateway layer. This gateway preserves the old request/response contract for existing integrations that need drop-in migration, while translating commands into the new backend domain model.

The compatibility gateway is not the core API and must not leak legacy constraints into the internal model. It is an adapter for migration, partner continuity, low-risk rollback, and third-party clients. For owned WordPress sites such as Vino&Co, the preferred path is a new platform-native WordPress plugin.

Required compatibility elements:

- JSON POST endpoint.
- Request envelope with `command`, `fid`, `token`, `locale`.
- Bil24-style `resultCode`, `description`, `command` response envelope.
- Command names used by current integrations.
- Legacy ID field names and response shapes where existing clients depend on them.
- Bil24-like session/cart/order semantics where required for drop-in migration.
- Contract tests based on captured WordPress/Vino&Co request/response fixtures.

### Scanner Boundary

Scanning remains a separate existing service. It has an application and `macs.arenasoldout.com` event/scanning surface. The current Bil24 synchronization is webhook-driven, and the new platform should keep scanner synchronization webhook/event-driven.

The platform should publish scanner-relevant events through signed webhook notifications so the scanner does not need to poll continuously:

- event/session created or updated
- ticket issued/revoked/refunded
- complimentary ticket issued/revoked
- external barcode batch approved/revoked
- scan authority or validation policy updated

The scanner validates barcodes against a ticket authority, not only against the platform's own ticket table. Supported authorities can include platform-issued tickets, complimentary tickets, imported legacy tickets, external-platform quota tickets, manually imported guest lists, and partner-provided barcode batches.

Suggested lifecycle:

- hot scan state during event and short post-event window
- immutable scan log for audit
- aggregated analytics retained long term
- raw scan events archived according to retention policy

Required architectural properties:

- barcode namespaces are explicit because raw barcode strings may collide across authorities
- external barcode batches can be imported for event-day validation
- scanner decisions identify the authority/source used
- ambiguous barcode matches require safe rejection or supervisor review
- scan events flow back into platform reports and reconciliation
- existing scanner webhook payloads and idempotency behavior must be captured before replacing Bil24 sync

See `07_external_allocations_scanner_ingestion_ru.md`.

### Platform Superadmin And Observability Boundary

The platform needs a dedicated superadmin/operations console for trusted platform owners/operators. It must provide cross-tenant visibility and operational control without hiding security and audit requirements.

Required architectural properties:

- `platform_superadmin` is distinct from organizer admin and normal platform operator
- superadmin can see all organizations, organizers, agents, users, sales channels, events, orders, tickets, billing, scanner sync, external allocations, reports, and integration health
- load, errors, logs, metrics, traces, queues, workers, webhooks, background jobs, and deployments are visible in a clear admin panel
- cross-organization access and support actions are audited with actor, target, reason/context, IP/session, request ID, and timestamp
- sensitive actions require MFA/step-up policy where configured
- support impersonation should be scoped, temporary, audited, and preferably read-only unless elevated
- raw logs and sensitive data are masked by default and permission-gated

See `08_platform_superadmin_observability_ru.md`.

### External Allocation And Data Ingestion Boundary

Organizers may allocate part of event/session capacity to external ticketing platforms. The platform should reserve or block that quota, import external reports and barcodes where available, and show the organizer one combined operational view.

Required architectural properties:

- external allocations are not native platform checkout orders
- allocated quota reduces platform-sellable inventory
- external sales consume from allocated quota
- unsold quota can be returned/released according to event policy
- external sales, returns, settlements, and barcodes can be imported from structured or unstructured sources
- external data quality is tracked as reported, estimated, confirmed, or disputed
- reconciliation exceptions are visible to organizer/operator roles
- low-cost/cold AI normalization and MCP/connector services may assist ingestion but cannot silently mutate inventory, settlement, or scan decisions without validation and audit

See `07_external_allocations_scanner_ingestion_ru.md`.

### Large Venue Performance Boundary

The first production stages may target modest loads, but the platform must keep a clear path to large venue and high-demand sale behavior.

Required architectural properties:

- separate static seating geometry from dynamic seat status
- support precomputed and pre-compressed schema/layout assets
- support dynamic per-session seat-status cache with point updates
- keep reservation/order writes atomic, short, idempotent, and auditable
- allow event/session-specific hot-sale mode, queueing, rate limits, and backpressure
- keep external rendering clients able to load schema once and then refresh status snapshots or deltas

See `04_large_venue_performance_strategy_ru.md`.

### Complimentary Invitation Ticket Boundary

The platform must support complimentary invitation tickets as a no-payment issuance path, separate from normal paid checkout. This flow can be exposed through organizer backoffice, box office/POS, operator backoffice, and explicitly scoped management API clients.

Required architectural properties:

- organizer roles can issue invitation tickets for their own events when granted the required permission
- issued invitation tickets produce normal platform tickets and barcodes
- assigned-seat invitations consume selected seats; general-admission invitations consume capacity
- no payment provider transaction is created for the no-payment path
- issuance reason, recipient/delivery data where collected, issuing user, organization, interface type, sales channel/app credential, idempotency key, and audit trail are recorded
- revocation/cancellation rules are explicit and can release inventory only when the event policy allows it
- reports separate paid sales, complimentary invitations, replacements, cancelled/revoked tickets, scanned tickets, and revenue

Recommended initial permissions:

- `complimentary_ticket.issue.own_event`
- `complimentary_ticket.issue.batch`
- `complimentary_ticket.revoke.own_event`
- `complimentary_ticket.view_report.own_event`

See `05_interface_taxonomy_and_complimentary_tickets_ru.md`.

### Post-Event Reporting Boundary

The platform must generate reports automatically after an event/session reaches a configurable reporting cutoff. Reports should be delivered to organizers and agents according to role, organization, sales channel, and explicit subscriptions.

Required architectural properties:

- report jobs are triggered from event/session lifecycle and event backbone signals
- reports distinguish paid tickets, complimentary invitations, refunds, cancellations, scans, no-shows, channel sales, revenue, fees, and billing inputs
- delivery is deduplicated when the same person or organization is both organizer and agent
- report packages can include multiple role sections for the same recipient
- reports are available in the console and can notify recipients by email or webhook

See `06_event_notifications_billing_reporting_ru.md`.

### Legacy Materials

Bil24/TixGear materials should be used for:

- role and workflow analysis
- ticketing domain terminology
- edge cases in reservations, orders, refunds, quotas, and reports
- UI coverage of existing professional tools
- examples of what to improve

They should not define the new backend's technical coupling.

## Next Discussion Topics

1. Domain model boundaries: platform, organization, event, session, inventory, ticket, order.
2. Payment provider architecture and settlement ledger.
3. Identity/auth architecture for email, messenger, social, anonymous, and organization users.
4. Event-driven integration: webhooks, outbox, inbox, retries, idempotency.
5. Scanner service contract and data retention.
6. Web backoffice modules and permission model.
7. Bil24-compatible API facade command coverage and migration test plan.
8. WordPress integration plugin contract, checkout model, sync model, and migration plan for owned sites.
9. Platform management API, venue deduplication, seating plan ownership, and organizer/operator permission rules.
10. Large venue performance test profile, cache design, and hot-sale operational workflow.
11. Box office/POS workflow: shifts, cash/terminal payments, printing/fiscal data, and refunds.
12. External-ticketing-system integration mode: catalog/payment ownership, settlement, inventory allocation, and API scopes.
13. Complimentary invitation ticket workflow: organizer permissions, quotas, delivery, reporting, and revocation policy.
14. Notification/webhook event catalog, subscription filtering, retries, replay, and social publishing policy.
15. Platform service billing: tariff model, invoice lifecycle, Stripe Billing adapter, and dunning workflow.
16. Post-event reporting: report cutoff, report packages, delivery recipients, and deduplication rules.
17. Scanner federation: barcode authorities, external barcode imports, collision handling, offline cache, and scan reconciliation.
18. External quota allocation: allocation lifecycle, external sales imports, quota returns, settlement, and organizer one-window view.
19. External data ingestion: files/emails/PDFs/screenshots/API/MCP connectors, AI normalization, confidence, review, and audit.
20. Platform superadmin console: cross-tenant access, observability, logs, errors, load, health, alerts, support access, and audit.
21. Bil24-style role mapping: agent, organizer, operator/platform operator, external ticketing operator, and superoperator/platform superadmin.
22. Domain state machines: reservation, checkout/order/payment/refund, ticket credential, scanner decision, external allocation, payout and invoice lifecycle.
23. Compliance/security/privacy: all-in pricing, PCI scope, SCA/3DS, accessibility, data retention, consent, API security and abuse controls.
24. First production profile: GA-first scanner/import-first launch versus assigned-seat-first launch.
25. Payout risk policy: delayed payout, reserve, tranche, direct payout, dispute exposure and KYB/KYC gate.
