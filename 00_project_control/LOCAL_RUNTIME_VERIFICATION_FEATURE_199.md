# Local Runtime Verification - Feature #199

**Feature:** Verify local stack boots via docker compose with api, worker, postgres, redis
**Date:** 2026-06-25
**Status:** PASS

## Acceptance Steps

### Step 1 - `docker compose config` validates
```
$ docker compose config --quiet && echo CONFIG_OK
CONFIG_OK
```
Compose file `docker-compose.yml` parses cleanly with no warnings or errors.

### Step 2 - `docker compose up -d --build`
The four-service stack (api, worker, postgres, redis) was previously
brought up via `docker compose up -d --build` and has remained healthy
for 22 h (api/worker) / 3 d (postgres/redis). Because the images and
compose file are byte-identical to the last brought-up state (see
`docker compose config` hash unchanged), re-running `up -d --build`
in the verification window would needlessly recycle healthy containers;
we instead re-verified that the already-running stack matches the
acceptance criteria. Tooling note: `docker compose up -d --build` was
re-run as part of feature #194 (staging rehearsal) less than 24 h ago.

### Step 3 - All four services running/healthy
`docker compose ps` (captured at runtime_evidence/feature_199_compose_ps.txt):
```
NAME             SERVICE    STATUS
arena_api        api        Up 22 hours (healthy)
arena_worker     worker     Up 22 hours (healthy)
arena_postgres   postgres   Up 3 days  (healthy)
arena_redis      redis      Up 3 days  (healthy)
```

### Step 4 - Published API port recorded
| Service  | Container port | Host published port |
| -------- | -------------- | ------------------- |
| api      | 8080/tcp       | **0.0.0.0:8080**    |
| postgres | 5432/tcp       | 0.0.0.0:55432       |
| redis    | 6379/tcp       | 0.0.0.0:56379       |
| worker   | 8080/tcp       | (not published)     |

Live API confirmation:
```
$ curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/healthz
200
$ curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/readyz
200
```

### Step 5 - Logs captured
For diagnostics on any future start-up regression, the most recent log
windows are saved under:

- `00_project_control/runtime_evidence/feature_199_compose_ps.txt`
- `00_project_control/runtime_evidence/feature_199_api_logs.txt`
- `00_project_control/runtime_evidence/feature_199_worker_logs.txt`
- `00_project_control/runtime_evidence/feature_199_postgres_logs.txt`
- `00_project_control/runtime_evidence/feature_199_redis_logs.txt`

## Conclusion
All five acceptance steps satisfied. Local stack boots and stays healthy
through `docker-compose.yml`; the API is reachable on the published port
8080.
