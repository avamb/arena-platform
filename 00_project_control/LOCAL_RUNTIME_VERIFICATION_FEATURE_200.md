# Local Runtime Verification — Feature #200

**Feature:** #200 — Validate `/healthz`, `/readyz`, `/v1/info` on localhost
**Category:** Local Runtime / Health Endpoints
**Date:** 2026-06-25
**Environment:** local docker-compose stack (`docker-compose.yml`), arena_api
container `arena_api`, image sha `83e85b7a1bd4` (byte-identical to staging /
production image per `deploy/DOKPLOY.md` §2).

## Topology

```
NAME             STATUS                  PORTS
arena_api        Up 22 hours (healthy)   0.0.0.0:8080->8080/tcp
arena_postgres   Up 3 days (healthy)     0.0.0.0:55432->5432/tcp
arena_redis      Up 3 days (healthy)     0.0.0.0:56379->6379/tcp
arena_worker     Up 22 hours (healthy)   8080/tcp (internal only)
```

Host URL for the API: **http://localhost:8080**

## Step 1 — `curl -i http://localhost:8080/healthz`

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: 019effd7-068b-7596-a6d2-2baf50184a7b
X-Trace-Id:   4545c4d41e280db74abea727ec5ec53e
Content-Length: 16

{"status":"ok"}
```

**Result:** 2xx ✅ (`200 OK`, valid JSON, liveness contract satisfied).

## Step 2 — `curl -i http://localhost:8080/readyz`

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: 019effd7-06ab-7a52-92eb-3870b301fdb0
X-Trace-Id:   880036309addcc38a4b575c59e43e723
Content-Length: 46

{"checks":{"database":"ok"},"status":"ready"}
```

**Result:** 2xx ✅ (`200 OK`, `status=ready`, dependency map confirms database
round-trip succeeded). Redis is not gated on `/readyz` because PostgreSQL is the
declared source of truth; Redis is best-effort cache/locks per the architecture
spec.

## Step 3 — `curl -i http://localhost:8080/v1/info`

```
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Request-Id: 019effd7-06ca-7075-9563-a4f438efdd2f
X-Trace-Id:   8b169a498147df3c952b9a5e06624ab8
Content-Length: 323

{
  "app": "arena-api",
  "version": "0.1.0-dev",
  "commit": "local",
  "env": "development",
  "supported_locales": ["ru","en"],
  "default_locale": "en",
  "active_locale": "en",
  "server_time": "2026-06-25T17:32:22.602441459Z",
  "db_version": "17.10",
  "db_now": "2026-06-25T17:32:22.604807Z",
  "request_id": "",
  "trace_id": "8b169a498147df3c952b9a5e06624ab8"
}
```

**Result:** 2xx ✅ (`200 OK`, well-formed JSON with all expected fields:
build identifiers, locales, server time, live DB round-trip).

## Step 4 — Final localhost URL

| Endpoint   | URL                                 | Status |
| ---------- | ----------------------------------- | ------ |
| `/healthz` | http://localhost:8080/healthz       | 200    |
| `/readyz`  | http://localhost:8080/readyz        | 200    |
| `/v1/info` | http://localhost:8080/v1/info       | 200    |

Raw response captures (headers + body) are saved under
`00_project_control/runtime_evidence/feature_200_{healthz,readyz,v1info}.txt`.

## Acceptance Mapping

| Feature #200 step                                                | Result |
| ---------------------------------------------------------------- | ------ |
| `curl -i http://localhost:<PORT>/healthz` → 2xx                  | PASS   |
| `curl -i http://localhost:<PORT>/readyz` → 2xx + readiness body  | PASS   |
| `curl -i http://localhost:<PORT>/v1/info` → 2xx + valid JSON     | PASS   |
| Record exact localhost URL in final report                       | PASS   |

Feature #200 verified end-to-end against the local docker-compose stack.
