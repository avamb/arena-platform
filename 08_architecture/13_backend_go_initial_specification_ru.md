# Initial Backend Specification: Go Stack

Обновлено: 2026-06-21

Статус: `initial specification`, backend stack decision accepted for Go. Не является полной master specification.

## Назначение

Этот документ фиксирует начальную техническую спецификацию backend core после решения использовать Go как основной язык backend.

Scope этого файла:

- platform backend core;
- platform API;
- transactional domain: organizations, catalog, inventory, reservations, orders, payments, refunds, tickets, external allocations, billing, reports, audit, webhooks;
- background workers, outbox, jobs and operational APIs.

Out of scope:

- scanner service implementation;
- frontend/admin/checkout implementation;
- WordPress plugin implementation;
- mobile apps;
- final production infrastructure sizing.

Scanner remains an external boundary. Backend publishes events/import APIs and receives scan/report events, but scanner runtime is not part of backend core.

## Accepted Decision

Primary backend language:

```text
Go
```

Reason:

- backend must be fast, predictable and resource-efficient;
- transactional ticketing core needs explicit concurrency, short critical sections, reliable workers and simple deployment;
- Go is a strong fit for API servers, background workers, webhooks, queues and cloud/network services;
- a compiled binary/container deployment reduces runtime complexity;
- Go keeps performance high without forcing Rust-level development cost.

## Proposed Stack

These are proposed defaults for the first implementation scaffold.

```text
Language: Go
Application shape: modular monolith first
HTTP foundation: standard net/http
Router: chi as proposed default
Database: PostgreSQL
PostgreSQL driver: pgx
SQL access: sqlc + explicit transaction boundaries
Migrations: goose or Atlas, to confirm before scaffold
Cache/locks: Redis where needed, but Postgres remains source of truth
Background work: Postgres outbox/jobs first; external queue only when load requires it
API contract: OpenAPI-first
Serialization: JSON for public APIs, internal formats only when justified
Observability: structured logs, metrics, traces, health checks
Load testing: k6 or equivalent before production launch
```

## Framework Rule

The backend must avoid a heavy framework as the default architecture.

Allowed:

- `net/http`;
- `chi` or similar lightweight router;
- explicit middleware chain;
- small internal packages for domain/application/adapters;
- generated OpenAPI client/server types where they reduce drift.

Avoid by default:

- heavy full-stack web framework;
- ORM-first domain model;
- framework-specific magic for transactions, permissions, idempotency or events;
- placing business logic inside HTTP handlers;
- coupling API schema to database schema.

Reason:

Ticketing performance is usually lost in database locks, slow payment/provider boundaries, over-broad transactions, missing idempotency and uncontrolled payloads, not only in router overhead. The Go backend should keep those boundaries explicit.

## Initial Repository Shape

Recommended first scaffold:

```text
apps/
  backend/
    cmd/
      arena-api/
      arena-worker/
      arena-migrate/
    internal/
      platform/
        config/
        logging/
        observability/
        clock/
        ids/
      domain/
        identity/
        organizations/
        catalog/
        inventory/
        checkout/
        payments/
        tickets/
        allocations/
        billing/
        reporting/
        audit/
      app/
        commands/
        queries/
        workflows/
      adapters/
        http/
        postgres/
        redis/
        paymentproviders/
        webhooks/
      migrations/
      openapi/
      tests/
```

Notes:

- `domain` owns business vocabulary and invariants.
- `app` owns use cases/workflows and transaction boundaries.
- `adapters` own IO: HTTP, PostgreSQL, Redis, payment providers, webhook delivery.
- HTTP handlers must call application services, not mutate database state directly.
- Workers use the same application services and idempotency rules as HTTP flows.

## Core Runtime Services

The first backend scaffold must include:

- configuration loading and validation;
- structured logging with request ID/correlation ID;
- health/readiness endpoints;
- database connection pool;
- migration command;
- graceful shutdown;
- panic recovery middleware;
- auth placeholder boundary;
- permission check boundary;
- idempotency middleware/store boundary;
- audit writer boundary;
- outbox writer and dispatcher boundary;
- OpenAPI route/versioning structure;
- test harness for Postgres-backed integration tests.

## Transaction And Concurrency Rules

Required rules:

1. No external provider call inside inventory/payment/order database critical section.
2. Every mutating request has request ID, actor context, idempotency key where required and audit context.
3. Reservation/order/payment transitions must use explicit transactions.
4. Inventory-changing writes must be short, bounded and observable.
5. Long-running work moves to jobs/outbox.
6. Worker jobs must be idempotent.
7. Provider webhooks must be verified before mutation and deduplicated before side effects.
8. Database lock strategy must be documented per state machine before implementation.

