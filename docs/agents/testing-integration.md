# Testing and integration-test fixtures

Integration-test traps: fixtures, shared databases, outbox draining, worker jobs, load tests, CI-only failures. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **`sessions` has no `capacity` column** — it is `capacity_total` (migration
  0016) plus the nullable `capacity_override` (0079). Likewise
  `memberships_role_check` does NOT allow `org_admin`; use `organizer` (0011,
  widened by 0042). Both bite integration-test fixtures that guess the column
  or enum value from the Go side.

- **`sessions.status` allows only `draft`/`scheduled`/`cancelled`/`completed`**
  (`sessions_status_check`). `published` is an *events* status, not a sessions
  one — seeding a session with it fails with SQLSTATE 23514.

- **`httpserver.Options` needs BOTH `Pool` and `PgxPool` for a fully wired test
  server.** `PgxPool` only feeds the `*gen.Queries` fallbacks; `Pool` feeds
  `s.pool`, which is the nil-guard `bil24_shims.go` checks before wiring the
  gateway-session store, the customer store and the Bil24 cart deps. With
  `PgxPool` alone, CREATE_USER self-gates and answers `-99`.

- **`s.pool` is the `PoolDB` INTERFACE; `s.pgxPool` is the raw
  `*pgxpool.Pool`.** Anything wiring a helper that takes a `*pgxpool.Pool`
  (`orderexport.Query*`, the MACS export) must read `s.pgxPool` — passing
  `s.pool` fails to compile with "need type assertion".

- **Waiting on the worker in a test: never read a job row right after the
  handler signals, and never poll for a transient status.** A handler runs
  BEFORE the worker writes `MarkDone`/`MarkRetry`, so "handler fired, now
  read the row" races the write (`TestWorkerBoot109_WorkerClaimsAndCompletesJob`
  failed in CI with `got "claimed"` after 20.02 s: it read the row under
  `q.mu` and called `Stop()` while holding the lock, so `MarkDone` blocked and
  `Stop` waited out the 20 s shutdown timeout). Wait for the status
  (`waitForStatus`) with a generous deadline and hold no queue lock across
  `Stop()`. A retried job is `pending` only for the retry backoff (50 ms), so
  a test that needs to observe or restart on that state calls
  `q.freezeAfter(n)` (`worker_jobs_persistence_test.go`) and `q.thaw()` before
  the next worker starts. To reproduce such a race, temporarily add a
  `time.Sleep` to `inMemoryQueue.MarkDone`; `go test -race` needs cgo, which
  this Windows host lacks, so run it in `golang:1.26.9` via docker.

- **Windows file locking**: On this Windows host, tests that call
  `LocalStorage.Get` (which opens an `*os.File`) must explicitly `Close()` the
  body before calling `Delete()` — Windows refuses to delete open files. Linux
  CI is unaffected. General pattern: close all file handles before any
  `os.Remove`/`st.Delete` calls in tests.

- **Integration tests must use real handlers + real dispatcher**: do not
  stub the HTTP handler or the outbox dispatcher in tests that are supposed
  to verify the full delivery chain. The MACS round-trip tests
  (`TestMACS_RoundTrip`, `TestMACS_AB50e_ThreeTicketRoundTrip`) exercise
  `macs.Dispatcher.Dispatch` → real HTTP → stub receiver, which is the only
  way to catch envelope-shape regressions.

- **Global unique-index literals in integration tests must be randomized too,
  not just org-scoped values**: `customer_identities_strong_uq` is a GLOBAL
  unique index on `(kind, value_normalized)`, unscoped by org/channel. A
  fixed phone literal in `postgres_store_integration_test.go` (email and
  device token were already randomized, phone was not) collided with a
  leftover row from a prior interrupted run against the shared persistent
  `arena_postgres` dev-stand and produced a spurious "expected Created=true"
  failure with no code defect behind it. Fix: derive every value feeding a
  global-unique column from a per-run random/derived source, not just the
  values that happen to be org- or channel-scoped.

