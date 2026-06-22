# Interface Taxonomy And Complimentary Tickets

Updated: 2026-06-21

## Decision

The platform architecture must treat interface type as a first-class product and API dimension. Bil24/TixGear distinguishes several interface families, and the new platform should preserve the useful business separation without copying Bil24 as the internal core.

The platform must explicitly support:

- public checkout interfaces
- agent sales interfaces
- box office / POS interfaces
- external ticketing systems
- organizer and operator backoffice interfaces
- management API clients
- scanner service interfaces
- developer/API console tooling

Complimentary invitation tickets are also a first-class ticket issuance path. They are issued without payment and must be easy for an authorized organizer role to create, while still respecting inventory, permissions, audit log, delivery, cancellation, and scan rules.

## Interface Families

## Role Mapping

Interface family is not the same thing as business role. The platform must support Bil24-style roles explicitly:

- `agent`: uses agent sales, POS, partner, or other permitted sales interfaces.
- `organizer`: uses organizer backoffice and management APIs for own events, quotas, reports, complimentary tickets, and external allocations.
- `platform_operator`: uses operator backoffice for platform moderation, verification, support, and exceptions.
- `external_ticketing_operator`: uses external-ticketing-system integrations and may have own processing, own customers, allocated quota, external barcode batches, and settlement reports.
- `platform_superadmin` / `superoperator`: uses platform operations console with cross-tenant visibility and observability.

The same person can hold multiple memberships/roles. Reporting, notifications, and permissions should resolve by user, organization, role, sales channel, and relationship rather than by a single global role string.

### `platform_public_checkout`

Public customer-facing sales surfaces.

Examples:

- platform-hosted checkout
- embedded checkout
- WordPress plugin
- embeddable widget
- custom event sites
- Telegram Mini Apps
- future customer mobile apps

Primary properties:

- platform owns reservation, order, payment state, ticket issuance, and financial totals
- customer identity can be anonymous or lightweight
- payment goes through the platform payment layer
- browser retries and double-clicks must be idempotent

### `agent_sales_interface`

Sales interfaces used by agents or partner sellers where the platform remains the source of truth.

Examples:

- browser agent console
- Android agent app
- iOS agent app
- partner seller dashboard

Primary properties:

- agent acts under an organization, sales channel, or partner account
- platform owns inventory, reservation, order, payment state, and ticket issuance
- commission, settlement, and reporting must be attributed to the agent/sales channel
- buyer identity may be anonymous, collected by the agent, or promoted into a platform account later

### `box_office_pos`

In-person cashier and POS interfaces. This is not just a web checkout skin.

Examples:

- venue box office
- event-day cashier terminal
- mobile cashier tablet
- "new cash desk" style interface

Primary properties:

- cashier works inside a shift/session
- sales may use cash, external card terminal, local acquiring, bank transfer confirmation, or complimentary issuance
- no internet acquiring is required for the buyer flow
- buyer may remain anonymous and should not be forced into a platform account
- ticket printing, fiscal data, receipt numbers, terminal references, and cash drawer totals may be needed
- every sale, cancellation, refund, reprint, and complimentary issue must be audited
- degraded/offline mode can be considered later, but the first architecture must not make it impossible

### `external_ticketing_system`

Third-party ticketing systems or operators that have their own frontend, customer database, checkout, and sometimes their own acquiring.

Primary properties:

- integration mode must be declared per sales channel or partner app
- the platform may expose availability/reservation/ticket issuance APIs without owning the external checkout UI
- the platform may allocate quota to an external platform and later ingest sales/returns/barcodes/settlement reports from that platform
- payment ownership must be explicit: platform payment layer, external payment confirmation, or settlement-only reporting
- catalog ownership must be explicit: platform catalog, mirrored external catalog, or external catalog with platform inventory allocation
- external reports may be structured or unstructured and must go through ingestion, validation, reconciliation, and audit
- contracts need strict scopes, quotas, idempotency keys, webhook signatures, and audit trail
- Bil24-compatible API can serve migration/legacy clients, but external-ticketing-system mode is broader than Bil24 command compatibility

### `organizer_backoffice`

Organizer-facing management interface.

Primary properties:

- event/session creation
- venue and seating plan usage within permissions
- inventory and tariff management
- sales channel configuration
- order support actions
- complimentary invitation ticket issuance
- reporting

### `operator_backoffice`

Platform operator/admin interface.

Primary properties:

- global moderation and verification
- organization and role management
- venue merge/verification
- seating plan verification
- payment provider administration
- risk/support operations
- cross-organization audit and reporting

### `management_api_client`

API clients that manage platform resources on behalf of authorized users, organizations, or service accounts.

Examples:

- partner dashboards
- automation connectors
- no-code tools
- Telegram bots
- future professional tools

Primary properties:

- same permission model as first-party backoffice
- scoped credentials only
- no broad admin token by default
- all mutating operations require idempotency and audit

### `scanner_service`

Ticket validation and event-day scan interfaces.

Primary properties:

