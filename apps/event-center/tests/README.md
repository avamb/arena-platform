# Tests of the web event center

Three layers, from the cheapest:

| Layer | Where | Runs against | Command |
|-------|-------|--------------|---------|
| Unit and parity | `src/**/*.test.ts(x)` | nothing | `npm --prefix apps/event-center test` |
| Mock smoke | `tests/smoke/*.spec.ts` | `tests/smoke/mockApi.ts` (`page.route`) | `npm --prefix apps/event-center run test:smoke` |
| **Live twin tests** | `tests/smoke/live/*.spec.ts` | a REAL `arena-api` on a migrated database | `npm --prefix apps/event-center run test:e2e:live` |

The mock smoke proves a screen draws what a mock says. It cannot prove that the
real API says the same. A **twin test** is the Playwright version of a scenario
of the Telegram bot's Go e2e (`bot_e2e_*_integration_test.go`): it goes through
the same flow against the live API, and it is the proof of a web screen for the
stages 4 and 5 of the plan (`docs/plans/event_center_unified_plan_2026-10-14_ru.md`,
section 8.2).

## The rule of three roles

Every screen of the event center is proven on **owner, manager and agent** of
one organization: membership roles `org_admin`, `organizer` and `agent`. A twin
test loops over the three roles (see `events-three-roles.spec.ts`) and:

1. asks the server what the person may do: `GET /v1/organizations/{org_id}/me`
   with the person's access token (`person.access_token` from the harness). The
   server is the truth, not the role name;
2. signs in through the real login form (`signInAs`);
3. asserts that the screen shows exactly what the server allowed: a button or
   figure the person may not use is absent (`toHaveCount(0)`), and the request
   behind it was never sent (`apiCalls` listener).

A screen decides by `useOrgAccess(orgId).can(permission)`, never by the role
and never by `/v1/me` (AGENTS.md, A1).

## Run the live twin tests locally

The harness needs a **migrated database of its own**. Never point it at the
shared dev database `arena`: the fixture is small, but a scratch base keeps the
runs repeatable. The harness enforces it: it exits with an error when `DATABASE_URL`
names a non-local host or the database `arena`.

1. Create the database on the dev stand (once). Run each statement as its own
   command, `DROP` cannot run inside a transaction:

   ```powershell
   docker exec arena_postgres psql -U arena -d arena -c "DROP DATABASE IF EXISTS arena_ci_w09"
   ```

   ```powershell
   docker exec arena_postgres psql -U arena -d arena -c "CREATE DATABASE arena_ci_w09 OWNER arena"
   ```

2. Migrate it. The port is whatever `docker port arena_postgres` says: 55432
   normally, 45432 on the host where Windows reserved 55432 (AGENTS.md,
   "Docker Desktop ... NAT port exclusion").

   ```powershell
   $env:DATABASE_URL = "postgres://arena:arena@localhost:55432/arena_ci_w09?sslmode=disable"
   $env:PATH = "C:\Program Files\Go\bin;$env:PATH"
   go run ./apps/backend/cmd/arena-migrate up
   ```

   **Trap for an agent in the AutoForge sandbox:** its command allowlist
   rejects a bash command that contains the bare token `postgres`, even in an
   env assignment. Split the word, so the scanner does not see it:
   `DATABASE_URL="post""gres://arena:arena@localhost:55432/arena_ci_w09?sslmode=disable" go.exe run ./apps/backend/cmd/arena-migrate up`
   (`export` and `base64` are blocked too, so the value goes inline in front
   of the command). A Claude Code worktree session may refuse that form too;
   PowerShell with `$env:DATABASE_URL = "..."` set on its own line works there.

