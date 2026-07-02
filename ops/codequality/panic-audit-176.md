# Feature #176 — Audit of `panic()` calls in production code

**Date:** 2026-06-25
**Status:** Complete; static-analysis gate enforced by
`apps/backend/tests/staticanalysis/nopanic_176_test.go`.

## Scope

Audit every `panic(` call site in non-test, non-`cmd/` Go source under
`apps/backend/` and classify each as:

- **(a) Programmer invariant** — keep, document with `// allow:panic`
  and add a test exercising the surrounding contract.
- **(b) User input / IO error** — replace with `error` return; HTTP layer
  logs `slog.Error` and responds with 5xx.
- **(c) Initialization / boot-time wiring** — keep, document with
  `// allow:panic`; must not be reachable from a request path.

## Findings

The audit found **zero** category-(b) call sites. Every production
`panic(` in non-test code is either a constructor / boot-time
precondition guard, a documented `Must*` variant, the idiomatic
defer-rollback-and-rethrow pattern, or the dedicated debug endpoint
that exists to exercise the panic-recoverer middleware.

| File | Line | Category | Justification |
| --- | --- | --- | --- |
| `internal/payments/routing.go` | 57 | (c) init | `PaymentRoutingPolicy.Register` is called only from boot wiring; nil adapter is a programmer error. |
| `internal/payments/routing.go` | 63 | (c) init | Same as above; empty `ProviderName()` is a programmer error. |
| `internal/platform/ids/uuidv7.go` | 63 | (a) invariant | `MustNewUUIDv7` is the documented panic-on-error variant of `NewUUIDv7`; request-path code uses the error-returning variant. |
| `internal/adapters/postgres/pool.go` | 260 | (a) invariant | Re-raise panic in `defer` after rolling back the in-flight transaction; idiomatic Go. |
| `internal/platform/worker/outbox_backlog_poller.go` | 56 | (c) init | Constructor nil-dependency guard; called once from `cmd/arena-worker`. |
| `internal/platform/worker/outbox_backlog_poller.go` | 61 | (c) init | Same as above for the `Gauge` dependency. |
| `internal/platform/i18n/middleware.go` | 41 | (c) init | Middleware-construction-time nil-dependency guard; not reachable per-request. |
| `internal/platform/ratelimit/ratelimit.go` | 73 | (c) init | Constructor configuration validation (`MaxAttempts > 0`). |
| `internal/platform/ratelimit/ratelimit.go` | 79 | (c) init | Constructor configuration validation (`Window > 0`). |
| `internal/platform/observability/metrics.go` | 315 | (a) invariant | `MustNew` is the documented panic-on-error variant of `New`; called once from `main()`. |
| `internal/platform/httpserver/debug_panic.go` | 27 | (a) invariant | Dedicated `GET /v1/debug/panic` endpoint that exists solely to exercise the panic-recoverer middleware in integration tests; mounted only when `DEBUG_ROUTES_ENABLED=true`. |
| `internal/platform/networkscope/networkscope.go` | 187 | (c) init | Constructor nil-dependency guard (`NewScoper` requires a non-nil `Querier`); called only from boot wiring. |

`cmd/arena-migrate/main.go:403` (goose `Fatalf` adapter) is exempt
because it lives under `cmd/` — the static-analysis gate ignores that
subtree.

## Enforcement

`apps/backend/tests/staticanalysis/nopanic_176_test.go` walks
`apps/backend/`, skips `*_test.go` files and the `cmd/` subtree, strips
trailing line-comments to avoid false matches on doc comments, and
fails on any `panic(` call that is not annotated with
`// allow:panic` on the same or immediately preceding line.

If the test fails:

1. If the new `panic()` is recoverable (user input, IO, request path),
   convert it to an `error` return; the HTTP layer logs `slog.Error`
   and responds with 5xx.
2. If the new `panic()` is a genuine programmer-invariant or
   boot-time precondition, annotate it with
   `// allow:panic: <one-sentence reason>` and add a row to the table
   above.