- **Reproduce the CI Integration job on a FRESH database, not on the shared dev
  stand.** The dev-stand DB (`arena`, port 55432) carries months of data, so
  tests that only pass thanks to pre-existing rows (or fail only on empty
  tables) diverge from CI. Seen 2026-09-12: the W1-S1 e2e test decoded
  `warnings` as `[]string`, passed on the stand (no warnings there) and failed
  in CI where a fresh import returns advisory `{code,message}` objects. Recipe:
  `docker exec arena_postgres psql -U arena -d arena -c "CREATE DATABASE arena_ci OWNER arena"`
  (DROP first if it exists — one statement per `-c`, DROP cannot run in a
  transaction), then with `DATABASE_URL=...:55432/arena_ci...` run
  `go.exe run ./apps/backend/cmd/arena-migrate up`, `go.exe run ./apps/backend/cmd/arena-seed`,
  and `go.exe test -count=1 -p 1 -tags integration ./apps/backend/...`. The
  `internal/tests/pgtest` package panics on Windows ("rootless Docker is not
  supported") — that one is host-specific, ignore it locally.
  **Also run the package under test BEFORE any other package** (or alone):
  CI runs packages in parallel, so rows that another package's harness seeds
  (e.g. `tests/compat/bil24/seed_test.go` inserting country `CZ`/`czechia`,
  which `0006_geo.sql` never seeds) are NOT guaranteed to exist. Seen
  2026-09-12 on run 34711169622: the 533/538 provisioning test passed locally
  only because the harness had already inserted Czechia, and failed in CI
  with `import.country_unresolved`. A test that needs geo rows outside the
  0006 seed list must insert them itself with the harness's idempotent
  `INSERT ... ON CONFLICT (iso2) DO NOTHING` idiom.

- **Load-test the sales paths with `ops/loadtest` (gateway.js, native.js,
  provision.mjs, sql/audit.sql), never against a server.** `provision.mjs`
  refuses non-local BASE_URLs. Run k6 from `grafana/k6:0.54.0` with
  `BASE_URL=http://host.docker.internal:8080`; on Git Bash prefix
  `MSYS_NO_PATHCONV=1` and mount a `C:/...` path. Give every simulated buyer a
  distinct email AND phone: customers are matched by those identities, and a
  second CREATE_ORDER_EXT for the same customer+session is refused with
  `101 bil24.open_order_exists` while the first order's hold is live.
  Findings of the first run: `docs/loadtest/2026-09-13_local_step1_ru.md`.

- **A test that drives `HandlePaymentIntentWebhook` end-to-end (checkout
  completion, ticket issuance) MUST sweep `worker_jobs` in its cleanup.**
  The webhook enqueues `checkout.issue_tickets` / `checkout.convert_reservation`
  rows keyed by `checkout_session_id` / `reservation_id` in their JSON
  `payload` (no FK to clean them up via cascade). A fixture that deletes
  `checkout_sessions/reservations` without first deleting the matching
  `worker_jobs` rows (`payload->>'checkout_session_id' IN (...)` /
  `payload->>'reservation_id' IN (...)`, run BEFORE those parent deletes)
  leaks rows into the shared test database; another integration test that
  drains `worker_jobs` generically (`auth_email_integration_test.go`) then
  fails with "no handler for job type checkout.issue_tickets" — seen live
  2026-09-13 on `arena_ci_webhook`.

- **`webhook_widget_completion_integration_test.go` leaks one
  `checkout.issue_tickets` row into `worker_jobs`**, and
  `TestAuthEmailIntegrationPR02_VerificationEmailArrives` (which drains
  `worker_jobs` generically) then fails with `no handler for job type
  "checkout.issue_tickets"` on the NEXT run against the same database.
  It is leftover contamination, not a regression: `DELETE FROM worker_jobs
  WHERE job_type='checkout.issue_tickets'` and re-run. The AGENTS.md rule
  about sweeping `worker_jobs` in such fixtures is still not honoured there.

- **A test that drains `outbox_events` must claim only its own rows.**
  `PGOutboxEventStore.ClaimNext` takes ANY pending row of the shared
  database, and CI runs packages in parallel, so a test's drain loop both
  works through other packages' backlog before reaching its own row and
  "delivers" their events through whatever dispatchers it wired — a fan-out
  with no-op MACS legs made `04_refund_dedup` see `macs=0`, and
  `TestSuperadminOrgProvisioning533_Integration` went red twice on
  2026-09-19. Wrap the store with an `aggregate_id` filter
  (`prov533ScopedStore` in that test). Locally, the dev stand's
  `arena_worker` container also polls `outbox_events` and claims test rows
  (it cannot reach a host `127.0.0.1` stub, so the row backs off for an
  hour): `docker stop arena_worker` while running outbox integration tests,
  `docker start arena_worker` after.
  Scoping only protects OTHER packages from your drain: a generic drain in a
  parallel package can still claim YOUR row first (mark it processed through
  its own no-op legs or back it off for an hour), and a scoped claim filtered
  on `processed_at IS NULL` never sees it again. A test that must prove a
  delivery should fall back to reading its own row directly and handing it
  to the same real dispatcher (`prov533PublishedEvent`), failing only when
  the row is missing or the delivery errors.

- **The `Widget Acceptance (real backend)` CI job needs BOTH a return-URL
  fallback and a fake Stripe, because it runs a real `arena-api` BINARY.**
  Go integration tests inject a stub through `httpserver.Options.
  StripeAPIBaseURL`; that seam does not exist for a separate process, so
  the job sets `STRIPE_API_BASE_URL` (env, read in
  `feed_payment_shims.go`'s `stripeBaseURL` — the Options override still
  wins) and starts `apps/widget/scripts/stripe-stub.cjs` on port 12111
  BEFORE arena-api. The value must carry the `/v1` segment: the adapter
  concatenates `"/checkout/sessions"` onto it. `config.Validate` REFUSES a
  non-empty `STRIPE_API_BASE_URL` under `APP_ENV=production` — it would
  otherwise post live checkouts, carrying the organizer's own secret key,
  to somebody else's endpoint. Separately, the acceptance suite sends no
  `return_url`, so the job also needs `PUBLIC_TICKETS_BASE_URL=http://
  localhost:4174` (the origin `serve-demo-real.cjs` serves the demo page
  from) or every paid `checkout/start` answers 400
  `checkout.invalid_return_url`. Locally: start the stub, then arena-api
  with those two variables, then `ARENA_API_URL=http://localhost:<port>
  npm --prefix apps/widget run test:e2e:real`. The suite hard-codes
  single-use seats (B01/B02, C0x, D0x) against ~15-minute holds, so a
  second run on the same database fails with 409 where 201 is expected —
  recreate the database between runs rather than chasing the "failure".

- **An integration test that drives a PAID `checkout/start` now needs a
  payment provider.** Since the hosted flow landed, a cart with a total above
  zero is only confirmed when a hosted page can actually be created for it.
  Such a fixture needs `sales_channels.provider='stripe'`, a configured
  `payment_provider_configs` row, a `return_url` in the request body, and a
  Server built with `Options.StripeAPIBaseURL` pointing at a stub — see
  `enableStripeForChannel` / `newStubStripe` / `buildHostedCheckoutServer` in
  `httpserver/hosted_checkout_integration_test.go`. `enableStripeForChannel`
  returns a teardown that must be deferred AFTER the fixture's own (defers are
  LIFO, so it then runs BEFORE the organization it references is deleted).

