# Arena Production Release Checklist

**Document owner:** Backend platform team
**Status:** Signed — ready for production release gate
**Last reconciled:** 2026-06-25 (feature #190; signed under #181)
**Tracking feature:** AutoForge #181 "Reconciliation чек-лист готовности к production"
**Reconciliation history:** #181 (sign), #190 (commands reconciled with `Makefile` + `.github/workflows/ci.yml`)

This checklist is the single source of truth for the four-gate production
readiness contract that was previously asserted in the
`<implementation_status_override>` block of `CLAUDE.md`. With #181 closed,
the override block has been removed; this document supersedes it.

## Scope

The Wave 20 broad-scaffold milestone (171/171 features) introduced bounded
contexts for identity, organizations, catalog, inventory, checkout, payments,
tickets, scanner integration boundaries, WordPress/Bil24 compatibility,
reporting, billing, superadmin, webhook delivery, and reconciliation.

Production readiness for that milestone is gated on the items below. All
gates are green on the `master` branch as of 2026-06-25. Gates 1-4 form the
original four-gate signed contract; Gate 5 (container image builds) is
included here for reproducibility against the CI `build-and-push` job.

> **Environment-side gate:** the four (now five) gates here assert that a
> build is *capable* of being promoted. Promoting that build to a public
> environment additionally requires walking
> [`PRODUCTION_HARDENING_CHECKLIST.md`](PRODUCTION_HARDENING_CHECKLIST.md)
> (feature #193) on the target environment — `APP_ENV=production`,
> `ENABLE_DEV_AUTH=false`, rotated `JWT_SIGNING_SECRET`, SSL on the
> database, locked-down CORS, JSON logs with `DB_LOG_QUERIES=false`,
> Grafana off `admin / admin`, and Postgres / Redis / Prometheus /
> worker-metrics ports not on the public network.

> **Pre-promotion rehearsal:** before promoting a build to production,
> run the staging deploy rehearsal documented in
> [`STAGING_REHEARSAL_REPORT.md`](STAGING_REHEARSAL_REPORT.md) (feature
> #194). The rehearsal walks the seven acceptance steps (deploy,
> `arena-migrate up`, migration head matches `0041_reconciliation_reports.sql`,
> `/healthz` + `/readyz` + `/v1/info`, worker job pickup, backup
> dry-run, captured release notes) against the candidate image and
> records the captured outputs as the promotion evidence.

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
make gen-openapi                     # OpenAPI -> Go server types (oapi-codegen v2.4.1)
make gen-ts-client                   # OpenAPI -> TypeScript client types
# Alternative end-to-end driver (same output, calls both generators):
./generate-clients.sh
git status                           # expect clean working tree
```

- `apps/backend/openapi/openapi.yaml` is the contract source of truth.
- `make gen-openapi` writes
  `apps/backend/internal/adapters/http/openapi/types_gen.go`; CI Job 3
  (`openapi-check` in `.github/workflows/ci.yml`) re-runs the same target and
  fails on any uncommitted drift, so this command is authoritative.
- `make gen-ts-client` writes
  `apps/backend/openapi/clients/ts/index.d.ts`; verify with
  `npx tsc --noEmit apps/backend/openapi/clients/ts/index.d.ts`.
- Generated Go server types and TypeScript client types are committed and
  byte-identical to a fresh regeneration.
- Known acceptable warning: oapi-codegen v2.4.1 does not fully support
  OpenAPI 3.1; this is tracked in `08_architecture/11_architecture_decision_log_ru.md`
  and does not gate release.
- `make generate` does NOT exist in `Makefile`. Use `make gen-openapi`
  and `make gen-ts-client` explicitly (or run `./generate-clients.sh`).

## Gate 3 — Tests pass (unit + race + coverage + integration)

**Owner:** Backend
**Status:** GREEN
**Reproduce in `golang:1.24`:**

```bash
# Equivalent Make targets exist (make test / make test-race / make lint);
# the raw commands below mirror what CI executes in .github/workflows/ci.yml.
make test            # ≡ go test ./...
make test-race       # ≡ go test -race ./...
# CI Job 2 ("Test") runs:
go test -race -coverprofile=coverage.out -covermode=atomic ./...
# CI Job 1 ("Lint") runs golangci-lint via golangci/golangci-lint-action@v6
# with `--timeout=5m`; locally use either:
make lint                                 # golangci-lint run ./...
golangci-lint run --timeout=5m ./...      # explicit, matches CI argument
```

- All packages under `apps/backend/...` pass `go test ./...`.
- Race detector run with coverage profile completes clean.
- `golangci-lint:latest` reports zero issues (feature #182 closed the 563-issue
  baseline that was the last red gate at commit `8e8ad9d`). The CI Lint job in
  `.github/workflows/ci.yml` uses `--timeout=5m`; this checklist matches that
  value so local runs do not contradict the CI gate.
- Static-analysis gates (DDD layout, file-size ratchet, context-Background
  ban) under `apps/backend/tests/staticanalysis/...` pass.

## Gate 4 — Runtime databases migrated

**Owner:** SRE / Backend
**Status:** GREEN
**Reproduce:**

```bash
docker compose up -d postgres
# Run the migrator from source (no install needed):
make migrate-up                              # ≡ go run ./apps/backend/cmd/arena-migrate up
# Or, if the compiled binary is already on PATH:
arena-migrate up
arena-migrate status                         # expect: 0041_reconciliation_reports.sql (applied)
```

- Embedded migrations live in `apps/backend/internal/migrations/sql/`.
- The latest applied migration is `0041_reconciliation_reports.sql`.
- Local `docker-compose` stack is healthy; PostgreSQL 17 is the runtime
  database.
- Before promoting a release: run `arena-migrate up` against staging and
  production and confirm `0041_reconciliation_reports.sql` is the head
  migration on both.

## Gate 5 — Container image builds

**Owner:** SRE / Backend
**Status:** GREEN
**Reproduce:**

```bash
# Single-image build (same Dockerfile CI uses in the build-and-push job):
docker build -t arena-api:local .
# Full stack (api + worker + postgres + redis), exercising docker-compose.yml:
docker compose build
docker compose up -d --wait
curl -sf http://localhost:8080/readyz       # expect HTTP 200
```

- `Dockerfile` is the source CI's `build-and-push` job consumes
  (`.github/workflows/ci.yml` → `docker/build-push-action@v6`).
- `docker compose up -d --wait` is the same sequence
  `.github/workflows/load-test.yml` uses to spin up the full stack.
- `init.sh` is a one-shot convenience wrapper around `docker compose up`
  plus migrations for first-time local bring-up.
- **admin-web reachable on localhost (feature #252):** `docker compose up`
  brings up the SuperAdmin UI on `http://localhost:5174` via the
  `admin-web` service. Verification: container `arena_admin_web`
  reports `healthy`, `curl -I http://localhost:5174/` returns HTTP 200,
  browser login through `/v1/auth/login` succeeds without CORS errors
  (api `CORS_ALLOWED_ORIGINS=*` in local dev — tighten for prod).

---

## Signature

This checklist is countersigned by the closing of AutoForge feature #181.

| Gate                                  | Backing feature | State  |
|---------------------------------------|-----------------|--------|
| 1. Architecture/spec reconciled       | #180            | passed |
| 2. Generated clients current          | n/a (CI Job 3)  | green  |
| 3. Tests + lint green                 | #182            | passed |
| 4. Runtime migrations through 0041    | n/a (ops)       | green  |
| 5. Container image builds             | n/a (CI build)  | green  |

Reproduce commands above are byte-for-byte aligned with `Makefile` targets
(`make gen-openapi`, `make gen-ts-client`, `make test`, `make test-race`,
`make lint`, `make migrate-up`) and the steps in `.github/workflows/ci.yml`
as of this reconciliation (#190).

With all gates green, the `<implementation_status_override>` block
that previously lived in `CLAUDE.md` is retired. Future scope expansions
beyond `08_architecture/14_current_implementation_overview_ru.md` MUST
land as ADRs under `08_architecture/11_architecture_decision_log_ru.md`
before code changes are merged.
