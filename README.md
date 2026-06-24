# arena_new

[![CI](https://github.com/abhteam/arena_new/actions/workflows/ci.yml/badge.svg)](https://github.com/abhteam/arena_new/actions/workflows/ci.yml)

Production-grade backend for a multi-tenant ticketing platform — the successor
to the legacy Bil24 / TixGear ecosystem.

## Current implementation status

As of 2026-06-24, the checked-in implementation is no longer limited to the
original **Backend Foundation Milestone**. The codebase and AutoForge backlog
now cover a broad scaffold through Wave 20 / 171 features, including identity,
organizations, catalog, inventory, reservations, checkout, payments, tickets,
scanner integration boundaries, WordPress/Bil24 compatibility, reporting,
billing, superadmin, webhook delivery, and reconciliation.

The original foundation-only text is retained below where it explains the
initial architecture, but it is historical context rather than the current
implementation scope.

Verified reconciliation status as of 2026-06-24:

| Gate | Status | Evidence |
|------|--------|----------|
| Architecture/spec status | Reconciled for current broad-scaffold stage | `CLAUDE.md` and `.autoforge/prompts/app_spec.txt` now explicitly mark the foundation-only text as historical seed context |
| OpenAPI -> Go generation | Passing with warning | `oapi-codegen v2.4.1` regenerates `apps/backend/internal/adapters/http/openapi/types_gen.go`; warning remains that OpenAPI 3.1 is not fully supported by the generator |
| OpenAPI -> TypeScript generation | Passing | `npm.cmd run gen-ts-client` and `npm.cmd run check-ts` |
| Go tests | Passing | `go test ./... -count=1` in `golang:1.24` |
| Race/coverage tests | Passing | `go test -race -coverprofile=/tmp/coverage.out -covermode=atomic ./...` in `golang:1.24` |
| Runtime DB migrations | Passing locally | `docker compose` runtime healthy; DB applied through embedded migration `0041_reconciliation_reports.sql` |
| Lint | Failing | `golangci-lint:latest` now loads the v2 config, but reports 563 existing issues |

Current readiness remains **not production-ready / not CI-green** because the
lint gate is still red. The next engineering stage is lint cleanup, not new
feature implementation.

> Кратко по-русски: первый milestone строит чистый production-ready
> backend-скелет на Go (modular monolith, `net/http` + chi, PostgreSQL 17,
> Redis 7, OpenTelemetry, i18n, Dockerfile/Dokploy). Бизнес-логика
> тикетинга в этот milestone **не** входит — только архитектурные
> boundary placeholders.

---

## Repository layout

```
arena_new/
├── .editorconfig                 # Editor whitespace / indent conventions
├── .env.example                  # Documented environment variables (copy to .env)
├── .github/                      # GitHub Actions (lint, test, build, push)
├── .gitignore                    # Go template + .env, dist/, bin/, IDE files
├── README.md                     # ← you are here
├── go.mod                        # Module: github.com/abhteam/arena_new (Go 1.24 + toolchain)
├── init.sh                       # One-shot bring-up (docker compose + migrations)
├── app_spec.txt                  # Full project specification driving the backlog
├── 08_architecture/              # Architecture brief, ADR log, master spec
├── 09_autoforge/                 # Agent guardrails for autonomous coding agents
└── apps/
    └── backend/                  # Go modular monolith (this milestone's deliverable)
        ├── cmd/
        │   ├── arena-api/        # HTTP API server binary
        │   ├── arena-worker/     # Background worker binary
        │   └── arena-migrate/    # goose-driven migration tool binary
        ├── internal/
        │   ├── platform/         # Config, slog, pgx, otel, chi middleware, boundaries
        │   ├── domain/           # Pure domain types (no I/O)
        │   ├── app/              # Use-cases orchestrating domain + platform
        │   ├── adapters/         # Concrete impls of platform boundaries (pg, http, jwt)
        │   ├── migrations/       # Embedded goose SQL migrations
        │   ├── openapi/          # Generated server types from openapi.yaml
        │   └── tests/            # Module-internal test helpers (fixtures, harnesses)
        ├── openapi/              # OpenAPI 3.1 contract + generated TS clients
        ├── queries/              # sqlc input SQL
        ├── i18n/                 # go-i18n message catalogs (ru.toml, en.toml)
        └── tests/                # End-to-end / integration suites
```

The repository is a single Go module rooted at the repo root (module path
`github.com/abhteam/arena_new`). The `apps/backend/` layout below the module
root follows the architecture brief's *Initial Repository Shape* and matches
the package paths the platform code already imports
(`github.com/abhteam/arena_new/apps/backend/internal/platform/...`). The Go
version is pinned via the `toolchain` directive in `go.mod`, so any
contributor — local developer, CI runner, or autonomous agent — uses an
identical compiler.

---

## Quick start (local development)

Prerequisites: Docker 24+, Docker Compose v2, Go 1.24+ (optional for local
test runs — container builds work without a local toolchain).

```bash
# One-shot: copy .env, bring up postgres + redis + api + worker, wait for /readyz
./init.sh
```

Useful URLs once `init.sh` reports readiness:

| Endpoint                  | Purpose                              |
| ------------------------- | ------------------------------------ |
| http://localhost:8080/healthz | liveness probe                   |
| http://localhost:8080/readyz  | readiness probe (DB + migrations)|
| http://localhost:8080/metrics | Prometheus exposition            |
| http://localhost:8080/v1/info | service metadata example         |

---

## Observability stack (Prometheus + Grafana)

The repo ships ready-to-import Grafana dashboard templates in `ops/grafana/dashboards/`.
A matching Prometheus scrape config lives in `ops/prometheus/prometheus.yml`.

### Quick start

```bash
# Start core services + observability stack
docker compose --profile observability up -d

# Open Grafana in your browser (credentials: admin / admin)
open http://localhost:3000

# Prometheus UI (metric browser, target health)
open http://localhost:9090
```

Grafana is pre-provisioned with a datasource pointing at the local Prometheus
container.  The "Arena Platform Overview" dashboard loads automatically on first
start (via `ops/grafana/provisioning/`).

### Dashboard panels

The **Arena Platform Overview** dashboard (`ops/grafana/dashboards/arena_platform_overview.json`)
covers all key operational signals:

| Section | Panels |
|---------|--------|
| **HTTP — Latency by Route** | p50/p95/p99 request latency per route; request rate by route & status; error rate (4xx/5xx); handler panics; idempotency replays |
| **Worker — Queue Lag** | Age of oldest ready job per queue (time-series + gauge); alert threshold at 60 s |
| **Outbox — Event Backlog** | Pending outbox events (time-series + stat); alert threshold at 100 events |
| **Webhooks — Delivery Success Rate** | Success rate per event_type; delivery latency p50/p95/p99; retries & dead-letter counts |
| **Payment Provider — Error Rate** | 4xx/5xx error rate on `/v1/checkout*` and `/v1/payments*` routes; request volume |
| **Database — Connection Pool** | Open / in-use / idle connections; pool wait count & cumulative wait duration |

### Metric reference

All arena_new metrics use the `arena_` namespace.  The full list:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `arena_http_request_duration_seconds` | Histogram | method, route, status | HTTP request latency |
| `arena_http_requests_total` | Counter | method, route, status | HTTP request count |
| `arena_http_panics_total` | Counter | — | Handler panics caught by Recoverer |
| `arena_db_pool_open_connections` | Gauge | — | Total open pgx pool connections |
| `arena_db_pool_idle` | Gauge | — | Idle pgx pool connections |
| `arena_db_pool_in_use` | Gauge | — | Acquired pgx pool connections |
| `arena_db_pool_wait_count` | Gauge | — | Cumulative pool exhaustion events |
| `arena_db_pool_wait_duration_seconds` | Gauge | — | Cumulative pool wait time |
| `arena_worker_jobs_lag_seconds` | Gauge | queue | Age of oldest ready job |
| `arena_outbox_backlog` | Gauge | — | Pending outbox events |
| `arena_idempotency_replays_total` | Counter | — | Idempotency key replay hits |
| `arena_idempotency_cleanup_deleted_total` | Counter | — | Idempotency rows purged by maintenance |
| `arena_webhook_delivery_duration_seconds` | Histogram | subscriber_url, event_type | Webhook delivery round-trip |
| `arena_webhook_retry_total` | Counter | subscriber_url, event_type | Webhook retry attempts |
| `arena_webhook_dead_letter_total` | Counter | subscriber_url, event_type | Webhook dead-letter events |

### Importing dashboards manually into an existing Grafana

If you run your own Grafana instance (not the docker-compose one above):

1. In Grafana, go to **Dashboards → Import**.
2. Upload the JSON file from `ops/grafana/dashboards/arena_platform_overview.json`.
3. Select your Prometheus datasource when prompted.
4. Click **Import**.

The dashboard uses `${DS_PROMETHEUS}` as a datasource variable so it adapts
to any Prometheus instance.

### Production deployment (Dokploy)

For Dokploy-managed production:

1. Deploy Prometheus as a separate service pointing at the `arena-api` /metrics endpoint.
2. Deploy Grafana as a separate service, mounting `ops/grafana/provisioning/` and
   `ops/grafana/dashboards/` as read-only volumes (or bake them into a custom Grafana image).
3. Set `GF_SECURITY_ADMIN_PASSWORD` via Dokploy environment variables (never use the
   default `admin/admin` in production).
4. Restrict `/metrics` at the ingress layer (firewall rule or nginx `deny all; allow <prometheus_ip>;`).

---

## Code generation

The backend uses two code-generation tools.  Both must be re-run and the output
committed whenever the corresponding source files change.

### OpenAPI → Go server types + TypeScript client

```bash
make gen-openapi      # regenerate apps/backend/internal/adapters/http/openapi/
make gen-ts-client    # regenerate apps/backend/openapi/clients/ts/index.d.ts
```

Source: `apps/backend/openapi/openapi.yaml`
Config: `apps/backend/openapi/oapi-codegen.yaml`

### sqlc → typed SQL query wrappers

```bash
make sqlc-generate
```

This command runs `sqlc generate` inside `apps/backend/` using the config at
`apps/backend/sqlc.yaml`.

| Path | Role |
|------|------|
| `apps/backend/sqlc.yaml` | sqlc configuration (engine, package, output path, overrides) |
| `apps/backend/internal/adapters/postgres/queries/` | Hand-written `.sql` source files with `-- name: QueryName :one/:many/:exec` annotations |
| `apps/backend/internal/adapters/postgres/gen/` | Generated Go package — **do not edit by hand**; commit the output alongside the SQL source |

**Prerequisites:** sqlc v2 must be on your `PATH`.

```bash
# Install via Go toolchain
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

# Or download a pre-built binary
# https://docs.sqlc.dev/en/stable/overview/install.html
```

**Example query** (`internal/adapters/postgres/queries/system.sql`):

```sql
-- name: SelectUUIDv7 :one
SELECT uuidv7() AS id;
```

After running `make sqlc-generate`, the generated `*Queries.SelectUUIDv7(ctx)`
method returns a `uuid.UUID` and serves as proof that the sqlc pipeline is
correctly wired.

---

## Technology stack (target end-of-milestone)

| Concern           | Choice                                                                |
| ----------------- | --------------------------------------------------------------------- |
| Language          | Go 1.24.x (pinned via `toolchain` directive)                          |
| HTTP foundation   | Standard library `net/http`                                           |
| Router            | `chi` v5                                                              |
| Database          | PostgreSQL 17 via `pgx/v5`                                            |
| SQL access        | `sqlc`-generated typed wrappers; explicit transactions in workflows   |
| Migrations        | `goose` embedded via `embed.FS`, driven by `arena-migrate`            |
| Cache / locks     | Redis 7 (Postgres remains source of truth)                            |
| Background jobs   | Postgres-backed queue (`FOR UPDATE SKIP LOCKED`) + outbox pattern     |
| API contract      | OpenAPI 3.1 → `oapi-codegen` for Go server types + TypeScript clients |
| ID strategy       | UUIDv7 (native PG17 `uuidv7()` with Go-side fallback)                 |
| Logging           | `log/slog` (JSON in prod, text in dev) with request/correlation IDs   |
| Metrics           | Prometheus `/metrics`                                                 |
| Tracing           | OpenTelemetry SDK with OTLP gRPC exporter                             |
| Internationalization | `go-i18n/v2`, TOML catalogs; `ru` and `en` active                  |
| Deployment        | Multi-stage Docker → distroless; Dokploy-compatible repository layout |

---

## Authentication — PLACEHOLDER (Foundation Milestone)

> **⚠ PLACEHOLDER — This is NOT production-grade authentication.**
>
> The `internal/platform/auth` package provides a development-only JWT boundary
> for the Backend Foundation Milestone. It is **not** a substitute for a real
> identity system.

### What is wired now

| Component | Description |
|-----------|-------------|
| `auth.AuthContext` | Value type (`ActorID uuid.UUID`, `OrgID *uuid.UUID`, `Roles []string`, `TokenID string`) stored on every authenticated request context. |
| `auth.WithAuthContext` / `auth.FromContext` | Context helpers to write/read `AuthContext`. |
| `auth.ValidateJWT(secret)` | HS256 middleware using [`github.com/golang-jwt/jwt/v5`](https://github.com/golang-jwt/jwt). Extracts the `Authorization: Bearer …` token, verifies the HMAC-SHA256 signature, validates time claims, and stores `AuthContext` on the request context. Returns `401` with a standard error envelope on failure. |
| `auth.IssueJWT(…)` | Dev-only HS256 token minter (jwt/v5-backed). |
| `POST /v1/dev/auth/token` | Dev endpoint that issues a signed JWT. **Only mounted when `ENABLE_DEV_AUTH=true`.** Blocked in production. |
| `auth.StubProvider` | Manual HS256 issuer/verifier (HMAC-SHA256 without jwt/v5). Used by the existing `/v1/dev/token` and `/v1/echo` middleware chain. |

### What is NOT wired (deferred)

- Real user identity (OAuth 2.0, magic link, password hashing)
- RS256 / ECDSA JWT verification against a real IdP (Keycloak, Auth0, custom)
- Token revocation / deny-list
- Per-organization role management / RBAC enforcement
- Refresh token rotation

### Dev quick-start

```bash
# Get a dev JWT (ENABLE_DEV_AUTH=true required)
curl -s -X POST http://localhost:8080/v1/dev/auth/token \
  -H "Content-Type: application/json" \
  -d '{"actor_id":"00000000-0000-0000-0000-000000000042","roles":["admin"]}' \
  | jq -r .token

# Use it to call an authenticated endpoint
curl -s http://localhost:8080/v1/echo \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-1" \
  -d '{"message":"hello"}'
```

#### POST /v1/scaffold/echo — scaffolding example (will be removed)

`POST /v1/scaffold/echo` is a **scaffolding example endpoint** that demonstrates
the full cross-cutting boundary stack in a single PostgreSQL transaction:
auth → permission check (`scaffold.echo.create`) → idempotency →
`scaffold_echo` table INSERT (sqlc) → audit event → outbox event → COMMIT → 201.

> **Note:** This endpoint is a scaffolding example and will be removed when real
> domain command endpoints (orders, tickets, etc.) arrive in later milestones.
> It is safe to call in development but should not be used for any real business logic.

#### POST /v1/echo — strict schema policy

`POST /v1/echo` enforces a **strict schema** on the request body: any field not
defined in `EchoRequest` (i.e. anything other than `"message"`) causes an
immediate `HTTP 400` with `code='validation.unknown_field'` and
`error.details.field` set to the offending field name. The field's **value is
never echoed** in the error to prevent information leakage.

This catches common typos (`"messsage"` instead of `"message"`) and prevents
clients from silently passing private data in unknown fields that the server
would otherwise ignore. The policy is enforced at the handler layer after JSON
decode, documented in `openapi/openapi.yaml` via `additionalProperties: false`
on the `EchoRequest` schema.

---

## Original foundation scope (historical)

This section documents the original foundation milestone. It is not a complete
description of the current implementation, which has since advanced into broad
domain scaffolding.

**In scope**: repository layout, three binaries (`arena-api`,
`arena-worker`, `arena-migrate`), config + slog + health + graceful shutdown,
pgx pool + goose migrations + transaction helper, chi router with `/v1` prefix
and standard middleware + uniform error envelope, OpenAPI 3.1 skeleton with
`GET /v1/info` and `POST /v1/echo`, cross-cutting boundary placeholders
(Auth / Permission / Idempotency / Audit / Outbox / Webhook), worker scaffold
with retry + dead-letter, i18n scaffold, observability (`/metrics`, OTLP),
testing harness with `testcontainers-go`, Dockerfile + `docker-compose.yml` +
`.env.example`, Dokploy deployment guide.

**Out of scope (deferred to subsequent milestones)**: real identity / auth,
organizations / memberships / roles, catalog (events, sessions, venues,
seating plans), inventory, reservations, checkout, payments, refunds,
disputes, payouts, tickets, complimentary issuance, external quotas,
scanner integration, WordPress plugin, Bil24 gateway, reporting, service
billing, superadmin console, frontend / admin UI / public checkout UI, payment
provider adapters (Stripe, YooKassa, etc.).

See `app_spec.txt` for the authoritative specification driving the
AutoForge backlog of 80+ scaffold features.

---

## Reference architecture documents

Detailed architectural rationale lives in `08_architecture/`:

* `08_architecture/13_backend_go_initial_specification_ru.md` — primary source for this milestone
* `08_architecture/00_backend_architecture_brief_ru.md`
* `08_architecture/11_architecture_decision_log_ru.md`
* `08_architecture/12_master_platform_specification_ru.md`
* `08_architecture/10_compliance_security_privacy_ru.md`

Guardrails for autonomous coding agents working on this repository live in
`09_autoforge/00_AGENT_GUARDRAILS.md`.

---

## Deploying to Dokploy

A dedicated *"Deploying to Dokploy"* section will be added in Wave 10 once the
production `Dockerfile` and `docker-compose.yml` are finalised. At that point
this README will document: app creation, environment variable injection,
Postgres volume binding, domain + SSL configuration, and `/healthz`-driven
auto-restart.

---

## License

Proprietary. © ABH Team. All rights reserved.
