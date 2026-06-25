# Arena Production Environment Hardening Checklist

**Document owner:** Backend platform / SRE
**Status:** Signed — gating checklist for promoting a build to a public-facing
production environment
**Last reconciled:** 2026-06-25 (feature #193)
**Tracking feature:** AutoForge #193 "Add production environment hardening checklist"
**Companion documents:**
- `00_project_control/RELEASE_CHECKLIST.md` — four-gate release-readiness
  contract (architecture, generated clients, tests + lint, migrations,
  container build). This document is the **fifth, environment-side gate**:
  even when all release-readiness gates are green, a deployment may not
  be promoted to production until every item below is verified on the
  *target environment*.
- `deploy/DOKPLOY.md` §10 — short-form Dokploy variable list. This
  document is the long-form authoritative version; §10 of DOKPLOY.md is
  kept as the quick operator reference and points here.
- `.env.example` — documented defaults; **every entry below is a
  production override of a dev default that ships in `.env.example`**.

## How to use this checklist

1. Run through every section in order on the **target deployment environment**
   (Dokploy app, Docker host, Kubernetes namespace — whichever applies).
2. Verify each box. Where a box has a `verify:` command, run it against the
   live container/service and paste the output into the deploy ticket.
3. Sign the table at the bottom. A deploy is **not** considered hardened
   until the signature row is filled in.
4. Re-run this checklist after every change to environment variables,
   compose files, Dokploy app config, reverse-proxy/firewall rules, or
   Grafana credentials.

> A `dev default` in this document means any value shipped in
> `.env.example`, `docker-compose.yml`, or `Makefile` that is acceptable
> for `APP_ENV=development` but **MUST NOT** reach a production deploy.

---

## Gate A — Application identity & auth

- [ ] **`APP_ENV=production`**
      - Asserted by the runtime: the API and worker log this on boot under
        the `app_env` field (`apps/backend/cmd/arena-api`, `arena-worker`).
      - Why it matters: gates dev-only code paths (verbose error bodies,
        `ENABLE_DEV_AUTH`) and is the canonical environment tag in metrics
        and traces.
      - **verify:** `docker exec arena_api printenv APP_ENV` → `production`
- [ ] **`ENABLE_DEV_AUTH=false`**
      - The dev-stub IdentityProvider (HS256, anonymous `dev-only-...` key)
        **must** be disabled. Production swaps to the real IdP via the
        same AuthBoundary (see `08_architecture/14_current_implementation_overview_ru.md`).
      - Why it matters: with `ENABLE_DEV_AUTH=true`, any caller can mint
        a JWT against the dev secret and impersonate any user.
      - **verify:** `docker exec arena_api printenv ENABLE_DEV_AUTH` → `false`
- [ ] **`JWT_SIGNING_SECRET` is strong and unique**
      - Must NOT equal the dev placeholder `dev-only-do-not-use-in-prod`.
      - Minimum 32 bytes of cryptographic randomness, base64- or
        hex-encoded. Generate with:
        `openssl rand -base64 48` or `head -c 48 /dev/urandom | base64`.
      - Rotation procedure: see `deploy/DOKPLOY.md` §11.
      - **verify:** `docker exec arena_api sh -c 'test "$JWT_SIGNING_SECRET" != "dev-only-do-not-use-in-prod" && echo OK'` → `OK`
      - **verify length:** `docker exec arena_api sh -c 'printf "%s" "$JWT_SIGNING_SECRET" | wc -c'` → ≥ 32

## Gate B — Database transport security

- [ ] **`DATABASE_URL` uses `sslmode=require` or `sslmode=verify-full`**
      - The dev DSN ships with `sslmode=disable` (acceptable only for the
        sidecar `postgres` service inside a local compose network).
      - Production: prefer `sslmode=verify-full` against a managed
        PostgreSQL 17 instance with a pinned CA bundle. `sslmode=require`
        is the absolute floor and only acceptable when the DB is reached
        over a trusted private network.
      - Why it matters: without TLS, credentials and per-tenant data
        traverse the network in cleartext.
      - **verify:** `docker exec arena_api sh -c 'echo "$DATABASE_URL" | grep -Eo "sslmode=[a-z-]+"'` → `sslmode=require` or `sslmode=verify-full`
      - **verify TLS in use:** at the database server, confirm the most
        recent connection from the API IP shows `ssl=on` in
        `pg_stat_ssl`.

## Gate C — HTTP exposure

- [ ] **`CORS_ALLOWED_ORIGINS` is an explicit allowlist (not `*`)**
      - Set to the comma-separated list of fully qualified frontend
        origins, e.g. `https://app.arena.example,https://admin.arena.example`.
      - Never use `*` in production; the API exposes per-tenant data and
        accepts credentials.
      - Why it matters: `*` plus credentialed requests defeats the
        cross-origin protection browsers rely on.
      - **verify:** `docker exec arena_api printenv CORS_ALLOWED_ORIGINS` →
        contains only `https://…` entries, no `*`, no `http://localhost`.
- [ ] **`HTTP_LISTEN_ADDR` is bound behind a reverse proxy**
      - The container listens on `:8080`; the proxy (Dokploy Traefik / nginx)
        terminates TLS, enforces HSTS, and forwards `X-Forwarded-*` headers.
      - Do not publish container port 8080 directly to the internet.
- [ ] **`BODY_LIMIT_BYTES` and `REQUEST_TIMEOUT_SECONDS` left at safe defaults**
      - Defaults (1 MiB, 30 s) are appropriate. If overridden, document the
        new value in the deploy ticket.

## Gate D — Logging & runtime diagnostics

- [ ] **`LOG_FORMAT=json`**
      - Required for structured ingestion (Loki / ELK / Datadog). The
        `text` handler is dev-only.
      - **verify:** `docker exec arena_api printenv LOG_FORMAT` → `json`
- [ ] **`LOG_LEVEL=info` (or `warn` for high-traffic services)**
      - Never `debug` in production — debug-level logs may include user
        identifiers, request bodies, and SQL parameters.
      - **verify:** `docker exec arena_api printenv LOG_LEVEL` → `info` or `warn`
- [ ] **`DB_LOG_QUERIES=false`**
      - The query log emits every SQL statement (including parameters)
        through the slog handler. In production this is both a
        performance regression and a PII exposure risk.
      - **verify:** `docker exec arena_api printenv DB_LOG_QUERIES` → `false` (or unset; default is `false`)
      - **verify worker:** `docker exec arena_worker printenv DB_LOG_QUERIES` → `false`
- [ ] **`APP_VERSION` and `APP_COMMIT` are populated by CI**
      - Not the literal strings `0.1.0-dev` / `local`. These tags are
        required to correlate alerts and traces back to a deployed SHA.
      - **verify:** `curl -sf $API_URL/healthz | jq '.version, .commit'` →
        non-default values.

## Gate E — Observability stack isolation

The compose file ships Postgres, Redis, Prometheus, Grafana, and the worker's
metrics endpoint with **published host ports** that are convenient for local
development but unsafe for production exposure.

- [ ] **Grafana admin credentials rotated off `admin / admin`**
      - In `docker-compose.yml`, `GF_SECURITY_ADMIN_USER` and
        `GF_SECURITY_ADMIN_PASSWORD` default to `admin / admin`. In every
        non-local environment these must be overridden via Dokploy
        environment variables (or a separate compose override file).
        Recommended: `GF_SECURITY_ADMIN_USER=arena-ops`,
        `GF_SECURITY_ADMIN_PASSWORD` from a 24+ char generated secret.
      - **verify:** `docker exec arena_grafana printenv GF_SECURITY_ADMIN_PASSWORD` → not `admin`, ≥ 16 chars
      - **verify GUI:** logging in with `admin / admin` at `:3000` fails.
- [ ] **PostgreSQL is not published to the public network**
      - `docker-compose.yml` publishes `55432:5432` for local tooling. In
        production, remove that `ports:` entry (use the compose override
        pattern or the Dokploy "internal service" setting) so the database
        is reachable only from `arena-api` and `arena-worker` on the
        internal Docker network.
      - **verify (host-side):** `ss -tlnp | grep ':55432'` → no output
      - **verify (external):** from outside the host, `nc -vz $PUBLIC_IP 55432` → connection refused / timeout
- [ ] **Redis is not published to the public network**
      - Same treatment as Postgres: compose publishes `56379:6379`; strip
        the `ports:` entry in production.
      - **verify (host-side):** `ss -tlnp | grep ':56379'` → no output
- [ ] **Prometheus (`:9090`) is not on the public internet**
      - Prometheus' query API has no authentication. Either remove the
        published port (recommended; scrape internally only) or front it
        with an authenticated reverse proxy bound to the ops VPN.
      - **verify (external):** `curl -m 5 http://$PUBLIC_IP:9090/-/healthy`
        → connection refused, timeout, or 401 from the auth proxy.
- [ ] **Worker metrics endpoint (`:9091`) is not on the public internet**
      - `WORKER_METRICS_ADDR=:9091` exposes the worker's `/metrics` and
        `/healthz`. These are scraped by Prometheus on the internal
        network and must not be reachable from the public internet.
      - **verify (external):** `curl -m 5 http://$PUBLIC_IP:9091/metrics`
        → connection refused or timeout.
- [ ] **Grafana (`:3000`) is access-controlled**
      - Either keep `:3000` off the public internet (ops VPN only) or
        front it with an authenticated reverse proxy. The default
        password rotation above is a defence-in-depth measure, not a
        substitute for network isolation.
      - **verify:** `curl -m 5 http://$PUBLIC_IP:3000/api/health` →
        reachable **only** from authorized networks.
- [ ] **API `/metrics` is scraped privately**
      - The arena-api `/metrics` endpoint is unauthenticated by design
        (Prometheus scraping). It must be reachable from Prometheus on
        the internal network and **not** through the public reverse
        proxy. Either the proxy strips `/metrics` from the public route,
        or Prometheus scrapes the container directly on the Docker
        network.
      - **verify (external):** `curl -m 5 https://$PUBLIC_FQDN/metrics`
        → 404 from the proxy.

## Gate F — Worker & background jobs

- [ ] **Worker connects to the same hardened `DATABASE_URL`** (sslmode applies)
- [ ] **`WORKER_CONCURRENCY` and timeouts are tuned for production load**
- [ ] **Outbox dispatcher is running** (look for `outbox_dispatcher_loop_started` in worker logs)

## Gate G — Post-deploy runtime verification

Run **inside the live containers** after promotion, not against a local copy
of the env file. Configuration drift between the deploy pipeline and the
running container is the most common source of "the checklist passed but the
service is unsafe" incidents.

- [ ] **`docker exec arena_api env | sort`** captured to the deploy ticket
      - Sanity-check that every variable in Gates A–E matches the expected
        value. Anything still set to a value from `.env.example` is a
        finding.
- [ ] **`docker exec arena_worker env | sort`** captured to the deploy ticket
      - The worker uses `DATABASE_URL`, `LOG_FORMAT`, `LOG_LEVEL`,
        `DB_LOG_QUERIES`, `WORKER_*`, `OUTBOX_*` — verify each.
- [ ] **`curl -fsS $API_URL/healthz`** returns 200 with `status: ok`
- [ ] **`curl -fsS $API_URL/readyz`** returns 200 (DB ping included)
- [ ] **`curl -fsS $API_URL/healthz | jq .`** shows non-dev `version` and
      `commit` (matches the deployed CI build)
- [ ] **`docker compose ps`** shows `api`, `worker`, and (if used)
      `prometheus`, `grafana` healthy; one-shot `migrate` exited 0
- [ ] **Application logs are JSON**:
      `docker logs --tail=20 arena_api | jq -e .level >/dev/null && echo OK`
- [ ] **No `dev-only-do-not-use-in-prod` string** appears in container env:
      `docker exec arena_api sh -c 'env | grep -F dev-only-do-not-use-in-prod && exit 1 || echo OK'`
- [ ] **Network exposure audit** completed: from a host *outside* the
      production network, attempts to reach Postgres (`55432`), Redis
      (`56379`), Prometheus (`9090`), worker metrics (`9091`), and
      unauthenticated Grafana login all fail.

---

## Cross-reference: every item maps back to feature #193 acceptance steps

| Feature #193 step | Gate |
|---|---|
| `APP_ENV=production` | A |
| `ENABLE_DEV_AUTH=false` | A |
| `JWT_SIGNING_SECRET` strong, not `dev-only-do-not-use-in-prod` | A |
| `DATABASE_URL` with `sslmode=require` or `verify-full` | B |
| `CORS_ALLOWED_ORIGINS` not `*` | C |
| `LOG_FORMAT=json` | D |
| `DB_LOG_QUERIES=false` | D |
| Grafana password not `admin` | E |
| Postgres / Redis / worker metrics not published publicly | E |
| Runtime env verified inside containers post-deploy | G |

---

## Sign-off

Filling this row is the gate that promotes a build to production.

| Role | Name | Date | Signature / commit SHA |
|---|---|---|---|
| Deploy engineer | _(fill in)_ | _(fill in)_ | _(fill in)_ |
| Backend platform reviewer | _(fill in)_ | _(fill in)_ | _(fill in)_ |
| SRE on-call | _(fill in)_ | _(fill in)_ | _(fill in)_ |

A failed item is **not** a soft warning. If any box in Gates A–G cannot be
ticked, the deploy must be rolled back or held until the finding is fixed
and this checklist re-run end-to-end.
