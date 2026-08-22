# AutoForge backlog: production readiness remediation — Wave PR

Updated: 2026-07-15

Status: executable planning source mirrored into `.autoforge/features.db` as
features `#344`–`#356`. AutoForge executes the database records, not this file.

Audit baseline: repository `master` at `3c1d7fe`.

## Goal

Remove the confirmed deployment blockers found by the 2026-07-15 independent
readiness audit. The wave is complete only when production authentication,
email delivery, outbox delivery, worker health, tagged PostgreSQL integration
tests, frontend artifacts, and release gates work together in a clean checkout.

## Mandatory rules for every feature

- Read `09_autoforge/00_AGENT_GUARDRAILS.md` before implementation.
- Treat current source code and migrations as authoritative; older readiness
  reports that stop at migration `0041` are stale.
- Do not mark a feature passing from unit tests alone when its acceptance
  criteria require PostgreSQL, SMTP, webhook, container, browser, or CI-level
  integration.
- Never weaken or delete a failing acceptance test merely to obtain green.
- Never log passwords, JWTs, verification tokens, password-reset tokens, raw
  ticket barcodes, SMTP credentials, webhook secrets, or complete signed URLs.
- Production must fail fast on an unsafe or ambiguous configuration. Silent
  fallback to development adapters is forbidden.
- Keep OpenAPI, generated clients, `.env.example`, Dokploy documentation, and
  production validation synchronized with runtime behaviour.
- Record exact commands and pass summaries in `claude-progress.txt`.

## Dependency order

```text
PR-00 configuration contract
├── PR-01 production auth ─────────────┐
├── PR-02 auth email jobs              │
├── PR-03 ticket SMTP delivery         │
├── PR-04 outbox fail-closed delivery  ├── PR-10 CI release graph
└── PR-05 worker health                │
PR-06 integration-suite repair ────────┤
PR-07 admin production artifact ───────┤
PR-08 widget E2E lifecycle ────────────┤
PR-09 reproducible containers ─────────┘
PR-10 CI release graph ── PR-11 honest load gate ── PR-12 release evidence
```

## PR-00 — Establish one production configuration contract

Model: opus

Category: Production Configuration / Security

Depends on: none

Objective:

Make documented environment variables, runtime behaviour, and production
validation one typed contract. Remove silent configuration drift before other
production adapters are changed.

Acceptance criteria:

- Every active variable in `.env.example` is parsed and applied, or is removed
  from the example and documentation. This explicitly covers JWT TTL,
  issuer/audience, idempotency TTL/key limit, worker poll/job/retry settings,
  worker concurrency, and outbox batch size.
- Add typed configuration for the canonical public application URL, email
  delivery mode and SMTP connection, outbox delivery mode/webhook signing, and
  healthcheck target.
- `APP_ENV=production` rejects: missing/weak JWT secret, dev auth, wildcard
  CORS, query logging, unsafe DB TLS mode, unsigned local media, log-only email,
  and implicit/noop outbox mode.
- Secrets are redacted from structured config logs and error messages.
- Table-driven config tests cover every production rejection and a valid
  production configuration.
- `.env.example`, `deploy/DOKPLOY.md`, and hardening docs match the tested
  contract.

Verification:

- `go test ./apps/backend/internal/platform/config/...`
- `go test ./...`
- `docker compose config --quiet`

## PR-01 — Make internally issued JWTs work in production

Model: opus

Category: Authentication / Security

Depends on: PR-00

Objective:

Replace the protected-route dependency on the disabled development stub with a
production JWT verifier that validates tokens issued by normal login/refresh.

Acceptance criteria:

- Authentication middleware depends on a verifier interface, not directly on
  `StubProvider`.
- Production verifier validates signature, algorithm, expiry, issuer, audience,
  subject, roles/permissions, and revocation/session rules using the same
  contract as `IssueJWT`.
- With `APP_ENV=production` and `ENABLE_DEV_AUTH=false`, login followed by
  `GET /v1/me` and one protected API request returns success, not
  `503 auth.disabled`.
