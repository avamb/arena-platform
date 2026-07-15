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