## Database Access Rules

Preferred approach:

- SQL is explicit.
- `pgx` is the PostgreSQL driver.
- `sqlc` generates typed query wrappers where useful.
- Repositories are thin and do not hide transaction semantics.
- Domain workflows own transaction boundaries.

Avoid:

- broad generic repositories;
- ORM lifecycle hooks for business events;
- hidden implicit transactions;
- automatic lazy loading;
- database schema generated from structs without review.

Reason:

Ticketing correctness depends on precise inventory locks, idempotency, state transitions, ledger writes and audit/outbox writes. Those must be visible in code and tests.

## API Rules

Platform API should be OpenAPI-first.

Required:

- stable `/v1` prefix;
- typed request/response schemas;
- consistent error envelope;
- idempotency key header for mutating operations that can be retried;
- request ID/correlation ID;
- pagination standard;
- filtering/sorting limits;
- explicit permission errors;
- rate-limit headers where applicable;
- compatibility gateway separate from platform-native API.

OpenAPI should generate:

- server contract checks where practical;
- TypeScript clients for frontend/admin/checkout;
- PHP client/helper layer for WordPress plugin if useful;
- contract fixtures for AutoForge tests.

## Performance Targets

Initial targets are engineering budgets, not final SLA.

```text
Simple health/readiness endpoint: p95 < 50 ms
Cached/catalog read endpoint: p95 < 150 ms
Transactional read endpoint: p95 < 250 ms
Reservation mutation excluding external provider calls: p95 < 300 ms
Checkout mutation excluding external provider calls: p95 < 500 ms
Provider webhook acknowledgement after verified durable write: p95 < 300 ms
Background outbox dispatch lag under normal load: p95 < 5 s
```

Rules:

- payment provider latency is measured separately;
- email/SMS/social delivery is asynchronous;
- external report ingestion is asynchronous;
- performance tests must include database and realistic payload sizes;
- no production launch without load test profile tied to first production scope.

## Observability

Required from first scaffold:

- structured JSON logs;
- request ID and trace ID propagation;
- metrics for HTTP latency, errors, DB pool, job lag, webhook delivery, payment callbacks and outbox backlog;
- tracing boundary using OpenTelemetry or compatible abstraction;
- `/healthz` and `/readyz`;
- protected diagnostics only for trusted operations;
- no raw sensitive PII/payment data in normal logs.

Recommended Go libraries:

- standard `log/slog` or `zap` for logs;
- OpenTelemetry for traces/metrics abstraction;
- Prometheus-compatible metrics where deployment supports it.

## Testing Requirements

Before backend implementation tickets are considered done:

- unit tests for domain invariants;
- integration tests against real PostgreSQL;
- transaction/concurrency tests for reservation and inventory;
- idempotency tests for mutating endpoints and webhooks;
- permission tests for every protected endpoint family;
- migration up/down or forward-only migration verification policy;
- race detector in CI for relevant packages;
- API contract tests from OpenAPI;
- load smoke tests for critical paths.

## First Backend Milestone

The first coding milestone should not implement business behavior deeply. It should produce a clean foundation:

1. Go module and project layout.
2. API server boot.
3. Worker boot.
4. Config validation.
5. Logging, health, readiness.
6. PostgreSQL connection and migrations baseline.
7. OpenAPI skeleton.
8. Error envelope.
9. Request ID/correlation middleware.
10. Idempotency boundary placeholder.
11. Audit/outbox boundary placeholder.
12. One example read endpoint and one example transactional command behind tests.

This milestone proves the architecture shape before implementing reservations, payments or ticket issuance.

## Open Decisions Before Coding

These must be confirmed before scaffold implementation:

- exact Go version to pin at implementation start;
- exact router: `chi` proposed default;
- migration tool: `goose` or Atlas;
- SQL generation: `sqlc` accepted or alternative;
- ID strategy: UUIDv7, ULID, Snowflake-like, or BIGINT/internal + public string IDs;
- deployment target and CI runner;
- first observability stack;
- first auth/session mechanism.

## Non-Goals

Do not implement at backend scaffold stage:

- scanner runtime;
- full seating editor;
- payment provider adapter;
- payout execution;
- AI ingestion;
- public discovery marketplace;
- complex microservice split;
- frontend/admin UI;
- WordPress plugin code.

## Source Links

Related project files:

- `08_architecture/11_architecture_decision_log_ru.md`
- `08_architecture/12_master_platform_specification_ru.md`
- `08_architecture/09_domain_state_machines_ru.md`
- `08_architecture/10_compliance_security_privacy_ru.md`
- `09_autoforge/00_AGENT_GUARDRAILS.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`