- Invalid, expired, wrong-audience, wrong-issuer, and tampered tokens are
  rejected without leaking verification details.
- Dev token endpoints remain unavailable in production.
- Refresh and logout behaviour remains covered; no token is written to logs.
- A PostgreSQL-backed integration test exercises the complete production login
  → protected route → refresh → logout path.

Verification:

- `go test -tags=integration ./apps/backend/internal/platform/httpserver/...`
- `go test ./apps/backend/internal/platform/auth/...`

## PR-02 — Deliver registration and password-reset mail through durable jobs

Model: opus

Category: Authentication / Email / Security

Depends on: PR-00

Objective:

Replace development token logging with durable email jobs and canonical,
non-host-header-derived links.

Acceptance criteria:

- Registration and password-reset handlers enqueue dedicated durable jobs after
  the database transaction succeeds; they do not synchronously call SMTP.
- Verification/reset links use the validated canonical public URL from config,
  never `r.Host`, `r.TLS`, or untrusted forwarded headers.
- Logs contain correlation/job/user identifiers but never the token or complete
  signed URL.
- Responses remain enumeration-safe and do not claim delivery if enqueue fails.
- Templates are locale-aware, escaped, and include expiry information.
- Tokens are single-use and expiry is verified in integration tests.
- An SMTP capture integration test proves that both message types arrive and
  that their links complete the intended flow.

Verification:

- `go test -tags=integration ./apps/backend/internal/platform/httpserver/...`
- Run the auth email acceptance test against a local SMTP capture service.

## PR-03 — Make ticket email delivery fail honestly

Model: opus

Category: Worker / Email Delivery

Depends on: PR-00

Objective:

Ensure a ticket delivery job becomes `sent` only after a real configured sender
accepts the message.

Acceptance criteria:

- `LogSender` is restricted to explicit non-production use and cannot masquerade
  as a successful production delivery.
- Missing/disabled sender never results in delivery status `sent`; use an
  explicit `skipped`/`disabled` state only where the domain permits it.
- SMTP errors preserve retry semantics and ultimately dead-letter according to
  configured attempts.
- Status-update failure after SMTP success is reconciled idempotently and cannot
  duplicate an email without a stable delivery key.
- SMTP credentials and ticket secrets are redacted from logs.
- An integration test captures a real ticket email with PDF attachment and
  proves the state transition `queued → processing → sent`.
- A negative test proves SMTP refusal does not produce `sent`.

Verification:

- `go test -tags=integration ./apps/backend/internal/platform/delivery/...`
- `go test -tags=integration ./apps/backend/internal/platform/worker/...`

## PR-04 — Make outbox delivery explicit and fail-closed

Model: opus

Category: Outbox / Reliability / Security

Depends on: PR-00

Objective:

Prevent unconfigured or failed dispatch from marking outbox events delivered.

Acceptance criteria:

- Production requires an explicit outbox mode. Webhook mode requires URL and a
  strong signing secret; disabled mode does not claim or consume events.
- `NoopDispatcher` is limited to test/development and can never cause a
  production row to receive `processed_at`.
- Webhook success marks the event dispatched exactly once; timeout, non-2xx,
  invalid configuration, and signing failure leave it retryable.
- Retry/dead-letter/observability behaviour is documented and covered.
- Integration tests use a real HTTP receiver and PostgreSQL to prove success,
  retry, signature, duplicate-safety, and disabled-mode behaviour.

Verification:

- `go test -tags=integration ./apps/backend/internal/platform/outbox/...`

## PR-05 — Give API and worker correct container healthchecks

Model: sonnet

Category: Deployment / Observability

Depends on: PR-00

Objective:

Make the shared image healthcheck target the actual process endpoint for both
API and worker deployments.

Acceptance criteria:

- Healthcheck resolution is deterministic: explicit `HEALTH_ADDR` wins; worker
  configuration otherwise resolves to its metrics/health address; API resolves
  to its HTTP listen address.
- Dokploy and Compose examples set correct values explicitly.
- A container-level test starts API and worker separately and proves each image
  becomes healthy.
