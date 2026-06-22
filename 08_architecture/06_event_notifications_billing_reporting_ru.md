# Event Notifications, Billing, And Reporting

Updated: 2026-06-21

## Decision

The platform must use an event-driven notification system so connected sites, apps, agents, organizers, scanner services, and partner systems do not need to poll the API constantly. Domain changes should publish durable platform events, and subscribers should receive signed webhooks or queued notifications.

The same event backbone should support:

- catalog/event/session/inventory updates
- order, payment, refund, ticket, and scan lifecycle notifications
- WordPress and widget cache invalidation
- partner and external-ticketing-system synchronization
- external allocation, external report ingestion, and reconciliation workflows
- social publishing workflows for public event promotion
- automatic billing usage capture
- automatic post-event reports

Billing for platform service fees is separate from ticket payments. The platform needs its own billing subsystem for approved tariffs, invoices, usage, customer billing accounts, and provider integration. Stripe can be the first billing provider for issuing invoices and collecting service fees, but Stripe must remain an adapter, not the internal billing source of truth.

Post-event reports must be generated automatically and delivered to organizers and agents according to role and relationship. If the same person or organization is both organizer and agent, reporting must be deduplicated.

## Event Backbone And Webhooks

### Goals

- minimize unnecessary API polling
- provide timely updates for connected clients
- make integrations reliable through retries and idempotency
- support social, billing, reporting, and audit workflows from the same domain events
- avoid making webhooks the only source of truth

### Core Pattern

Recommended pattern:

```text
Domain transaction
  -> Outbox event committed with transaction
  -> Event dispatcher publishes to internal subscribers
  -> Webhook delivery jobs fan out to external subscribers
  -> External client stores event id and fetches full resource if needed
```

Webhook payloads should be notifications with enough context to route work, not necessarily full resource snapshots. For large or sensitive resources, the receiver should fetch the current resource by ID and version.

### Required Entities

- `DomainEvent`
- `OutboxEvent`
- `WebhookSubscription`
- `WebhookDelivery`
- `WebhookEndpoint`
- `WebhookSecret`
- `NotificationPreference`
- `EventCursor`
- `DeadLetterEvent`

### Required Delivery Properties

- signed payloads
- timestamp and replay-window validation
- event ID idempotency
- delivery retry policy
- dead-letter queue after repeated failures
- per-subscriber rate limits
- event type allowlist
- organization/sales channel/event filters
- delivery logs visible to authorized operators and integration owners
- manual replay for authorized users

### Event Families

Required event families:

```text
catalog.event.*
catalog.session.*
catalog.venue.*
catalog.seating_plan.*
inventory.*
reservation.*
order.*
payment.*
refund.*
ticket.*
scan.*
complimentary_ticket.*
billing.*
report.*
integration.*
external_allocation.*
external_report.*
external_ticket_import.*
external_reconciliation.*
scanner_authority.*
```

Important examples:

```text
catalog.event.created
catalog.event.updated
catalog.session.updated
inventory.changed
reservation.expired
order.created
order.paid
order.cancelled
refund.requested
refund.approved
refund.succeeded
refund.failed
ticket.issued
ticket.revoked
scan.recorded
complimentary_ticket.issued
billing.invoice.created
billing.invoice.paid
report.event.generated
external_allocation.created
external_report.normalized
external_reconciliation.exception_created
scanner_authority.updated
```

## Existing Scanner Webhook Subscriber

The scanner system already exists and currently synchronizes with Bil24 through webhooks. The new platform should provide equivalent signed webhook notifications for scanner synchronization.

Scanner-relevant webhook events should include:

- event/session changes
- ticket issued/revoked/refunded
- complimentary ticket issued/revoked
- external barcode batch approved/revoked
- scan authority or validation policy changes

The scanner remains an external subscriber and event publisher. It should receive platform notifications and publish scan results back to the platform event backbone.

## Social Publishing

Social publishing should consume approved catalog and marketing events from the event backbone, not scrape public pages or poll the platform.

### Supported Concept

The platform should support a `SocialPublishingJob` model:

- event/session source
- target social channel
- locale
- generated post text
- image/poster asset
- event URL
- UTM/tracking parameters
- approval status
- scheduled publish time
- publish result
- external post ID

### Rules

- only public/marketing-safe catalog events can trigger social publishing
- order, payment, refund, ticket, scan, and personal data events must not be published to social channels
- organizers can configure which events are eligible for social publishing
- publication can be automatic only when the channel policy allows it; otherwise it should require approval
- social posts must be idempotent per event/session/channel/locale/campaign
- updates should avoid duplicate posts unless explicitly configured as a new campaign

