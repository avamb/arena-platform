# arena_new — Load Test Baseline (k6)

## Overview

k6 load test scripts for the latency-critical paths identified in the
architecture decision log ([arch: Q8 — hundreds of tickets/day target]).

| Script | Scenario | VU default | Auth method |
|--------|----------|------------|-------------|
| `auth-login.js` | Production login + GET /v1/me | 5 | Real POST /v1/auth/login |
| `scanner.js` | Scanner barcode validate (POST /v1/scan) | 20 | Dev-stub JWT |
| `feed.js` | Public feed read (GET /v1/feeds/{token}) | 50 | None (public) |
| `checkout.js` | Checkout end-to-end (start → confirm → complete) | 10 | Dev-stub JWT |

`auth-login.js` is the **required production-auth scenario** (PR-11). Every CI
run of the load-test workflow executes it first because it validates the real
bcrypt + JWT issuance path that all other users depend on.

---

## Sales-path suite — both entry points (local stand)

Added 2026-09-13. Exercises the whole sale on one on-sale event through both
ways arena sells tickets, and checks inventory correctness afterwards.

| File | What it does |
|------|--------------|
| `provision.mjs` | Local-only fixtures: channel with a short hold TTL, gateway credential (fid/token), import API key, three events (`flow` 20k+2k GA, `race` 10 GA, `expiry` 20 GA), public feed token with the events published, org JWT. Writes `results/fixtures.local.json` (gitignored, holds local secrets). |
| `gateway.js` | The Bil24-compatible gateway (`/compat/bil24/json`) the migrated WordPress sites use. `SCENARIO=flow` (browsers + buyers + abandoned carts, polls tickets), `race` (N buyers for the last tickets), `expiry` (abandoned holds must return to sale after the TTL). |
| `native.js` | arena's own public API used by the widget: feed → `checkout/start` → payment intent + webhook (`processing`, `succeeded`) → public checkout status. `SCENARIO=flow` or `race`. |
| `sql/audit.sql` | Read-only inventory audit for one session: ledger vs units vs tickets, double-sold units, expired holds never released. Every "violations" column must be 0. |
| `quota_admin.js` | One operator VU that edits GA category quotas (plan 08_architecture/23) WHILE `flow` sells tickets on the same session: swings the VIP quantity ±`SWING` every `TICK_SECONDS`, closes/reopens VIP every `CLOSE_EVERY` ticks for `CLOSE_FOR_SECONDS`, adds a "Late release" category once at `ADD_AT_SECONDS`. Run it next to a `flow` scenario (`gateway.js` or `native.js`), against the same `results/fixtures.local.json`. Success = `qa_errors` == 0, `qa_patch_unexpected` == 0 (only 200 or the documented 409 `tier.quantity_below_used` are expected), and the category invariant query (below) holds afterwards. |
| `bil24/docker-compose.loadtest.yml` | Compose override: mounts the gateway, turns SQL query logging off. |

Defaults model the agreed peak: **200 concurrent visitors, 50 orders/min**, 5 minutes.

```bash
# 1. Stand with the gateway mounted (rebuild the image when the code changed)
docker compose -f docker-compose.yml -f ops/loadtest/bil24/docker-compose.loadtest.yml up -d --build api worker

# 2. Fixtures (re-run before each race/expiry run: those pools are consumed)
node ops/loadtest/provision.mjs

# 3. Scenarios — k6 runs from the official image, results land in ops/loadtest/results/
#    (on Git Bash prefix with MSYS_NO_PATHCONV=1 and use a C:/... path for -v)
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=flow   grafana/k6:0.54.0 run /lt/gateway.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=race   grafana/k6:0.54.0 run /lt/gateway.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=expiry grafana/k6:0.54.0 run /lt/gateway.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=flow   grafana/k6:0.54.0 run /lt/native.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=race   grafana/k6:0.54.0 run /lt/native.js

# 3b. Operator swinging GA category quotas on the flow session, run alongside 3
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e DURATION=3m grafana/k6:0.54.0 run /lt/quota_admin.js

# 4. Audit a session afterwards (session ids are in results/fixtures.local.json)
docker exec -i arena_postgres psql -U arena -d arena -v session_id=<uuid> < ops/loadtest/sql/audit.sql
```

