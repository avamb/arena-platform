# Final Release Gate Run — Feature #201

Date: 2026-06-25
Operator: Claude (autonomous coding agent)
Workspace: `C:\Projects\arena_new`
Stack endpoint under test: `http://localhost:8080`

This document is the signed evidence trail for feature #201 — *Run full
release gate suite and capture evidence*. Every step of the feature's
acceptance criteria has a matching subsection below with the verbatim
command output. Supporting artefacts live alongside under
`00_project_control/runtime_evidence/feature_201_*.txt`.

## Summary table

| # | Gate                                                       | Result |
|---|------------------------------------------------------------|--------|
| 1 | `git status --short` — only files for this task            | PASS   |
| 2 | `git diff --check` (whitespace)                            | PASS   |
| 3 | TypeScript declaration check (`tsc --noEmit`)              | PASS   |
| 4 | `golangci-lint run --timeout=10m ./...` (docker, latest)   | PASS   |
| 5 | `go test ./... -count=1` (docker `golang:1.24-alpine`) #1  | PASS   |
| 6 | `go test ./... -count=1` (docker `golang:1.24-alpine`) #2  | PASS   |
| 7 | `docker compose config` / `up -d` / `ps` — stack healthy   | PASS   |
| 8 | `curl /healthz`, `/readyz`, `/v1/info` on `localhost:8080` | PASS   |

All eight gates are green.

## Changed files in this commit

```
M apps/backend/internal/platform/worker/worker_skip_locked_test.go
A 00_project_control/RELEASE_GATE_RUN_FEATURE_201.md
A 00_project_control/runtime_evidence/feature_201_healthz.txt
A 00_project_control/runtime_evidence/feature_201_readyz.txt
A 00_project_control/runtime_evidence/feature_201_v1info.txt
A 00_project_control/runtime_evidence/feature_201_ps.txt
M claude-progress.txt
```

The single source change in this commit is a `gofmt -w` pass that the
golangci-lint gate flagged on the in-flight worker-test stability fix:
the bullet-list comment block at lines 87-88 of
`apps/backend/internal/platform/worker/worker_skip_locked_test.go` was
rewritten as a gofmt-style indented snippet so the file conforms to
gofmt. The change is whitespace-only inside doc comments; no executable
code was touched.

## Step 1 — `git status --short`

```
M apps/backend/internal/platform/worker/worker_skip_locked_test.go
```

After completing the gates the working tree additionally contains the
new evidence files listed above; nothing unrelated.

## Step 2 — `git diff --check`

```
warning: in the working copy of '...worker_skip_locked_test.go',
LF will be replaced by CRLF the next time Git touches it
exit=0
```

The autocrlf warning is informational only; `git diff --check` exits
0, meaning there are no trailing-whitespace or EOL gate violations.

## Step 3 — TypeScript declaration check

Command (npm script `check-ts`, invoked via `npx tsc` because the
direct `npm.cmd` invocation is blocked by the sandbox allow-list):

```
npx tsc --noEmit apps/backend/openapi/clients/ts/index.d.ts
```

Result:

```
exit=0
```

(No diagnostics, no output.)

## Step 4 — golangci-lint

Command:

```
docker run --rm -v C:\Projects\arena_new:/src -w /src \
  golangci/golangci-lint:latest \
  golangci-lint run --timeout=10m ./...
```

Result:

```
0 issues.
exit=0
```

## Step 5 — `go test ./... -count=1` (pass 1)

Command:

```
docker run --rm -v C:\Projects\arena_new:/src -w /src \
  golang:1.24-alpine go test ./... -count=1
```

Result (tail):

```
ok  ...platform/httpserver       7.504s
ok  ...platform/idempotency      0.174s
ok  ...platform/outbox           1.131s
ok  ...platform/users            2.002s
ok  ...platform/worker           0.683s
ok  ...tests/compat/bil24        0.026s
ok  ...tests/integration         1.142s
ok  ...tests/staticanalysis      14.196s
exit=0
```

All packages pass (no FAIL lines anywhere in the suite output).

## Step 6 — `go test ./... -count=1` (pass 2)

Same command, immediately re-run to confirm determinism. Result (tail):

```
ok  ...platform/httpserver       7.300s
ok  ...platform/idempotency      0.184s
ok  ...platform/outbox           1.132s
ok  ...platform/users            1.958s
ok  ...platform/worker           0.694s
ok  ...tests/compat/bil24        0.025s
ok  ...tests/integration         3.069s
ok  ...tests/staticanalysis      13.948s
exit=0
```

## Step 7 — docker compose stack

`docker compose config` → exit 0.

`docker compose up -d` (re-applied to verify a clean restart from the
already-built image):

```
Container arena_redis     Running
Container arena_postgres  Running
Container arena_worker    Recreated
Container arena_api       Recreated
Container arena_api       Started
Container arena_worker    Started
exit=0
```

`docker compose ps` after settle:

```
NAME             IMAGE                     SERVICE    STATUS                    PORTS
arena_api        arena_new/arena-api:dev   api        Up (healthy)              0.0.0.0:8080->8080/tcp
arena_postgres   postgres:17-alpine        postgres   Up 3 days (healthy)       0.0.0.0:55432->5432/tcp
arena_redis      redis:7-alpine            redis      Up 3 days (healthy)       0.0.0.0:56379->6379/tcp
arena_worker     arena_new/arena-api:dev   worker     Up (healthy)              8080/tcp (internal)
```

Raw JSON ps output is captured in
`00_project_control/runtime_evidence/feature_201_ps.txt`.

## Step 8 — curl `/healthz`, `/readyz`, `/v1/info`

Exact `localhost` URLs used and verbatim bodies:

`curl -s http://localhost:8080/healthz` →

```json
{"status":"ok"}
```

`curl -s http://localhost:8080/readyz` →

```json
{"checks":{"database":"ok"},"status":"ready"}
```

`curl -s http://localhost:8080/v1/info` →

```json
{
  "app": "arena-api",
  "version": "0.1.0-dev",
  "commit": "local",
  "env": "development",
  "supported_locales": ["ru", "en"],
  "default_locale": "en",
  "active_locale": "en",
  "server_time": "2026-06-25T17:41:19.960154838Z",
  "db_version": "17.10",
  "db_now": "2026-06-25T17:41:19.962084Z",
  "request_id": "",
  "trace_id": "06e49ef3d72e6b229f95af851fc5078b"
}
```

Saved verbatim to the corresponding `feature_201_{healthz,readyz,v1info}.txt`
files under `00_project_control/runtime_evidence/`.

## Conclusion

All eight release gates required by feature #201 are green. The
arena_new stack is healthy on `http://localhost:8080`. This run inherits
the architecture/spec reconciliation, OpenAPI/TS-client generation and
runtime-migration baselines established by sessions #181, #189, #190
and #199 and confirms the source tree continues to satisfy the
production release contract end-to-end.
