# Platform Superadmin And Observability Console

Updated: 2026-06-21

## Decision

The platform needs a `platform_superadmin` role and a dedicated platform operations/admin console. This role is for trusted platform owners/operators who must see how the whole platform works, monitor load and errors, inspect logs, investigate integrations, and access all organizers, agents, sales channels, events, orders, tickets, reports, billing, scanner synchronization, external allocations, and system entities from one window.

This is not the same as an organizer admin or normal operator. It is a cross-tenant platform administration and observability role. Because it can access all organizations and all entities, every sensitive action must be protected by strong authentication, least-privilege sub-permissions where possible, and immutable audit logs.

## Role

### `platform_superadmin`

Capabilities:

- full cross-organization read access
- controlled cross-organization write/support actions
- organization and agent management
- platform user and role management
- sales channel and credential management
- event/session/order/ticket/support inspection
- payment, refund, billing, invoice, and settlement inspection
- webhook, queue, integration, scanner, and external allocation monitoring
- system health, logs, errors, metrics, traces, and background jobs
- feature flags and environment configuration where explicitly granted
- emergency/break-glass access where explicitly enabled

Recommended rule:

`platform_superadmin` should be rare. Day-to-day support should use narrower roles such as `platform_operator`, `support_operator`, `billing_operator`, `integration_operator`, or `readonly_observer` where possible.

## One-Window Platform Console

The superadmin console should provide one operational view across the platform.

Required areas:

- global dashboard
- organizations
- organizers
- agents
- users and identities
- roles and permissions
- events and sessions
- venues and seating plans
- sales channels
- reservations, orders, tickets, refunds
- complimentary invitations
- POS/cash desk activity
- external platform allocations
- external report ingestion and reconciliation
- scanner sync and scan events
- webhooks and event deliveries
- billing accounts, tariffs, invoices, collection attempts
- post-event reports
- support cases and audit log
- system health, load, errors, logs, queues, workers, and deployments

Every detail page should show related entities, for example:

- organization -> users, events, channels, invoices, webhooks, reports
- event -> sessions, inventory, orders, tickets, scans, allocations, reports
- sales channel -> credentials, orders, webhooks, errors, settlement
- user -> identities, memberships, roles, audit activity

## Observability

### Required Signals

The platform should collect and expose:

- metrics
- structured application logs
- error events
- traces/spans for requests and jobs
- audit log
- webhook delivery logs
- background job and queue status
- database health and slow queries
- cache health
- payment provider webhook health
- billing provider webhook health
- scanner sync health
- external ingestion job health

### Required Dashboards

Recommended dashboards:

- platform overview
- API traffic and latency
- checkout/reservation/order health
- payment/refund health
- webhook delivery health
- scanner sync and scan event health
- background jobs and queues
- billing/invoice health
- external allocation and ingestion health
- error rate by service/module
- organization/sales channel traffic
- large-event/hot-sale monitoring

### Error And Log UX

The admin panel should make errors understandable, not just dump raw logs.

Required properties:

- grouped errors by fingerprint
- severity
- first seen / last seen
- affected service/module
- affected organization/sales channel/event where known
- request ID / trace ID
- linked logs
- linked audit events
- status: open, acknowledged, resolved, ignored
- owner/assignee
- notes

Raw logs should be searchable, but sensitive data must be masked. Superadmin can access sensitive operational detail only where permission and audit policy allow it.

## Cross-Tenant Access

Superadmin can access all tenant data, but this must be explicit and audited.

Required rules:

- every cross-organization view/action records actor, target organization, reason/context, IP, session, request ID, and timestamp
- sensitive actions may require reason text
- high-risk actions can require step-up authentication
- support impersonation should prefer "view as" or scoped support session over password-level impersonation
- all support sessions expire
- all destructive actions require explicit confirmation and audit

Recommended permission split:

```text
platform.superadmin.read_all
platform.superadmin.write_all
platform.superadmin.manage_roles
platform.superadmin.manage_credentials
platform.superadmin.manage_feature_flags
platform.superadmin.view_logs
platform.superadmin.view_sensitive_logs
platform.superadmin.view_audit
platform.superadmin.impersonate_readonly
platform.superadmin.impersonate_support
platform.superadmin.break_glass
```

## Operations Objects

Recommended entities:

- `PlatformHealthSnapshot`
- `ServiceHealth`
- `MetricSample`
- `LogEvent`
- `ErrorEvent`
- `TraceReference`
- `QueueStatus`
- `JobRun`
- `WebhookDelivery`
- `IntegrationHealth`
- `SupportAccessSession`
- `AdminAction`
- `AuditLogEntry`

The production implementation can use external observability infrastructure, but the platform console should expose a product-level view that is understandable to platform operators.

## Security Requirements

Required:

- mandatory MFA for superadmins
- short session TTL or step-up for high-risk actions
- IP/session/device audit
- no shared superadmin accounts
- no hidden writes
- permission-gated access to raw logs and sensitive data
- masking for secrets, payment data, personal data, and raw barcodes by default
- immutable audit log for superadmin actions
- break-glass actions require reason and alerting

## Alerts

The platform should support alerts for:

- API error rate
- checkout/reservation failures
- payment/refund failures
- webhook delivery failures
- scanner sync failures
- queue backlog
- worker failures
- billing invoice failures
- external ingestion failures
- database/cache/service degradation
- high-demand event thresholds
- suspicious admin activity

Alerts should route to platform operators and remain visible in the superadmin console.

## Open Questions

1. Which users initially receive `platform_superadmin`?
2. Which operations require step-up authentication?
3. Is support impersonation allowed, and should it be read-only by default?
4. Which observability backend will be used first: self-hosted Grafana/Loki/Tempo/Prometheus, cloud provider tooling, or another stack?
5. What log retention and audit retention are required?
6. Which alerts are mandatory before first production release?
