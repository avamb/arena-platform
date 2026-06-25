# Deploying arena_new to Dokploy

> **Milestone scope:** This guide covers the **Backend Foundation Milestone**.
> Business-logic modules (identity, catalog, payments, etc.) are added in
> subsequent milestones; deployment steps will grow accordingly.

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Production Runtime Topology (api, worker, migrate)](#2-production-runtime-topology-api-worker-migrate)
3. [Create the Application in Dokploy](#3-create-the-application-in-dokploy)
4. [Attach a Managed PostgreSQL Service](#4-attach-a-managed-postgresql-service)
5. [Configure Environment Variables](#5-configure-environment-variables)
6. [Run Database Migrations](#6-run-database-migrations)
7. [Healthcheck & Port Configuration](#7-healthcheck--port-configuration)
8. [Deploy](#8-deploy)
9. [Post-Deploy Smoke Checks](#9-post-deploy-smoke-checks)
10. [Production Environment Variable Checklist](#10-production-environment-variable-checklist)
11. [Secret Rotation](#11-secret-rotation)
12. [Troubleshooting](#12-troubleshooting)

---

## 1. Prerequisites

| Requirement | Version |
|---|---|
| Dokploy | ≥ 0.6 (self-hosted) |
| Docker registry | Any OCI-compatible registry (GHCR, Docker Hub, etc.) |
| PostgreSQL | 17 (managed or Dokploy service) |
| Redis | 7 (optional for this milestone; required for locks in later milestones) |

---

## 2. Production Runtime Topology (api, worker, migrate)

A production deployment of arena_new consists of **three distinct runtime roles**,
all built from the same Docker image but invoked with different entrypoints. The
diagram below summarises the topology Dokploy should reproduce:

```
                  ┌────────────────────────┐
                  │  arena-migrate         │  (one-shot, runs first)
                  │  /app/arena-migrate up │
                  └───────────┬────────────┘
                              │ goose migrations applied
                              ▼
   ┌──────────────────────────┐      ┌──────────────────────────┐
   │  arena-api               │      │  arena-worker            │
   │  /app/arena-api          │      │  /app/arena-worker       │
   │  Public port: 8080       │      │  Internal metrics: 9091  │
   │  GET /healthz, /readyz   │      │  GET /healthz, /metrics  │
   │  GET /metrics            │      │  Polls worker_jobs,      │
   │                          │      │  dispatches outbox       │
   └──────────────┬───────────┘      └──────────────┬───────────┘
                  │                                 │
                  └─────────────► PostgreSQL ◄──────┘
                              (single source of truth)
```

### 2.1 `arena-api` service (public HTTP)

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running) |
| Docker image | `your-registry/arena-api:<tag>` (built from repo root `Dockerfile`) |
| Entrypoint | `/app/arena-api` (default `CMD` of the image) |
| Listen port | `8080` (`HTTP_LISTEN_ADDR=:8080`) |
| Liveness probe | `GET /healthz` → `200 OK` |
| Readiness probe | `GET /readyz` → `200 OK` once the pgx pool can `Ping` the DB |
| Metrics scrape | `GET /metrics` (Prometheus exposition on `:8080`) |
| Start policy | `Always` |
| Replicas | ≥ 1 (horizontally scalable behind Dokploy's Traefik router) |

Dokploy must expose `8080` as the public HTTP port and route the application
domain to it. Healthcheck is wired by the image's `HEALTHCHECK` directive (see
§7).

### 2.2 `arena-worker` service (background jobs + outbox)

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running, internal only) |
| Docker image | **Same image** as `arena-api` |
| Entrypoint | `/app/arena-worker` (override the image `CMD`) |
| Internal listen | `WORKER_METRICS_ADDR=:9091` (sidecar HTTP for `/healthz` and `/metrics`) |
| Public port | **None** — do not expose `:9091` to the public router |
| Liveness probe | `GET http://<container>:9091/healthz` → `200 OK` |
| Metrics scrape | `GET http://<container>:9091/metrics` (Prometheus) |
| Start policy | `Always` |
| Replicas | 1 by default; the queue uses `FOR UPDATE SKIP LOCKED`, so additional replicas scale safely |

The worker is **mandatory** in production: it drives the platform's job queue
(`worker_jobs`) and dispatches domain events from the `outbox` table. Without a
running worker, side effects (delivery, billing notifications, reconciliation,
scheduled reports, retried webhooks, etc.) accumulate in PostgreSQL and never
fire. Treat it as a first-class production component, not an optional sidecar.

### 2.3 `arena-migrate` one-shot (pre-deploy)

| Property | Value |
|---|---|
| Dokploy service type | **Application** with one-shot / "Run on deploy" start policy |
| Docker image | **Same image** as `arena-api` |
| Entrypoint | `/app/arena-migrate up` |
| Listen port | None (the binary exits when migrations are applied) |
| Start policy | `On Deploy` / `One-shot` (must **not** be `Always`) |
| Deploy order | Runs **before** `arena-api` and `arena-worker` on every deploy |

`arena-migrate` runs the embedded `goose` migrations (`embed.FS`) against
`DATABASE_URL` and exits `0` on success. If it fails, the deploy must abort —
the API and worker rely on the new schema being in place before they boot.

### 2.4 Env passthrough checklist (all three services)

The three services share configuration loaded from the same `config.Load()`
path. The variables below **must be set identically** on `arena-api`,
`arena-worker`, and `arena-migrate` (Dokploy "Shared Environment" or copy-paste
into each service's env tab):

| Variable | Required by | Notes |
|---|---|---|
| `APP_ENV` | api / worker / migrate | `production` |
| `DATABASE_URL` | api / worker / migrate | Must point to the same PostgreSQL instance |
| `LOG_LEVEL` | api / worker / migrate | `info` |
| `LOG_FORMAT` | api / worker / migrate | `json` |
| `APP_NAME` | api / worker | `arena-api` / `arena-worker` respectively |
| `APP_VERSION` | api / worker / migrate | Injected by CI |
| `APP_COMMIT` | api / worker / migrate | Injected by CI |
| `JWT_SIGNING_SECRET` | api (mandatory), worker (recommended) | Worker shares the symmetric secret for inter-service signed callbacks |
| `ENABLE_DEV_AUTH` | api / worker | `false` |
| `HTTP_LISTEN_ADDR` | api only | `:8080` |
| `WORKER_METRICS_ADDR` | worker only | `:9091` (default; do not expose publicly) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api / worker | Same collector endpoint |
| `OTEL_SERVICE_NAME` | api / worker | `arena-api` / `arena-worker` respectively |
| `REDIS_URL` | api / worker | Required once lock / hot-cache features land |
| `DB_POOL_MIN_CONNS` / `DB_POOL_MAX_CONNS` | api / worker | Tune pools independently — total connections across api + worker must stay below the PostgreSQL `max_connections` limit |
| `SHUTDOWN_TIMEOUT` | api / worker | `20s` recommended |

> Tip: in Dokploy ≥ 0.6 you can attach a **Shared Environment** group to all
> three services and only override the service-specific entries (`APP_NAME`,
> `HTTP_LISTEN_ADDR`, `OTEL_SERVICE_NAME`, `WORKER_METRICS_ADDR`).

---

## 3. Create the Application in Dokploy

1. Log in to your Dokploy dashboard.
2. Navigate to **Projects** → **New Project** (or open an existing project).
3. Click **+ Add Service** → **Application**.
4. Set the **Application Name** to `arena-api` (or your preferred name).
5. Under **Source**, select **Git Repository** and enter your repository URL.
   - Branch: `main` (or your production branch).
6. Under **Build**, select **Dockerfile** and set the **Dockerfile path** to:
   ```
   Dockerfile
   ```
   *(The `Dockerfile` is at the repository root — no subdirectory needed.)*
7. Leave **Build context** as the repository root (`.`).
8. Click **Save**.

---

## 4. Attach a Managed PostgreSQL Service

### Option A — Dokploy Managed Database (recommended)

1. In your Dokploy project, click **+ Add Service** → **Database** → **PostgreSQL 17**.
2. Set a strong password. Copy the generated **Internal Connection String** — you
   will need it for `DATABASE_URL` in §4.
3. Once the database service is `Running`, connect it to your `arena-api`
   application via **Service Links** (or set `DATABASE_URL` manually).

### Option B — External PostgreSQL

Set `DATABASE_URL` directly to your external DSN (e.g., a managed cloud database).

---

## 5. Configure Environment Variables

In Dokploy, open your `arena-api` application → **Environment Variables** tab.
Set each variable below.  A full list of *optional* tuning variables is in
[`.env.example`](../.env.example) at the repository root.

### Mandatory Production Variables

| Variable | Production value | Notes |
|---|---|---|
| `APP_ENV` | `production` | Enables production-mode code paths |
| `DATABASE_URL` | `postgres://user:pass@host:5432/dbname?sslmode=require` | pgx DSN; **must** use `sslmode=require` or `verify-full` in production |
| `JWT_SIGNING_SECRET` | *(strong random secret, ≥ 32 bytes)* | HS256 signing key for dev-stub JWT. **Replace** with RS256 asymmetric key when the real identity module ships |
| `ENABLE_DEV_AUTH` | `false` | **Must be `false` in production.** The dev-stub identity provider must be disabled |
| `LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | Production **must** use `json` for log aggregator compatibility |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `host:4317` | OTLP gRPC endpoint for traces; leave empty to disable |

### Recommended Tuning Variables

| Variable | Recommended value | Notes |
|---|---|---|
| `APP_NAME` | `arena-api` | Appears in logs and traces |
| `APP_VERSION` | *(set by CI, e.g. `1.0.0`)* | Semver for observability dashboards |
| `APP_COMMIT` | *(set by CI, e.g. `abc1234`)* | Git SHA for alerting correlation |
| `HTTP_LISTEN_ADDR` | `:8080` | Keep default; Dokploy routes to this port |
| `CORS_ALLOWED_ORIGINS` | `https://your-frontend.example.com` | Restrict CORS in production |
| `DB_POOL_MIN_CONNS` | `2` | Minimum idle connections in pgx pool |
| `DB_POOL_MAX_CONNS` | `20` | Maximum connections; tune to DB tier limits |
| `REDIS_URL` | `redis://redis:6379/0` | Required when lock / hot-cache features ship |
| `SHUTDOWN_TIMEOUT` | `20s` | Graceful shutdown window |
| `OTEL_SERVICE_NAME` | `arena-api` | Service name in traces |
| `OTEL_TRACES_SAMPLER_ARG` | `0.1` | 10 % sampling; increase for debugging |

---

## 6. Run Database Migrations

**Migrations must run before `arena-api` starts.**  The `arena-migrate` binary
is embedded in the same Docker image as `arena-api`, but it must be invoked
separately — it is a one-shot command, not a daemon.

### Recommended: Pre-deploy Job in Dokploy

1. In your Dokploy project, create a second **Application** named `arena-migrate`.
2. Use the **same Docker image** (same repository, same `Dockerfile`).
3. Override the **Entrypoint** to `/app/arena-migrate up`.
4. Set **the same environment variables** as `arena-api` (at minimum `DATABASE_URL`
   and `APP_ENV`).
5. Set the **Start Policy** to `On Deploy` / one-shot (not `Always`).
6. In **Deploy Order**, place `arena-migrate` **before** `arena-api`.

Dokploy will then run migrations automatically before the API starts on every
deployment.

### Alternative: init-container pattern

If your Dokploy version or hosting setup supports init-containers, you can add
`arena-migrate up` as an init step that blocks the `arena-api` container from
starting until migrations succeed.

### Manual one-off execution

```bash
# From within the Dokploy host (or via `dokploy exec`):
docker run --rm \
  --env DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=require" \
  --env APP_ENV=production \
  --env ENABLE_DEV_AUTH=false \
  --env LOG_LEVEL=info \
  --entrypoint /app/arena-migrate \
  your-registry/arena-api:latest up
```

**Rollback:**

```bash
# Roll back the last migration:
docker run --rm ... --entrypoint /app/arena-migrate your-registry/arena-api:latest down 1
```

---

## 7. Healthcheck & Port Configuration

The image already contains a `HEALTHCHECK` directive that Dokploy honours
automatically:

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/arena-healthcheck"]
```

`arena-healthcheck` is a tiny static Go binary that performs
`GET http://localhost:8080/healthz` and exits 0 on HTTP 200.

| Endpoint | Purpose | Expected response |
|---|---|---|
| `GET /healthz` | Liveness — process alive | `200 OK` |
| `GET /readyz`  | Readiness — DB reachable | `200 OK` (or `503` during startup) |

**Dokploy port configuration:**

- **Expose port:** `8080` (already set via `EXPOSE 8080` in the `Dockerfile`)
- **Healthcheck path:** `/healthz`
- **Protocol:** HTTP

Dokploy will automatically forward traffic to port 8080 and mark the container
unhealthy if `/healthz` fails three consecutive checks.

---

## 8. Deploy

1. In the Dokploy dashboard, open the `arena-api` application.
2. Click **Deploy** (or push to `main` if auto-deploy is configured).
3. Monitor the **Logs** tab — look for:
   ```
   {"level":"INFO","msg":"server started","addr":":8080"}
   ```
4. Once the container is `Healthy`, verify:
   ```bash
   curl https://your-domain.example.com/healthz
   # → 200 OK  {"status":"ok"}

   curl https://your-domain.example.com/readyz
   # → 200 OK  {"status":"ok"}
   ```

---

## 9. Post-Deploy Smoke Checks

Run these checks **immediately after every production deploy**, before
announcing the deploy as successful. They cover all three runtime roles.

### 9.1 `arena-migrate` finished cleanly

```bash
# Dokploy → arena-migrate → Logs (last run):
# Expect a final line similar to:
#   {"level":"INFO","msg":"migrations applied","applied":N}
# Container exit code must be 0; if it is non-zero, abort the deploy and
# investigate before bringing arena-api up against an inconsistent schema.
```

### 9.2 `arena-api` liveness, readiness, and version

```bash
# Liveness
curl -fsS https://your-domain.example.com/healthz
# → 200 {"status":"ok"}

# Readiness (also asserts pgx can reach PostgreSQL)
curl -fsS https://your-domain.example.com/readyz
# → 200 {"status":"ok"}

# Confirm the deployed build matches CI (APP_VERSION / APP_COMMIT)
curl -fsS https://your-domain.example.com/healthz | jq .
```

### 9.3 `arena-worker` liveness and metrics

The worker has no public route; check it from the Dokploy host or via
`dokploy exec` into any container on the same internal network:

```bash
# Liveness (sidecar HTTP on WORKER_METRICS_ADDR, default :9091)
curl -fsS http://arena-worker:9091/healthz
# → 200 {"status":"ok"}

# Confirm Prometheus exposition is live
curl -fsS http://arena-worker:9091/metrics | head -n 20
# → expect arena_outbox_backlog, arena_worker_jobs_*, process_* gauges
```

### 9.4 Outbox / job queue is draining

```bash
# From a DB shell or psql connected via DATABASE_URL:
SELECT count(*) FROM outbox WHERE dispatched_at IS NULL;
-- → expect a small, decreasing number; a steadily growing value means
--    arena-worker is not running or is stuck.

SELECT status, count(*) FROM worker_jobs GROUP BY status;
-- → expect mostly 'done'; investigate any 'failed' rows.
```

### 9.5 Logs sanity

In Dokploy **Logs** for both `arena-api` and `arena-worker`, verify:

- Log lines are valid JSON (`LOG_FORMAT=json`).
- No repeated `ERROR` entries.
- `arena-api` shows `{"msg":"server started","addr":":8080"}`.
- `arena-worker` shows `{"msg":"arena-worker metrics server listening","addr":":9091"}` and periodic `{"msg":"outbox backlog","count":...}` ticks.

If any of the above fails, mark the deploy as failed in Dokploy and roll back
to the previous successful revision before debugging.

---

## 10. Production Environment Variable Checklist

Use this checklist before every first deploy and whenever environment variables
change.  See [`.env.example`](../.env.example) for full documentation of every
variable.

### ✅ Security-Critical (must not use dev defaults)

- [ ] `APP_ENV=production`
- [ ] `ENABLE_DEV_AUTH=false` — dev JWT stub **must** be disabled
- [ ] `JWT_SIGNING_SECRET` — set to a strong random secret (≥ 32 bytes, not the
  dev placeholder `dev-only-do-not-use-in-prod`)
- [ ] `DATABASE_URL` — uses `sslmode=require` or `sslmode=verify-full`
- [ ] `CORS_ALLOWED_ORIGINS` — restricted to your frontend domain(s), not `*`

### ✅ Observability

- [ ] `LOG_LEVEL=info` (or `warn` in high-traffic production)
- [ ] `LOG_FORMAT=json`
- [ ] `OTEL_EXPORTER_OTLP_ENDPOINT` — set if using distributed tracing
- [ ] `OTEL_SERVICE_NAME=arena-api`
- [ ] `APP_VERSION` — set by CI to the deployed semver
- [ ] `APP_COMMIT` — set by CI to the deployed git SHA

### ✅ Database

- [ ] `DATABASE_URL` — correct host, port, database name, credentials
- [ ] `DB_POOL_MAX_CONNS` — tuned to PostgreSQL connection limit
- [ ] Migrations have been applied via `arena-migrate up` before the first start

### ✅ Optional / Future Milestones

- [ ] `REDIS_URL` — required once lock / hot-cache features land (next milestone)
- [ ] `OTEL_TRACES_SAMPLER_ARG` — tune sampling rate for your traffic volume

---

## 11. Secret Rotation

> **Note:** A formal secret-rotation procedure is **out of scope for this
> (Backend Foundation) milestone.**  The following section documents *where*
> rotation is expected to be implemented in subsequent milestones.

### Current state (milestone 1)

- `JWT_SIGNING_SECRET` is used by the dev-stub HS256 identity provider.
  It is a single symmetric secret; rotation requires a restart.
- Database credentials are embedded in `DATABASE_URL`.
- No other secrets are used in this milestone.

### Expected in subsequent milestones

| Secret | Expected rotation mechanism | Milestone |
|---|---|---|
| JWT signing key | Replace HS256 stub with RS256 asymmetric keys; support key-id rotation without restart (JWKS endpoint) | Identity module milestone |
| Database password | Dokploy / Vault dynamic credentials; zero-downtime via pgBouncer connection pooling | Infrastructure hardening milestone |
| Redis AUTH token | Dokploy secret injection; no application restart required (reconnect on auth failure) | Infrastructure hardening milestone |
| OTLP / observability API keys | Injected via Dokploy secrets; rotated out-of-band | Infrastructure hardening milestone |

Until those milestones ship, rotate secrets by:

1. Generating the new secret value.
2. Updating the Dokploy **Environment Variables** for the application.
3. Triggering a redeploy (new containers pick up the new secret on start).

---

## 12. Troubleshooting

### Container exits immediately after start

Check the Dokploy **Logs** tab for a config-validation error.  Common causes:

- `DATABASE_URL` is empty or malformed.
- `ENABLE_DEV_AUTH=true` but `JWT_SIGNING_SECRET` is not set.
- `APP_ENV` is not one of `development`, `staging`, `production`.

### `/readyz` returns 503

The API is alive but cannot reach PostgreSQL.  Verify:

- `DATABASE_URL` is correct (host, port, credentials, database name).
- The PostgreSQL service is running and accepting connections.
- Network policies / firewall rules allow the container to reach the DB port.

### Migrations fail to run

- Ensure `DATABASE_URL` in the `arena-migrate` job matches the production DB.
- Check that the DB user has `CREATE TABLE`, `ALTER TABLE`, and `CREATE INDEX`
  privileges (needed by goose).
- Check for migration version conflicts: run
  `arena-migrate status` to see which migrations have been applied.

### `arena-worker` is running but jobs / outbox are not draining

- Check that `arena-worker` is actually started in Dokploy (not just defined).
  Its sidecar `:9091/healthz` must return `200`.
- Confirm `DATABASE_URL` points to the **same** PostgreSQL instance as
  `arena-api`. A mismatched DSN means the worker is polling an empty queue.
- Grep `arena-worker` logs for `handler not registered` — unknown job types
  are parked as `failed` until a handler ships.
- `SELECT count(*) FROM outbox WHERE dispatched_at IS NULL;` should trend
  toward zero. A monotonically increasing value indicates the worker process
  is crashed, stuck, or pointing at a different DB.

### High DB connection count

Reduce `DB_POOL_MAX_CONNS` or add a PgBouncer connection pooler in front of
PostgreSQL.  The current default of `20` connections assumes a single replica.