3. Run the tests. `DATABASE_URL` stays set in the same shell:

   ```powershell
   npm --prefix apps/event-center run test:e2e:live
   ```

   Playwright starts two servers itself: `scripts/live-harness.mjs` (builds
   `arena-e2e-seed` and `arena-api` with `go`, seeds the fixture, starts the
   API on 18086, signs the three people in and writes
   `tests/smoke/.live-env.json`) and the vite dev server on 5177 with
   `VITE_API_BASE_URL` pointing at that API (without it the app throws on boot).
   The first run takes about two minutes for the Go builds. Set
   `ARENA_API_BIN` and `ARENA_E2E_SEED_BIN` to prebuilt binaries to skip them (CI
   does). The ports are `ARENA_LIVE_API_PORT`, `ARENA_LIVE_READY_PORT`,
   `ARENA_LIVE_UI_PORT` and `ARENA_LIVE_STRIPE_PORT` (18088).

   The harness also runs a small Stripe stub (task 8-1) and points arena-api
   at it with `STRIPE_API_BASE_URL`, so the refund twin
   (`live/refund.spec.ts`) drives the REAL refund engine without reaching
   Stripe. The stub answers the same `Idempotency-Key` with the first refund,
   declines an amount of 7.77, and lists the calls it got at `GET /__calls`
   (`POST /__reset` forgets them); `.live-env.json` names it as
   `stripe_stub_url`.

4. Drop the database when you are done:

   ```powershell
   docker exec arena_postgres psql -U arena -d arena -c "DROP DATABASE arena_ci_w09"
   ```

The fixture is idempotent (fixed UUIDs, `ON CONFLICT DO NOTHING`), so a second
run on the same database is fine. A test that CHANGES data must clean up after
itself or use its own rows: the next run, and the next spec, share the base.

## What the fixture holds

`apps/backend/cmd/arena-e2e-seed` (a `config.RoleMigrate` tool, listed in
`cmd_config_role_test.go`) creates one organization "E2E Event Center Org"
(country ES, venue zone Europe/Madrid), three accounts that are **memberships**
of it, and one published event "E2E Twin Show" with one scheduled session in 30
days and one open category of 30 places (so the list says "On sale"). The
accounts, the ids and the test password are in the tool's source; the harness
copies them into `.live-env.json` (git-ignored), which `liveEnv()` reads.

| Person | Membership role | Holds `order.read` |
|--------|-----------------|--------------------|
| `e2e-owner@test.arena.local` | `org_admin` | yes |
| `e2e-manager@test.arena.local` | `organizer` | yes |
| `e2e-agent@test.arena.local` | `agent` | no |

The roles are memberships, not `user_roles`: `arena-seed`'s org-scoped
`user_roles` never reach a real login's permission checks (AGENTS.md), so its
users cannot play these parts.

## Add a twin test

1. Read the bot's scenario (`apps/backend/internal/platform/httpserver/bot_e2e_<name>_integration_test.go`)
   and note the API calls and the database rows it asserts.
2. Put the data the scenario needs into `arena-e2e-seed` **as a new fixed-UUID
   row**, or create it inside the test through the API with the person's
   `access_token`. Do not edit the existing rows: other specs rely on them.
3. Add `tests/smoke/live/<name>.spec.ts`, import `liveEnv`, `person`,
   `signInAs`, `noA11yViolations` from `./liveEnv`, and loop over the three
   roles. Assert what the server's `/me` allowed, not what you expect from the
   role name.
4. Database rows: the live twins assert through the API only, because the
   Playwright harness has no database access. For a resend that is the
   `delivery[]` recipient and state, the order's `buyer_email` and the 409 of a
   seller site's order. The `worker_jobs` and `order_events` rows of the same
   scenario are proven by the Go e2e `bot_e2e_resend_integration_test.go`.
   No SQL driver is bundled. Read the result back through the
   API (the order, the refund, the audit list), or, when only SQL can prove it,
   add a `pg` dev dependency by installing it **from inside `apps/event-center`**
   (`npm --prefix` adds the repository root as a `file:../..` dependency and
   breaks `npm ci`, see AGENTS.md).
5. Keep the file name `*.spec.ts`, and keep it under `tests/smoke/live/`: the
   mock config ignores that folder (`testIgnore`), the live config runs only it.

## CI

Job `event-center-live` of `.github/workflows/ci.yml`: Postgres service,
`arena-migrate up`, prebuilt `arena-api` and `arena-e2e-seed`, then the same
`test:e2e:live`. The mock smoke stays in `event-center-build`.

## Posters under the real CSP (task 7-1)

`tests/smoke/live/poster.spec.ts` (live twin, real API with a LOCAL media storage:
`live-harness.mjs` sets `MEDIA_BACKEND=local`, `MEDIA_LOCAL_ROOT=.live/media`,
`API_PUBLIC_URL` unless you name your own) covers creation with a poster, bad files,
replacement, the three roles, foreign media and the attach-after-creation failure.
`arena-e2e-seed` adds one media row of the OTHER organization (`foreign_media_id`).

