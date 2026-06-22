# Deploying arena_new to Dokploy

> **Milestone scope:** This guide covers the **Backend Foundation Milestone**.
> Business-logic modules (identity, catalog, payments, etc.) are added in
> subsequent milestones; deployment steps will grow accordingly.

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Create the Application in Dokploy](#2-create-the-application-in-dokploy)
3. [Attach a Managed PostgreSQL Service](#3-attach-a-managed-postgresql-service)
4. [Configure Environment Variables](#4-configure-environment-variables)
5. [Run Database Migrations](#5-run-database-migrations)
6. [Healthcheck & Port Configuration](#6-healthcheck--port-configuration)
7. [Deploy](#7-deploy)
8. [Production Environment Variable Checklist](#8-production-environment-variable-checklist)
9. [Secret Rotation](#9-secret-rotation)
10. [Troubleshooting](#10-troubleshooting)

---

## 1. Prerequisites

| Requirement | Version |
|---|---|
| Dokploy | ≥ 0.6 (self-hosted) |
| Docker registry | Any OCI-compatible registry (GHCR, Docker Hub, etc.) |
| PostgreSQL | 17 (managed or Dokploy service) |
| Redis | 7 (optional for this milestone; required for locks in later milestones) |

---

## 2. Create the Application in Dokploy

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

## 3. Attach a Managed PostgreSQL Service

### Option A — Dokploy Managed Database (recommended)

1. In your Dokploy project, click **+ Add Service** → **Database** → **PostgreSQL 17**.
2. Set a strong password. Copy the generated **Internal Connection String** — you
   will need it for `DATABASE_URL` in §4.
3. Once the database service is `Running`, connect it to your `arena-api`
   application via **Service Links** (or set `DATABASE_URL` manually).

### Option B — External PostgreSQL

Set `DATABASE_URL` directly to your external DSN (e.g., a managed cloud database).

---

## 4. Configure Environment Variables

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

## 5. Run Database Migrations

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

## 6. Healthcheck & Port Configuration

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

## 7. Deploy

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

## 8. Production Environment Variable Checklist

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

## 9. Secret Rotation

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

## 10. Troubleshooting

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

### High DB connection count

Reduce `DB_POOL_MAX_CONNS` or add a PgBouncer connection pooler in front of
PostgreSQL.  The current default of `20` connections assumes a single replica.