- A negative test proves an unavailable endpoint makes the container unhealthy.
- Worker health reports startup/readiness dependencies consistently and does not
  expose the internal metrics port publicly.

Verification:

- Build the production image and inspect health for both API and worker
  containers.

## PR-06 — Repair and stabilize the tagged PostgreSQL integration suite

Model: sonnet

Category: Testing / Migrations

Depends on: none

Objective:

Make `go test -tags=integration ./...` compile and pass against the pinned
dependency set and current migration head.

Acceptance criteria:

- Migration concurrency tests use APIs available in the pinned `goose` version,
  or the dependency is deliberately upgraded with compatibility review.
- `pgtest.TruncateAll` no longer hardcodes removed tables such as
  `scaffold_echo`; it safely discovers current application tables or uses a
  maintained authoritative list with regression coverage.
- Schema migration tables and extension-owned objects are excluded correctly.
- Worker retry/dead-letter integration tests pass without shared-database race or
  ordering assumptions.
- The full tagged suite passes twice from a clean cache to expose order/flaky
  failures.
- Standard non-tagged tests remain green.

Verification:

- `go test -tags=integration ./...`
- Repeat the same command a second time.
- `go test ./...`

## PR-07 — Produce a deployable admin-web artifact

Model: opus

Category: Admin Web / Deployment / Security

Depends on: PR-01

Objective:

Provide a production artifact for `apps/admin-web`; do not deploy Vite's
development server or bind-mounted source.

Acceptance criteria:

- Add a dedicated production image/target with deterministic `npm ci` build,
  non-root runtime, static asset serving, SPA fallback, cache headers, and a
  health endpoint.
- Runtime API-base configuration is documented and does not bake secrets into
  browser assets.
- Production source maps are disabled or stored as private CI artifacts.
- Split the oversized main bundle into sensible route/vendor chunks and keep a
  documented size budget.
- Placeholder Reports/Content/POS routes are not presented as working
  production modules; hide them behind explicit feature flags or remove them
  from production navigation until implemented.
- A browser smoke test uses the real production auth flow and loads `/v1/me`.
- Dokploy instructions include the admin service, domain, environment, health,
  and rollback procedure.

Verification:

- `npm --prefix apps/admin-web test`
- `npm --prefix apps/admin-web run build`
- Build and run the admin production container, then execute its smoke test.

## PR-08 — Make widget E2E terminate cleanly and remove accessibility warnings

Model: sonnet

Category: Widget / Test Infrastructure / Accessibility

Depends on: none

Objective:

Fix the Playwright lifecycle leak that leaves `npm run test:e2e` running after
all 57 tests report success, and eliminate the current Svelte accessibility
warning.

Acceptance criteria:

- The widget E2E command exits with code 0 within a documented timeout after all
  tests complete; it leaves no demo server, browser, or Node child process.
- Local and CI `reuseExistingServer` behaviour is deterministic and does not
  attach to a stale process accidentally.
- Failures still produce Playwright artifacts and non-zero exit status.
- The interactive seat-map element uses correct semantic/keyboard behaviour
  without suppressing a real compiler accessibility warning.
- Existing 57 E2E and 457 unit tests remain green.

Verification:

- `npm --prefix apps/widget test`
- `npm --prefix apps/widget run build`
- Run `npm --prefix apps/widget run test:e2e` twice with an outer 120-second
  timeout and verify clean exit both times.

## PR-09 — Make container builds reproducible and minimal

Model: sonnet

Category: Containers / Supply Chain

Depends on: PR-07

Objective:

Prevent local `node_modules`, build output, VCS metadata, test artifacts, and
AutoForge databases from entering Docker build contexts or stages.

Acceptance criteria:

- Add and test `.dockerignore` for `.git`, `.autoforge`, `node_modules`, local
  `dist`, test results, Playwright artifacts, editor files, and secrets while
  retaining every source/lockfile required by all production targets.
- Go and admin images build from a clean checkout without host dependency
  directories.
- Runtime images contain only required binaries/static assets and trusted CA/
  timezone data; they run as non-root where feasible.
