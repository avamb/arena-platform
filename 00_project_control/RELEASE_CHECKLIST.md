# Arena Production Release Checklist

**Document owner:** Backend platform team
**Status:** Signed — ready for production release gate
**Last reconciled:** 2026-06-25 (feature #181)
**Tracking feature:** AutoForge #181 "Reconciliation чек-лист готовности к production"

This checklist is the single source of truth for the four-gate production
readiness contract that was previously asserted in the
`<implementation_status_override>` block of `CLAUDE.md`. With #181 closed,
the override block has been removed; this document supersedes it.

## Scope

The Wave 20 broad-scaffold milestone (171/171 features) introduced bounded
contexts for identity, organizations, catalog, inventory, checkout, payments,
tickets, scanner integration boundaries, WordPress/Bil24 compatibility,
reporting, billing, superadmin, webhook delivery, and reconciliation.

Production readiness for that milestone is gated on the four items below.
All four are now green on the `main` branch as of 2026-06-25.

---

## Gate 1 — Architecture & specification reconciled with implementation

**Owner:** Architecture
**Status:** GREEN
**Backing feature:** #180 (closed)

- `08_architecture/14_current_implementation_overview_ru.md` is the
  authoritative inventory of bounded contexts and bounded-context
  responsibilities, synchronized with code on 2026-06-25.
- `08_architecture/12_master_platform_specification_ru.md` is marked
  `initial draft` (pending rewrite).
- `08_architecture/13_backend_go_initial_specification_ru.md` is marked
  `historical / superseded`.
- `08_architecture/11_architecture_decision_log_ru.md` carries the
  "ADR-protocol on scope expansion" section: any scope expansion beyond
  doc 14 requires a fresh ADR.

## Gate 2 — Generated clients are current

**Owner:** Backend
**Status:** GREEN
**Reproduce:**

```bash
./generate-clients.sh                # OpenAPI -> TypeScript clients
make generate                        # OpenAPI -> Go server types (oapi-codegen)
git status                           # expect clean working tree
```

- `apps/backend/openapi/openapi.yaml` is the contract source of truth.
- Generated Go server types and TypeScript client types are committed and
  byte-identical to a fresh regeneration.
- Known acceptable warning: oapi-codegen v2.4.1 does not fully support
  OpenAPI 3.1; this is tracked in `08_architecture/11_architecture_decision_log_ru.md`
  and does not gate release.

## Gate 3 — Tests pass (unit + race + coverage + integration)

**Owner:** Backend
**Status:** GREEN
**Reproduce in `golang:1.24`:**

```bash
go test ./... -count=1
go test -race -coverprofile=/tmp/coverage.out -covermode=atomic ./...
golangci-lint run --timeout=10m ./...
```

- All packages under `apps/backend/...` pass `go test ./...`.
- Race detector run with coverage profile completes clean.
- `golangci-lint:latest` reports zero issues (feature #182 closed the 563-issue
  baseline that was the last red gate at commit `8e8ad9d`).
- Static-analysis gates (DDD layout, file-size ratchet, context-Background
  ban) under `apps/backend/tests/staticanalysis/...` pass.

## Gate 4 — Runtime databases migrated

**Owner:** SRE / Backend
**Status:** GREEN
**Reproduce:**

```bash
docker compose up -d postgres
arena-migrate up
arena-migrate status        # expect: 0041_reconciliation_reports.sql (applied)
```

- Embedded migrations live in `apps/backend/internal/migrations/sql/`.
- The latest applied migration is `0041_reconciliation_reports.sql`.
- Local `docker-compose` stack is healthy; PostgreSQL 17 is the runtime
  database.
- Before promoting a release: run `arena-migrate up` against staging and
  production and confirm `0041_reconciliation_reports.sql` is the head
  migration on both.

---

## Signature

This checklist is countersigned by the closing of AutoForge feature #181.

| Gate                                  | Backing feature | State  |
|---------------------------------------|-----------------|--------|
| 1. Architecture/spec reconciled       | #180            | passed |
| 2. Generated clients current          | n/a (CI)        | green  |
| 3. Tests + lint green                 | #182            | passed |
| 4. Runtime migrations through 0041    | n/a (ops)       | green  |

With all four gates green, the `<implementation_status_override>` block
that previously lived in `CLAUDE.md` is retired. Future scope expansions
beyond `08_architecture/14_current_implementation_overview_ru.md` MUST
land as ADRs under `08_architecture/11_architecture_decision_log_ru.md`
before code changes are merged.