- can remain a separate service
- receives platform-issued ticket data, external barcode batches, guest lists, and authority configuration from the platform
- publishes scan events back to the platform
- must handle normal paid tickets, complimentary invitation tickets, imported legacy tickets, and external-platform tickets consistently
- validates against explicit ticket authorities/barcode namespaces, not only against platform orders

### `developer_api_console`

Sandbox and diagnostic interface for API contracts.

Primary properties:

- lets developers test commands/endpoints with scoped sandbox credentials
- supports compatibility-gateway command testing where needed
- must not bypass production permissions
- production usage should be read-only or heavily restricted unless explicitly enabled

## Sales Channel Model

Every externally exposed interface should map to a `SalesChannel` or equivalent resource.

Recommended fields:

- `organization_id`
- `interface_type`
- `channel_kind`
- `environment`
- `credential_scope`
- `payment_ownership`
- `catalog_ownership`
- `inventory_allocation_policy`
- `external_quota_policy`
- `commission_policy`
- `settlement_policy`
- `external_data_ingestion_policy`
- `allowed_issue_modes`
- `allowed_payment_methods`
- `allowed_delivery_methods`

Suggested `interface_type` values:

```text
platform_public_checkout
agent_sales_interface
box_office_pos
external_ticketing_system
organizer_backoffice
operator_backoffice
management_api_client
scanner_service
developer_api_console
```

Suggested `payment_ownership` values:

```text
platform_payment_layer
external_confirmed_payment
cash_or_terminal_at_pos
no_payment_complimentary
settlement_only
```

## Complimentary Invitation Tickets

### Decision

Complimentary invitation tickets must be modeled as a separate no-payment issuance path, not as a normal paid checkout that happens to have a zero total.

They may be issued from:

- organizer backoffice
- box office / POS
- management API client with explicit permission
- operator backoffice

The organizer workflow must be simple: choose event/session, choose seats or quantity, enter recipient/delivery details where needed, choose reason/campaign, issue tickets.

### Required Domain Concepts

Recommended entities or fields:

- `TicketIssueMode`: `paid`, `complimentary`, `replacement`, `manual_adjustment`
- `ComplimentaryIssuance`
- `ComplimentaryIssuanceItem`
- `ComplimentaryReason`
- `IssuedByUser`
- `IssuedForOrganization`
- `Recipient`
- `DeliveryStatus`
- `AuditLogEntry`

Complimentary tickets still produce normal issued tickets and barcodes. Scanner service should validate them like any other issued ticket, while reports can distinguish issue mode.

### Inventory Rules

Complimentary tickets must consume inventory unless explicitly configured as non-seat credentials such as staff passes.

Rules:

- assigned-seat invitation consumes the selected seat immediately
- general-admission invitation consumes capacity immediately
- cancellation/revocation can release inventory if the event policy allows it
- complimentary issuance must be blocked when capacity is unavailable, unless the user has an explicit override permission
- reports must separate paid sold count, complimentary issued count, held/reserved count, cancelled count, and scanned count

### Permission Rules

Recommended permissions:

```text
complimentary_ticket.issue.own_event
complimentary_ticket.issue.any_event
complimentary_ticket.issue.assigned_seat
complimentary_ticket.issue.general_admission
complimentary_ticket.issue.batch
complimentary_ticket.override_capacity
complimentary_ticket.revoke.own_event
complimentary_ticket.revoke.any_event
complimentary_ticket.view_report.own_event
```

Recommended default:

- organizer owner/admin can issue complimentary tickets for their own organization's events
- event manager can issue if the role has `complimentary_ticket.issue.own_event`
- operator can issue/revoke across organizations only with explicit operator permission
- external API credentials cannot issue complimentary tickets unless the scope is explicitly granted

### Audit Requirements

Every complimentary issuance must record:

- event/session
- seats or quantity
- issuing user
- organization
- interface type
- sales channel or app credential
- recipient details where collected
- reason/campaign
- request id
- idempotency key
- timestamp
- delivery method
- old/new ticket status for revocations

### API Direction

Recommended endpoints:

```text
POST /v1/events/{event_id}/complimentary-issuances
GET  /v1/events/{event_id}/complimentary-issuances
GET  /v1/complimentary-issuances/{issuance_id}
POST /v1/complimentary-issuances/{issuance_id}/revoke
```

The endpoint should be idempotent and return issued ticket references, delivery state, and inventory impact.

## Reporting Requirements

Reports must distinguish:

- paid tickets
- complimentary invitation tickets
- replacement tickets
- cancelled/revoked tickets
- scanned tickets
- no-shows
- cash/POS sales
- external-ticketing-system sales
- platform checkout sales

Financial reports must not count complimentary tickets as revenue. Attendance reports should count them as valid issued tickets unless revoked.

## Open Questions

1. Should organizer complimentary issuance have per-event quotas by default?
2. Should some organizer roles require approval before batch invitation issuance?
3. Which delivery methods are required first: email, PDF download, manual print, SMS/WhatsApp, or all later?
4. Do invitation tickets need custom visible labels such as "Invitation" or should this remain internal metadata?
5. Should POS support cash drawer/shift accounting in the first production release or only reserve the architecture?