Knobs: `BROWSERS`, `ORDERS_PER_MIN`, `ABANDON_PER_MIN`, `DURATION`, `RACERS`,
`TICKET_POLL_SECONDS`, `EXPIRY_GRACE_SECONDS`, `SHARED_IP=1` (native: all
visitors from one IP), `SHARED_BUYERS=N` and `PAY_DELAY_SECONDS=N` (gateway:
fold buyers onto N shared email/phone identities and wait before paying, so
orders of one customer overlap; expect `gw_open_order_refused` > 0 and no
failed payments, the journey threshold fails by design), `DEBUG=1` (log every
failed call). Provisioning:
`FLOW_POOL`, `RACE_POOL`, `EXPIRY_POOL`, `RESERVATION_TTL`. `quota_admin.js`:
`TICK_SECONDS` (default 2), `CLOSE_EVERY` ticks (default 10),
`CLOSE_FOR_SECONDS` (default 4), `ADD_AT_SECONDS` (default 30), `SWING`
(default 150, the +/- quantity delta around the VIP category's starting
quantity). It mints its own `platform_superadmin` dev token
(`POST /v1/dev/auth/token`, the fixtures' org-admin JWT is refused on the
tier routes — see AGENTS.md on org-scoped roles) and sends `X-Admin-Reason`
on every write, same as a real operator would.

Post-run invariant for a session touched by `quota_admin.js` (category
quantities, places and the session-level ledger row must all agree — see
`docs/ops/bil24_gateway.md` §10.4 for the full explanation):

```sql
SELECT
  s.id AS session_id,
  s.capacity_total,
  (SELECT count(*) FROM session_seats ss
     WHERE ss.session_id = s.id AND ss.kind IN ('seat', 'ga_unit')) AS tier_places,
  (SELECT coalesce(sum(tt.capacity), 0) FROM ticket_tiers tt
     WHERE tt.session_id = s.id AND tt.deleted_at IS NULL) AS sum_tier_capacity,
  il.capacity_total AS ledger_capacity_total
FROM sessions s
LEFT JOIN inventory_ledger il ON il.session_id = s.id AND il.tier_id IS NULL
WHERE s.id = :session_id;
```

Test-design notes learned the hard way:
- Give every simulated buyer its own email and phone. Customers are matched
  by those identities and arena keeps one open order per customer per session,
  so a shared email makes buyers expire each other's pending orders.
- The first findings report is `docs/loadtest/2026-09-13_local_step1_ru.md`.

---

## First Production Profile — Single Instance CI Baseline

These thresholds are enforced by the k6 `thresholds:` blocks. A run fails the
CI job if any threshold is breached (no `continue-on-error`).

### Auth Login + Protected Endpoint (`auth-login.js`)

| Metric | Target | Notes |
|--------|--------|-------|
| /v1/me p50 | < 50 ms | JWT verify + indexed user lookup |
| /v1/me p95 | < 500 ms | Single instance under 5 VUs |
| /v1/me p99 | < 1000 ms | |
| me_errors rate | < 1% | |
| http_req_failed | < 5% | Covers setup login request |

> **bcrypt note**: Login at cost 12 takes ~300–500 ms server-side. `setup()`
> calls `login()` once (serial) so login latency does not affect the VU-phase
> `/v1/me` metrics. The generous `http_req_failed < 5%` bucket covers the
> single login request k6 records before steady-state begins.

### Scanner Validate (`scanner.js`)

| Percentile | Target |
|------------|--------|
| p50 | < 20 ms |
| p95 | < 60 ms |
| p99 | < 100 ms |
| Error rate (non-404) | < 0.1% |