- Image inspection finds no `.env`, database, source map, test report, token, or
  private key.
- Build context size is recorded and materially lower than the audited ~108 MB.

Verification:

- Build every production target with plain progress output.
- Inspect final image file lists and users.

## PR-10 — Turn CI into a real publication gate

Model: opus

Category: CI / Release Engineering

Depends on: PR-01, PR-02, PR-03, PR-04, PR-05, PR-06, PR-07, PR-08, PR-09

Objective:

Ensure no API image, admin image, widget asset, or release is published before
all relevant contract and real-integration gates pass.

Acceptance criteria:

- Add a dedicated tagged PostgreSQL integration job that runs
  `go test -tags=integration ./...` with required Docker/Testcontainers support.
- Add admin unit/build/container smoke jobs.
- Keep widget unit/build/size gates and require the real backend acceptance job;
  mock-only E2E cannot satisfy publication.
- `build-and-push` and every release publication depend on lint, unit/race,
  integration, OpenAPI drift, admin, widget, and acceptance jobs.
- A failed/skipped required job prevents publication; PR builds never publish.
- Jobs have explicit timeouts, useful failure logs/artifacts, concurrency
  cancellation, and least-privilege permissions.
- Add a workflow-structure regression test that asserts the dependency graph so
  a future edit cannot silently drop a gate.

Verification:

- Validate workflow syntax and dependency assertions locally.
- Demonstrate one deliberate failing-gate dry run where publication is skipped,
  then restore and obtain a green run.

## PR-11 — Make the load-test workflow fail honestly

Model: sonnet

Category: Performance / CI

Depends on: PR-06, PR-10

Objective:

Eliminate false-green load tests caused by missing migrations/readiness and
`continue-on-error`.

Acceptance criteria:

- The workflow runs current migrations and deterministic seed/setup before k6.
- Readiness polling exits non-zero when the deadline expires and captures API,
  worker, migration, PostgreSQL, and Redis logs.
- k6 threshold failure fails the job; results are still uploaded using
  `if: always()` rather than `continue-on-error`.
- At least one scenario authenticates through the production login flow and
  accesses a protected endpoint.
- Thresholds and dataset size are documented and suitable for the selected first
  production profile.
- Add a negative workflow test or script proving unavailable API and breached
  thresholds both return non-zero.

Verification:

- Run the workflow scripts against a clean local Compose stack.

## PR-12 — Automate current-head release evidence and refresh stale docs

Model: opus

Category: Release Gate / Documentation

Depends on: PR-10, PR-11

Objective:

Replace the stale migration-0041 readiness evidence with a reproducible gate for
the current migration head and current commit.

Acceptance criteria:

- Release checks discover the embedded migration head dynamically; no checklist
  hardcodes `0041` or another obsolete version.
- Update README, release checklist, production hardening checklist, and Dokploy
  instructions to match the implemented runtime and actual services.
- A fresh ephemeral production-like stack runs migrations through current head,
  starts API/worker/admin, and executes smoke tests for: login + protected API,
  registration email, reset email, ticket email/PDF, signed outbox webhook,
  API health, worker health, admin login, and widget checkout entry.
- Rollback rehearsal and migration status are recorded without claiming that
  irreversible down-migrations are safe.
- Produce a new evidence report containing exact commit, image digests,
  migration head, commands, timestamps, and outcomes.
- If any required external staging credential/service is unavailable, mark the
  feature blocked/needs-human-input; never reuse old evidence or mark passing.

Verification:

- Run the automated production-like release gate from a clean checkout.
- Confirm `git status --short` is clean after generated evidence is committed.

## Wave Definition of Done

- All features `#344`–`#356` are passing in `.autoforge/features.db`.
- `go test ./...` and `go test -tags=integration ./...` pass independently.
- Admin and API/worker production images build and become healthy.
- Widget unit/build/size/E2E commands pass and terminate cleanly.
- CI cannot publish with any required gate red or skipped.
- A new current-head release evidence report replaces, but does not rewrite the
  historical staging report.