### Initial Channels

Recommended default:

- Telegram channel/group publishing first if operationally useful
- Facebook/Instagram or other networks later through adapters
- generic webhook/export adapter for custom social automation

## Billing System

### Boundary

Billing is about platform service fees charged to clients/organizers/agents. It is not the same as customer ticket payments.

Ticket payments belong to the payment layer. Service billing belongs to the billing subsystem and can use Stripe Billing or another provider as an adapter.

### Required Concepts

- `BillingAccount`
- `BillingCustomer`
- `BillingAgreement`
- `TariffPlan`
- `TariffVersion`
- `BillingMetric`
- `UsageRecord`
- `BillingPeriod`
- `Invoice`
- `InvoiceLine`
- `CreditNote`
- `CollectionAttempt`
- `BillingProvider`
- `BillingProviderCustomerRef`
- `BillingProviderInvoiceRef`
- `BillingProviderPaymentMethodRef`

### Tariffs

Approved tariffs must be versioned. Once an invoice period starts, generated invoice lines should reference the tariff version used for calculation.

Possible tariff components:

- monthly fixed fee
- per event/session fee
- per paid ticket fee
- percentage of ticket revenue
- per complimentary ticket fee if business policy requires it
- payment provider pass-through fee
- agent/partner fee
- custom contracted fee

The billing engine must support draft review before automatic collection where the customer contract requires it.

### Stripe Billing Adapter

Stripe can be the first billing provider for:

- creating/updating billing customers
- creating invoice items/invoices from platform-calculated lines
- sending invoices
- collecting payment from saved payment methods
- receiving invoice/payment webhooks
- recording invoice paid/failed/voided/uncollectible events

Rules:

- platform internal billing records remain the source of truth for tariff, usage, invoice intent, and customer account ownership
- Stripe IDs are stored as provider references
- Stripe webhooks must be verified and idempotent
- invoice line calculation should be deterministic and reproducible from platform data
- failed collection must trigger notification and dunning workflow, not silent state drift
- credits/adjustments must be explicit `CreditNote` or adjustment lines, not manual hidden edits

## Post-Event Reports

### Goal

After an event/session ends, the platform should generate automatic reports and send them to organizers and agents according to permissions and relationship to the event.

### Trigger

Recommended trigger:

```text
event/session end
  -> scan window closes or configured delay passes
  -> payment/refund data reaches reportable cutoff
  -> report generation job starts
  -> role-based report packages are delivered
```

The cutoff should be configurable because refunds, chargebacks, late scans, and settlement data can arrive after event end.

### Report Types

Recommended report families:

- sales summary
- revenue and fees
- ticket category/tariff breakdown
- complimentary invitation tickets
- refunds/cancellations
- agent/channel performance
- external allocation and external platform sales
- scan/attendance/no-show summary
- seating/inventory utilization
- settlement/billing inputs
- exception report

### Recipient And Deduplication Rules

Reports must be delivered by relationship, not by naive email list.

Recipient resolution should consider:

- user ID
- organization ID
- role
- agent relationship
- organizer relationship
- sales channel
- explicit report subscription
- email/contact address

Deduplication rule:

- if the same user is both organizer and agent, send one report package with both role sections
- if the same organization is both organizer and agent, send one organization report package with combined sections
- if multiple roles map to the same email but different verified users, avoid automatic merge unless identity mapping confirms they are the same recipient
- delivery logs must show which role sections were included and why

### Delivery Methods

Recommended initial delivery:

- downloadable report in organizer/agent console
- email notification with secure link
- optional PDF/CSV exports

Later:

- scheduled email attachments
- webhook report delivery
- Google Sheets/export connectors

### External Data In Reports

Reports must distinguish native platform data from imported external data.

External data should be labeled by confidence and reconciliation status:

- reported
- estimated
- normalized
- reviewed
- confirmed
- disputed

External-platform sales, refunds, imported barcodes, quota returns, settlement numbers, and scan results must remain traceable to source artifacts and ingestion jobs.

## Open Questions

1. Which webhook subscribers are required for the first production release: WordPress sites, scanner, external partners, social publishing, or all as architecture only?
2. Which social channels should be implemented first?
3. Are social posts auto-published or always approval-based at launch?
4. What are the first approved billing tariffs?
5. Is Stripe Billing the first and only billing provider for service invoices, or should the adapter stay provider-neutral from day one?
6. Should invoices be auto-collected immediately or generated as draft first for client approval?
7. What post-event report cutoff should be used: immediately after event, next day, after scan window, or after settlement window?
8. Which recipients get reports by default: organizer owners, event managers, agents, finance contacts, platform operators?