- **A test file must be named `*.test.ts` or vitest never runs it.**
  `apps/widget/src/widget_377_test.ts` (Go-style `_test` suffix) sat outside
  `vitest.config.ts`'s `include: ['src/**/*.test.ts']` and had never executed;
  when renamed, 2 of its 23 assertions were long stale (a `toStartWith`
  matcher chai does not have, and a `not.toContain` guarding against a
  synthetic-event stub that was deliberately reintroduced later as the PR2-21
  fallback). Check the reported test-file COUNT after adding a suite, not just
  that the run is green.

- **`internal/platform/delivery`'s integration tests come in two flavours
  and only one runs on this host.** `delivery_integration_test.go` uses
  `internal/tests/pgtest` (testcontainers — panics on Windows);
  `presentation_integration_test.go` connects to `DATABASE_URL` and skips
  when it is unset, the pattern the `gen` package's live-DB tests use.
  Select with `-run` when running the package locally, and point
  `DATABASE_URL` at a FRESH scratch database (the CI-Integration recipe
  above), never the shared dev stand. That file's fixture needs
  `arena-migrate` only — it seeds its own org/venue/event/session/order and
  relies on migration 0006's `tallinn` city plus its English `i18n_text`
  row — but the `htickets` live-DB tests in the same sweep DO need
  `arena-seed` ("no (org, channel, session) triple with GA inventory
  found").

- **The sales-path load-test suite (`ops/loadtest`) needs the Stripe stub,
  and its native scenario pays through the organizer's own webhook route.**
  Since the hosted-checkout flow, a paid `checkout/start` only answers 201
  once a Checkout Session exists, so `bil24/docker-compose.loadtest.yml`
  runs `apps/widget/scripts/stripe-stub.cjs` as the `stripe_stub` service
  (`HOST=0.0.0.0` — the stub binds 127.0.0.1 by default) and points
  `STRIPE_API_BASE_URL` at it; the stub also answers `GET /v1/balance` so a
  stub-backed payment config verifies `ok`. `provision.mjs` creates that
  stripe/test config (re-keys an existing one on 409) and writes
  `native.payment_config_id` + `native.webhook_secret` into the fixtures;
  `native.js` parses the `cs_…` id out of `redirect_url`, posts a
  `checkout.session.completed` (`payment_status: paid`) to
  `/v1/payment-intents/webhook/{config_id}` signed with `k6/crypto` HMAC,
  and refuses fixtures that predate this (re-run provisioning). A compose
  bind-mount path in an overlay file is resolved against the PROJECT
  directory (repo root), not the overlay's own directory —
  `../../../apps/...` silently became `C:\apps\...` and the container
  crash-looped on MODULE_NOT_FOUND. `provision.mjs` refuses non-local hosts
  unless `ALLOW_REMOTE=1`, and refuses the production hostnames outright;
  `ABORT_ON_FAIL=1` turns the error-rate/journey thresholds of `gateway.js`
  and `native.js` into run-aborting ones. Server runs: only on a disposable
  copy, `docs/loadtest/server_run_runbook_ru.md`. The shared local stand
  had migration 0103 skipped (0104 applied first): `migrate up` refuses with
  "found 1 missing migrations" — apply its Up block by hand per the goose
  gotcha above before provisioning there.

- **Server load test 2026-09-22 (docs/loadtest/2026-09-22_server_step2_ru.md)
  — what sizes the stand and what only looks like it.** (1) The widget's
  hold transaction (`hcheckout` checkout/start) holds the `sessions` row
  lock (`IncrementSessionSeatStatusVersion`) while the Go code runs between
  statements; under a 1000-visitor browse crowd on 2 vCPU that gap grows,
  the queue on the row outgrows `DB_POOL_MAX_CONNS` (20) and EVERYTHING
  stalls until the client's 30 s timeout — `context canceled` on every
  route, 20/20 conns busy, CPU idle. `DB_POOL_MAX_CONNS=60` turned 11 ok /
  590 failed into 1202/1202 on the same server; a real fix is to shorten
  the hold tx (customer resolve, pricing, payment pre-flight BEFORE the
  lock) and never acquire a second pool conn inside it. (2) GA gateway
  commands scale with the category's place count: a 60k-place session made
  RESERVATION 443 ms vs 56 ms at 2k (`session_seats` scan per hold /
  count). (3) Gateway browse must be modelled as the WP plugin (few
  server-side catalog readers), not one GET_ALL_ACTIONS per visitor —
  ~50 ms of DB CPU each. (4) `provision.mjs` re-keys the org's shared
  payment config on 409, so older fixture sets get 401 on the webhook;
  unpaid widget checkouts hold places for 33 min (payment window), not the
  2-min reservation TTL. (5) Behind a real proxy Traefik drops the
  client's X-Forwarded-For unless the peer is in
  `forwardedHeaders.trustedIPs`; the generator (and Cloudflare for the
  orange-cloud run) must be listed and `TRUSTED_PROXY_COUNT` raised to 2
  (3 via Cloudflare), or every visitor is one IP and the 600/min per-IP
  limit 429s the whole test. Docker 29 needs `"min-api-version": "1.24"`
  in daemon.json for Traefik's docker provider; Hetzner projects have a
  shared-core limit (prod 4 + copy + generator) — move the generator to
  `ccx` before rescaling the copy; `pkill -f <script>` inside an
  `ssh host '…'` line kills its own shell.
  **Fixed 2026-09-23:** `hfeed` checkout/start and checkout recover now
  read seat tier prices, price windows and promo caps through the hold
  transaction's own Queries (`h.tierQueries.WithTx(tx)` /
  `h.promoQueries.WithTx(tx)`), the GA promo check and the payment
  pre-flight reads run BEFORE `BeginTx` (`preparePaymentPreflight`, decided
  after pricing by `applyPaymentPreflight`), and migration 0109 adds
  `session_seats_ga_tier_status_idx (session_id, tier_id, status, seat_key)
  WHERE kind='ga_unit'` — GA allocation on a 66k-place session went from
  150 ms / 2130 buffers to 0.2 ms / 9. **Rule: inside an open pgx
  transaction never call a `*gen.Queries` bound to the pool** (every
  `h.<x>Queries` field is one) — use `.WithTx(tx)` or move the read before
  `BeginTx`; a pool acquire while holding a row lock is a distributed
  deadlock waiting for a full pool. `publicTierUnitPrice`,
  `seatPricingLines` and `applyPromoDiscount` take the handle explicitly
  for that reason.
  **Rerun 2026-09-23 (`docs/loadtest/2026-09-23_server_step2_rerun_ru.md`)
  on the fixed image confirmed it is the code, not the pool:** cpx22 with
  `DB_POOL_MAX_CONNS=20` went from 19 ok / 1 182 failed to 1 138 / 0 at
  400 orders/min + 1 000 visitors, zero lock waiters on every step. On
  2 vCPU a pool of 60 is NOT better — a step-start burst (1 000 visitors
  arriving at once) then runs straight into Postgres (13–35 holds queued
  on the sessions row, 120 % CPU) instead of queueing cheaply for a pool
  slot inside the api, so the run's p95 grows to ~0.9 s; on cpx32 (prod,
  4 vCPU) the burst is invisible and 60 is fine. Two tooling traps from
  that night: the owner's Cloudflare token is IP-filtered to their IPv4,
  and Python `urllib` may pick IPv6 for `api.cloudflare.com` — zone
  calls then answer 401 code 10000 while `/user/tokens/verify` still
  says 200 (force `AF_INET`); and killing a background `bash script.sh`
  wrapper kills only the outer shell — the script keeps running, so a
  long orchestrator needs a stop-file check (or `pkill -f script.sh`).

