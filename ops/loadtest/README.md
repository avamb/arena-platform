# arena_new — Load Test Baseline (k6)

## Overview

k6 load test scripts for the three latency-critical paths identified in the
architecture decision log ([arch: Q8 — hundreds of tickets/day target]):

| Script | Scenario | Concurrency default |
|--------|----------|---------------------|
| `feed.js` | Public feed read (GET /v1/feeds/{token}) | 50 VUs |
| `scanner.js` | Scanner barcode validate (POST /v1/scan) | 20 VUs |
| `checkout.js` | Checkout end-to-end (start → confirm → complete) | 10 VUs |

---

## Baseline Targets — Single Instance

These are the SLO thresholds enforced by the k6 `thresholds:` blocks.
A run fails CI if any threshold is breached.

### Public Feed Read (`feed.js`)

| Percentile | Target |
|------------|--------|
| p50 | < 20 ms |
| p95 | < 80 ms |
| p99 | < 150 ms |
| Error rate | < 0.1% |

### Scanner Validate (`scanner.js`)

| Percentile | Target |
|------------|--------|
| p50 | < 20 ms |
| p95 | < 60 ms |
| p99 | < 100 ms |
| Error rate (non-404) | < 0.1% |

> Note: 404 responses (barcode not found) are counted as "misses", not errors,
> because the load script generates random synthetic refs.

### Checkout End-to-End (`checkout.js`)

| Metric | Target |
|--------|--------|
| Full flow p50 | < 80 ms |
| Full flow p95 | < 200 ms |
| Full flow p99 | < 400 ms |
| Error rate | < 0.5% |

---

## Prerequisites

1. Install k6 ≥ 0.52.0:
   ```
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

2. Start the local stack:
   ```
   docker compose up -d
   ```

3. (Optional) Enable debug routes for dev JWT support:
   ```
   DEBUG_ROUTES=true in your .env
   ```

---

## Running Tests

### Quick run — all three scripts

```bash
# Public feed (requires a real feed token):
BASE_URL=http://localhost:8080 FEED_TOKEN=<token> k6 run ops/loadtest/feed.js

# Scanner (uses dev JWT, no seed data required — tests 404-miss path):
BASE_URL=http://localhost:8080 k6 run ops/loadtest/scanner.js

# Checkout (requires pre-seeded org/channel/reservation):
BASE_URL=http://localhost:8080 \
  ORG_ID=<uuid> \
  CHANNEL_ID=<uuid> \
  RESERVATION_ID=<uuid> \
  k6 run ops/loadtest/checkout.js
```

### Custom VU/duration

```bash
BASE_URL=http://localhost:8080 FEED_TOKEN=<token> \
  VUS=100 DURATION=60s \
  k6 run ops/loadtest/feed.js
```

### Output results to JSON

k6 writes a summary JSON to `ops/loadtest/results/` automatically via
`handleSummary`. This directory is gitignored.

---

## Tuning Knobs

If a baseline target is missed, try these levers before scaling horizontally:

### Feed read latency > target

| Knob | Location | Action |
|------|----------|--------|
| Response cache | `handlePublicFeed` in `feeds.go` | Add `Cache-Control: public, max-age=N` or a Redis-backed cache layer |
| DB index | `0013_feed_tokens.sql` | Ensure index on `(token, revoked_at)` |
| Connection pool | `DATABASE_POOL_SIZE` env var | Increase max connections |
| JSON serialisation | `feeds.go` | Switch to `encoding/json` with pre-allocated buffer |

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

## CI Integration

Load tests run optionally on pull requests when the `load-test` label is
applied. See `.github/workflows/load-test.yml`.

Results are uploaded as GitHub Actions artifacts under `load-test-results`.

---

## Metrics Dashboard Integration

When k6 runs with the Prometheus remote write output enabled, it pushes
custom metrics into Prometheus. The arena_new Grafana dashboard
(`ops/grafana/dashboards/arena_platform_overview.json`) includes a
"Load Test Results" row fed from these metrics.

To enable during a run:

```bash
K6_PROMETHEUS_RW_SERVER_URL=http://localhost:9090/api/v1/write \
  k6 run --out experimental-prometheus-rw ops/loadtest/feed.js
```

Custom metric names exported:
- `k6_feed_latency_ms` (Trend → p50/p95/p99 histograms)
- `k6_scan_latency_ms`
- `k6_checkout_flow_ms`
- `k6_feed_errors_rate`
- `k6_scan_errors_rate`
- `k6_checkout_errors_rate`

---

## Directory Structure

```
ops/loadtest/
├── README.md              ← this file (baseline targets + runbook)
├── checkout.js            ← checkout end-to-end scenario
├── feed.js                ← public feed read scenario
├── scanner.js             ← scanner barcode validate scenario
├── shared/
│   └── auth.js            ← shared JWT helpers
└── results/               ← gitignored; written by handleSummary
    ├── checkout-summary.json
    ├── feed-summary.json
    └── scanner-summary.json
```