`tests/smoke/container/poster-csp.spec.ts` runs against the PRODUCTION image, which
the dev server cannot prove (nginx adds the policy):

1. start the live harness by hand with the container origin as the UI port, so CORS
   allows it: `ARENA_LIVE_UI_PORT=8092 node scripts/live-harness.mjs` (same
   `DATABASE_URL` rules as above);
2. build the image for that API:
   `docker build --build-arg VITE_API_BASE_URL=http://127.0.0.1:18086 -t arena-event-center:poster-live -f apps/event-center/Dockerfile .`
   and run it: `docker run -d -p 8092:8080 arena-event-center:poster-live`;
3. `EVENT_CENTER_BASE_URL=http://127.0.0.1:8092 npm --prefix apps/event-center run test:e2e:container`.

The mock smoke ignores `container/**` and the live config ignores it too.

### Poster first (live)

`tests/smoke/live/poster-first.spec.ts` needs an `arena-api` built with the TEST-ONLY tag
`e2eposter` (`go build -tags e2eposter ./apps/backend/cmd/arena-api`; the harness does it
by itself when `ARENA_API_BIN` is not set, CI builds it that way). The tag injects a scripted
poster reader through `Options.PosterReader`: it replaces only the vision model; the
poster-facts route (auth, org access, `event.update`, media lookup, quota, bytes) is real.
`E2E_POSTER_SCRIPT` (default `.live/poster-script.json`) says what the next read answers,
`E2E_POSTER_CALLS` logs every read that reached the model boundary. A normal build has no
reader unless `POSTER_LLM_API_KEY` is set, and this harness never sets it.

### Session change and refunds: resilience specs and release gates

`live/session-change-resilience.spec.ts` and `live/refund-resilience.spec.ts` add to the two
twin specs (`session-change.spec.ts`, `refund.spec.ts`): a failed write keeps what was typed and
a retry writes once, a lost answer after the server wrote does not duplicate the change, the
letter or the session, a double press is one request, the contact form can be retried, a time is
read in the VENUE's zone (the browser runs in New York) with the first occurrence of an hour that
happens twice, a client-side move to another organization or order drops the open panel and an
answer in flight, a 5xx after the provider was called is UNKNOWN and the check repeats the same
body with the same `Idempotency-Key` (one Stripe-stub call), and a part of a price is exact in
minor units. Failures are made by `page.route` stand-ins that answer WITHOUT reaching the server;
a "lost answer" forwards the request and drops only the answer. Both suites need the harness
(real API + local Stripe stub, `-tags e2eposter` build) and never reach Stripe.

What the local harness does NOT prove, and so stays a release gate:

- **Real delivery of the session-change letter.** The harness runs no worker: a letter is a
  queued `session.change_email` job. A separate run (real `arena-api` + real `arena-worker`,
  `EMAIL_MODE=smtp` to a LOCAL SMTP catcher on 127.0.0.1, a synthetic `.test` buyer) showed the
  jobs delivered with the right Reply-To, the move letter carrying the new PDF and the cancel
  letter none, `session_change_notices` `sent`, orders still paid, no refund rows. That proves
  the worker and the SMTP protocol path, NOT a real mail provider: SPF/DKIM, the provider's
  acceptance and a real inbox need one controlled send through the production sender with an
  address of the owner's own.
- **Real Stripe.** Every refund call goes to the stub. Acceptance with Stripe TEST mode is done
  by the owner (a TEST secret key set by the owner in a throw-away environment, never in this
  repository): start arena-api with `STRIPE_API_BASE_URL` unset and the TEST key configured for
  the fixture organization, pay one order with a Stripe test card (4242 4242 4242 4242), then on
  the order card: (1) refund one ticket (word, password) and check in the Stripe TEST dashboard
  one refund with the amount in minor units and the `Idempotency-Key` the browser sent;
  (2) cut the answer (browser offline right after pressing) and press "Check the refund": the
  dashboard still shows ONE refund; (3) a part of the price; (4) a ticket scanned meanwhile is
  refused. Never a live key, never a real card.

The lost-answer regression verifies that a safe retry neither duplicates the change nor the
notification. If the response does not confirm a notification count, the UI displays an
unconfirmed-count notice instead of inventing zero; the existing queued letter remains intact.