> 404 responses (barcode not found) are counted as "misses", not errors, because
> the load script generates random synthetic refs.

### Public Feed Read (`feed.js`)

| Percentile | Target |
|------------|--------|
| p50 | < 20 ms |
| p95 | < 80 ms |
| p99 | < 150 ms |
| Error rate | < 0.1% |

### Checkout End-to-End (`checkout.js`)

| Metric | Target |
|--------|--------|
| Full flow p50 | < 80 ms |
| Full flow p95 | < 200 ms |
| Full flow p99 | < 400 ms |
| Error rate | < 0.5% |

---

## Prerequisites

### Install k6 ≥ 0.52.0

```bash
# macOS
brew install k6

# Linux (apt)
sudo gpg -k
sudo gpg --no-default-keyring --keyring /usr/share/keyrings/k6-archive-keyring.gpg \
  --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys C5AD17C747E3415A3642D57D77C6C491D6AC1D69
echo "deb [signed-by=/usr/share/keyrings/k6-archive-keyring.gpg] \
  https://dl.k6.io/deb stable main" | sudo tee /etc/apt/sources.list.d/k6.list
sudo apt-get update && sudo apt-get install k6

# Windows
choco install k6
```

### Start the local stack with migrations and seed

```bash
# 1. Start postgres, redis, api, worker
docker compose up -d

# 2. Apply current migrations
docker compose --profile tools run --rm migrate up

# 3. Insert deterministic test fixtures (idempotent — safe to re-run)
#    Creates: super@test.arena.local, admin@test.arena.local (password: TestPass!23)
DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
  APP_ENV=development ENABLE_DEV_AUTH=false \
  go run ./apps/backend/cmd/arena-seed
```

### Enable dev-auth routes (for scanner/checkout scenarios)

The scanner and checkout scripts use the dev-stub auth endpoint. This is
enabled by default in docker-compose (`ENABLE_DEV_AUTH: "true"`).

For the auth-login scenario no dev routes are needed — it uses the real
`POST /v1/auth/login` endpoint which is always available.

---

## Running Tests

### Auth-login (production auth path — recommended first run)

```bash
BASE_URL=http://localhost:8080 k6 run ops/loadtest/auth-login.js
```

With custom user (must be present in the database):

```bash
BASE_URL=http://localhost:8080 \
  LOAD_TEST_USER=super@test.arena.local \
  LOAD_TEST_PASSWORD=TestPass!23 \
  VUS=5 DURATION=60s \
  k6 run ops/loadtest/auth-login.js
```

### Scanner

```bash
BASE_URL=http://localhost:8080 k6 run ops/loadtest/scanner.js
```

### Public feed (requires a real feed token)

```bash
BASE_URL=http://localhost:8080 FEED_TOKEN=<token> k6 run ops/loadtest/feed.js
```

### Checkout (requires pre-seeded org/channel/reservation)

```bash
BASE_URL=http://localhost:8080 \
  ORG_ID=<uuid> CHANNEL_ID=<uuid> RESERVATION_ID=<uuid> \
  k6 run ops/loadtest/checkout.js
```

### Custom VU/duration

```bash
BASE_URL=http://localhost:8080 VUS=10 DURATION=60s \
  k6 run ops/loadtest/auth-login.js
```

---

## Negative Tests

`ops/loadtest/scripts/check-negative.sh` verifies that k6 returns nonzero
in both failure modes:

```bash
# Requires k6 in $PATH
bash ops/loadtest/scripts/check-negative.sh
```

What it checks:

| Test | What happens | Expected k6 exit |
|------|-------------|-----------------|
| Unavailable API | Points k6 at port 19999 (nothing listening); `http_req_failed` threshold `<1%` is immediately breached by connection-refused errors | **Nonzero** |
| Breached threshold | Custom Rate metric set to 100% failure; threshold requires `<10%` — mathematically impossible to pass | **Nonzero** |