- **A "writes nothing" test must count rows it can attribute, never whole tables (U-6, 2026-10-15).** CI runs the integration packages in parallel against one shared database, so `SELECT count(*) FROM media_objects` (or `outbox_events`, `worker_jobs`, `compatibility_id_map`) moves whenever another package's fixture inserts or cleans up. `TestEventBundleDryRun_Bil24SourceWritesNothing` compared such table-wide counts and failed twice on 2026-10-15 on docs-only PRs (`media_objects went 2 -> 0`), green on re-run. It now uses `attributableSideEffects` (`himports/event_bundle_dry_run_bil24_integration_test.go`): outbox and queue rows whose payload names the fixture's organization, `session_changes` and `media_objects` by `org_id`, compat ids by the fixture's wire ids. To reproduce this class of flake, run a second psql session that toggles a row in the counted table with `\watch 0.003` while the test runs. A new test of this kind filters by the fixture's own ids.

- **`runWorkerJob` (`auth_email_integration_test.go`) closes a job of an unknown type without running it instead of failing (2026-10-23).** The gate database is shared by parallel packages and runs, so the queue also holds `session.change_email`, letter and refund jobs other tests left behind; the old named list of ignorable types went stale with every wave and master CI failed twice in a row on `no handler for job type "session.change_email"` right after the wave D merge. A missing handler for the test's OWN job still fails the test ("no email addressed to ... arrived").
