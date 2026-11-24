# arena_new — Load Test Baseline (k6)

## Overview

k6 load test scripts for the latency-critical paths identified in the
architecture decision log ([arch: Q8 — hundreds of tickets/day target]).

| Script | Scenario | VU default | Auth method |
|--------|----------|------------|-------------|
| `auth-login.js` | Production login + GET /v1/me | 5 | Real POST /v1/auth/login |
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
| `provision.mjs` | Fixtures for the stand (local by default, `ALLOW_REMOTE=1` for a disposable copy — never production): channel with a short hold TTL, gateway credential (fid/token), a stripe/test payment config with a stub-backed key and a fresh webhook secret, import API key, four events (`flow` 20k+2k GA, `race` 10 GA, `expiry` 20 GA, `paywindow` 20 GA), public feed token with the events published, org JWT. Writes `results/fixtures.local.json` (gitignored, holds the stand's secrets). |
| `gateway.js` | The Bil24-compatible gateway (`/compat/bil24/json`) the migrated WordPress sites use. `SCENARIO=flow` (browsers + buyers + abandoned carts, polls tickets), `race` (N buyers for the last tickets), `expiry` (abandoned holds must return to sale after the TTL), `paywindow` (late payments refused, units back on sale). |
| `native.js` | arena's own public API used by the widget: feed → `checkout/start` (hosted Stripe Checkout Session on the stub) → signed `checkout.session.completed` webhook on the organizer's own route → public checkout status. `SCENARIO=flow` or `race`. |
| `bil24/docker-compose.loadtest.yml` | Compose override: mounts the gateway, turns SQL query logging off, starts the Stripe stub (`apps/widget/scripts/stripe-stub.cjs`) and points `STRIPE_API_BASE_URL` at it. |
| `capacity.mjs` | The staircase: one 3-minute k6 run per order rate (`STEPS`, default 25,50,100,200,400 orders/min, visitors ×2), a pause between steps, stop at the first step that breaks (errors > 0.5 %, journey p95 > 1 s, failed purchases, tickets not shown, or k6 dropping iterations = the generator is the bottleneck). Prints the table and the last clean step = this server's capacity for `ENTRY=native` or `ENTRY=gateway`. Size `FLOW_POOL` for the whole staircase first (orders × 2 tickets on average), but keep it hall-sized: GA command cost grows with the category's place count — a 60 000-place pool made RESERVATION 8× slower than a 2 000-place one on the same server. `VISITORS_PER_ORDER` (default 2) and `MAX_VISITORS` (cap; a 2 vCPU generator holds ~1000 k6 VUs) shape the browsing crowd. With `STAND_EXEC="ssh arena-loadtest"` (or nothing, for the local stand) plus `STAND_API_CONTAINER` / `STAND_PG_CONTAINER` it also snapshots `pg_stat_activity` at 60 % of every step and counts the api's own log for the step, so a k6 timeout is attributed to the stand only when the api really answered late or queued on a lock. |
| `monitor.mjs` | One CSV row per `INTERVAL` seconds: docker stats of the arena containers + a Postgres snapshot (connections, lock waits, longest tx, deadlocks, outbox/worker/delivery backlogs, dead letters, expired-but-unreleased holds, DB size). `--summary <csv>` prints first / peak / last per column — a column that ends above where it started is a leak or a queue that never drained. |
| `sql/audit.sql` | Read-only inventory audit for one session: ledger vs units vs tickets, double-sold units, expired holds never released. Every "violations" column must be 0. |
| `quota_admin.js` | One operator VU that edits GA category quotas (plan 08_architecture/23) WHILE `flow` sells tickets on the same session: swings the VIP quantity ±`SWING` every `TICK_SECONDS`, closes/reopens VIP every `CLOSE_EVERY` ticks for `CLOSE_FOR_SECONDS`, adds a "Late release" category once at `ADD_AT_SECONDS`. Run it next to a `flow` scenario (`gateway.js` or `native.js`), against the same `results/fixtures.local.json`. Success = `qa_errors` == 0, `qa_patch_unexpected` == 0 (only 200 or the documented 409 `tier.quantity_below_used` are expected), and the category invariant query (below) holds afterwards. |

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
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=paywindow grafana/k6:0.54.0 run /lt/gateway.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=flow   grafana/k6:0.54.0 run /lt/native.js
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e SCENARIO=race   grafana/k6:0.54.0 run /lt/native.js

# 3b. Operator swinging GA category quotas on the flow session, run alongside 3
docker run --rm -v "$PWD/ops/loadtest:/lt" -e BASE_URL=http://host.docker.internal:8080 -e DURATION=3m grafana/k6:0.54.0 run /lt/quota_admin.js

# 3c. Where is the ceiling? Staircase per entry point, with the resource monitor alongside
#     (re-provision with FLOW_POOL=60000 first — the default steps buy ~2300 tickets, larger steps more)
node ops/loadtest/monitor.mjs &
ENTRY=native  node ops/loadtest/capacity.mjs
ENTRY=gateway node ops/loadtest/capacity.mjs
node ops/loadtest/monitor.mjs --summary ops/loadtest/results/monitor-<ts>.csv

# 4. Audit a session afterwards (session ids are in results/fixtures.local.json)
docker exec -i arena_postgres psql -U arena -d arena -v session_id=<uuid> < ops/loadtest/sql/audit.sql
```

Knobs: `BROWSERS`, `ORDERS_PER_MIN`, `ABANDON_PER_MIN`, `DURATION`, `RACERS`,
`TICKET_POLL_SECONDS`, `EXPIRY_GRACE_SECONDS`, `SHARED_IP=1` (native: all
visitors from one IP), `SHARED_BUYERS=N` and `PAY_DELAY_SECONDS=N` (gateway:
fold buyers onto N shared email/phone identities and wait before paying, so
orders of one customer overlap; expect `gw_open_order_refused` > 0 and no
failed payments, the journey threshold fails by design), `DEBUG=1` (log every
failed call), `ABORT_ON_FAIL=1` (gateway/native: the error-rate and journey
thresholds abort the run as soon as they are breached, evaluated after
`DELAY_ABORT_EVAL`, default 30s — always set it for a run against a server),
`RETURN_URL` (native: the origin the buyer returns to, default from the
fixtures). Provisioning:
`FLOW_POOL`, `RACE_POOL`, `EXPIRY_POOL`, `PAYWINDOW_POOL`, `RESERVATION_TTL`,
`PAYMENT_WINDOW`, `PAYMENT_GRACE`, `RETURN_URL`, `ALLOW_REMOTE=1`. `quota_admin.js`:
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

Running the suite against a server (a disposable copy of production, never
production itself — monitoring, insurance and abort criteria):
`docs/loadtest/server_run_runbook_ru.md`.

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

### Enable dev-auth routes (for the checkout scenario)

The checkout script uses the dev-stub auth endpoint. This is
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
| `k6_checkout_flow_ms` | checkout.js |

---

## Directory Structure

```
ops/loadtest/
├── README.md              ← this file (baseline targets + runbook + PR-11 notes)
├── auth-login.js          ← production login + /v1/me scenario (PR-11 required)
├── checkout.js            ← checkout end-to-end scenario
├── feed.js                ← public feed read scenario
├── scripts/
│   └── check-negative.sh  ← negative tests: proves k6 fails honestly
├── shared/
│   └── auth.js            ← shared JWT helpers (login + devToken + bearerHeader)
└── results/               ← gitignored; written by handleSummary
    ├── auth-login-summary.json
    ├── checkout-summary.json
    └── feed-summary.json
```

---

## Staging "hype" sales opening (`staging-hype.mjs`)

Node 22 script, no npm dependencies (Playwright only through `apps/widget`).
Models a sales opening on the STAGING stand: many buyers load the seat map at
once, pick adjacent seats, start checkout and pay (signed Stripe webhook, as in
`native.js`); a few real mobile browsers pay with the Stripe test card. It
refuses any `API_BASE` that does not contain "staging" or is not localhost.
The feed token and the webhook secret come from files (`FEED_TOKEN_FILE`,
`WHSEC_FILE`) and are never printed or logged. Env knobs are listed in the
header comment of the script.

```powershell
$env:FEED_TOKEN_FILE = "C:\path\feed.txt"; $env:WHSEC_FILE = "C:\path\whsec.txt"
$env:MODE = "smoke"; node ops/loadtest/staging-hype.mjs                      # 1 fast, 1 classic, 1 abandon, 1 browser
$env:MODE = "hype"; $env:BUYERS = "900"; node ops/loadtest/staging-hype.mjs  # the opening
```

Output: `results/hype-<run>.jsonl` (every request and buyer outcome) and
`results/hype-<run>.json` (summary: sell-out seconds, per-request p50/p95/max
and status counts, retries, outcomes, webhook-to-paid, double-sold check, final
seat-status). Stop switch: 5xx share over 5% in 30 s or 10 transport errors in
a row aborts the run (exit 3).

Live view while a run is going: `node ops/loadtest/live-view.mjs`, then open
http://localhost:4319/ (env `SESSION_ID`, `RUN` = file or run name, default the
newest `results/hype-*.jsonl`, `PORT`, `API_BASE`; staging or localhost only).
It shows the hall with seat colours (free, held, sold), counters, requests per
second, checkout_start p95 and error counts. The server polls the public
seat-status once per 2 s for all browsers and reads the JSONL incrementally; no
tokens are used.

### `PROFILE=big`: 5000 buyers, ~45 minutes, held seats must come back

```powershell
$env:MODE = "hype"; $env:PROFILE = "big"; node ops/loadtest/staging-hype.mjs
```

Defaults: `BUYERS=5000`, `ARRIVAL_SECONDS=300`, `MAX_SECONDS=3000`. Each buyer
gets one behaviour by share: `FAST_SHARE` 0.55, `SLOW_SHARE` 0.10 (pays after
`SLOW_MIN_SECONDS`-`SLOW_MAX_SECONDS`, 120-1500), `CANCEL_SHARE` 0.15,
`SILENT_SHARE` 0.10 (starts and stops, no cancel, no pay), `CARDFAIL_SHARE`
0.05 (extra 10-30 s, then pays), `DOUBLE_SHARE` 0.05 (double pay, reload, two
tabs). `PREPARE_SHARE` still splits fast and classic inside each behaviour.
`WAITERS` (500) buyers who find the hall sold out keep polling until
`WAIT_UNTIL_SECONDS` (2700) and buy seats that come back. `LATE_WEBHOOKS` (20)
silent buyers get a signed `completed` webhook `LATE_AFTER_SECONDS` (2070) after
their start; `DUP` (20) paid orders get the same event again, a new event id and
an `expired` event. The summary adds per-behaviour outcomes, non-2xx codes per
request name, waiter stats, seat recovery times, late and duplicate webhook
results, DOUBLE results (a second 201 or order is a defect, exit 1), a 30-second
timeline (seat counts, requests/s, in-flight, event-loop lag), seats still held
at the end and the double-sold check over all paid orders. Details in the
header of the script. An unpaid hold lives about 33 minutes on the stand, so a
full run needs the whole hour: do not stop it early.

One Node process lags past ~2000 buyers (event-loop lag up to 3 s): run 5000
as several processes with distinct `RUN` names on the same `SESSION_ID`
(1667 buyers each). Buyer phones depend on `RUN`, because the stand matches
customers by phone as well as by e-mail; `SAME_PHONES=1` makes all runs share
phones on purpose, to test buyers who share a phone. Before a big run
set the worker `EMAIL_MODE=log` and raise the api `PUBLIC_API_IP_RATE_LIMIT`
on the stand, and restore both afterwards.