The CI load-test job runs this suite **before** the real k6 scenarios to confirm
that any threshold breach will honestly fail the job.

---

## CI Integration

Load tests run optionally on pull requests when the `load-test` label is applied,
or on demand via `workflow_dispatch`. See `.github/workflows/load-test.yml`.

### Workflow correctness guarantees (PR-11)

- Migrations are applied to current head **before** k6 starts.
- Deterministic seed (arena-seed) runs before k6; auth-login requires it.
- Readiness polling exits **nonzero** on deadline and dumps all service logs.
- `continue-on-error` is **removed** from all k6 steps.
- Results are uploaded with `if: always()` even when thresholds fail.
- Negative tests run before real scenarios to prove k6 fails honestly.
- Service logs are captured and uploaded as an artifact on any failure.

---

## Tuning Knobs

### Auth login latency > target

| Knob | Location | Action |
|------|----------|--------|
| bcrypt cost | `arena-seed/main.go` | Reduce cost for test users (trade security for speed in non-prod) |
| DB index | `users` table | Ensure index on `email` column |
| Connection pool | `DATABASE_POOL_MAX_CONNS` | Increase for higher auth concurrency |

### Feed read latency > target

| Knob | Location | Action |
|------|----------|--------|
| Response cache | `handlePublicFeed` in `feeds.go` | Add `Cache-Control: public, max-age=N` or Redis cache layer |
| DB index | `0013_feed_tokens.sql` | Ensure index on `(token, revoked_at)` |
| Connection pool | `DATABASE_POOL_SIZE` env var | Increase max connections |

### Scanner latency > target

| Knob | Location | Action |
|------|----------|--------|
| DB index | `0029_barcode_authorities.sql` | Add composite index on `(authority_id, external_ref, status)` |
| FOR UPDATE contention | `MarkBarcodeScanned` SQL | Partition by authority to reduce row-lock contention |
| Connection pool | `DATABASE_POOL_SIZE` env var | Increase max connections |

### Checkout latency > target

| Knob | Location | Action |
|------|----------|--------|
| Transaction depth | `checkout.go` | Merge start+confirm into a single DB transaction if free-ticket |
| Reservation lookup | `0021_reservations.sql` | Add index on `(id, expires_at)` |
| Outbox write | `handleCompleteCheckout` | Defer outbox write to background worker |

---

## Metrics Dashboard Integration

When k6 runs with the Prometheus remote write output enabled, it pushes
custom metrics into Prometheus. The arena_new Grafana dashboard
(`ops/grafana/dashboards/arena_platform_overview.json`) includes a
"Load Test Results" row fed from these metrics.

```bash
K6_PROMETHEUS_RW_SERVER_URL=http://localhost:9090/api/v1/write \
  k6 run --out experimental-prometheus-rw ops/loadtest/auth-login.js
```

Custom metric names exported:

| Metric | Script |
|--------|--------|
| `k6_me_latency_ms` | auth-login.js |
| `k6_feed_latency_ms` | feed.js |
| `k6_scan_latency_ms` | scanner.js |
| `k6_checkout_flow_ms` | checkout.js |

---

## Directory Structure

```
ops/loadtest/
├── README.md              ← this file (baseline targets + runbook + PR-11 notes)
├── auth-login.js          ← production login + /v1/me scenario (PR-11 required)
├── checkout.js            ← checkout end-to-end scenario
├── feed.js                ← public feed read scenario
├── scanner.js             ← scanner barcode validate scenario
├── scripts/
│   └── check-negative.sh  ← negative tests: proves k6 fails honestly
├── shared/
│   └── auth.js            ← shared JWT helpers (login + devToken + bearerHeader)
└── results/               ← gitignored; written by handleSummary
    ├── auth-login-summary.json
    ├── checkout-summary.json
    ├── feed-summary.json
    └── scanner-summary.json
```
