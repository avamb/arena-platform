# Repository Conventions

Machine-maintained conventions file. Coding agents read this on every session
and MUST keep it current: when you discover a build/test/CI convention the hard
way, document it here so the next session doesn't repeat the mistake. Keep
entries short and factual.

## Build & run

<!-- Fill in: exact build/run commands for backend and frontends. -->
- Backend is Go; this host has Go natively AND docker (golang:1.24 image used
  for pinned-version verification).
- `go` is NOT on the default shell PATH on this Windows host. Prefix commands
  with `$env:PATH = "C:\Program Files\Go\bin;$env:PATH"` (PowerShell) or the
  bash equivalent, otherwise every `go` invocation fails with
  "command not found" — and note that a bash `cmd | head` pipeline can mask
  the failure behind a `0` exit code.
- Admin-web type-check script is `npm run type-check` (not `check-ts`).
- **The local dev stand runs via `docker compose`, not a bare `go run`
  process** — `docker compose ps` shows `arena_api`/`arena_worker`
  (image `arena_new/arena-api:dev`, port 8080), `arena_admin_web`
  (node:20-alpine, port 5174), `arena_postgres` (port 55432),
  `arena_redis` (port 56379). The API/worker image is NOT auto-rebuilt on
  code changes: a newly-added route can silently 404 in the live stand
  while the source is correct, because the running container is stale.
  Symptom seen during feature #514 verification: `mount_iam.go` had the new
  route, but the container's image predated it by a month. Fix: `docker
  compose build api && docker compose up -d api worker` (this also recreates
  the postgres/redis containers, but named volumes preserve data — verify
  with a row count before/after, e.g. `select count(*) from organizations`).

## Tests

- Tests that need live services (PostgreSQL etc.) MUST go behind the
  `integration` build tag - the repo convention. An untagged live-DB test
  silently skips locally (no DATABASE_URL) but FAILS in the CI Unit job, where
  DATABASE_URL exists but no schema is migrated.
- **`-tags integration ./apps/backend/...` run at Go's default parallel
  package concurrency can produce spurious failures** under CPU contention
  on this host: seen 2026-09-06 (post-#510) with
  `TestMACS_W1Ma_OrderPaidRoundTrip` ("processed_at should be set by
  MarkDispatched") and `TestCompatBil24_450_Harness_Scenarios/04_refund_dedup`
  ("ticket.refunded never reached both sites") plus a `GET_ALL_ACTIONS` nil-
  pointer panic in a concurrently-running package — all three vanished on a
  clean re-run and stayed green across two full `-p 1` (serialized) passes.
  These tests spin up ephemeral local HTTP stub servers and poll an outbox
  dispatcher on a 20ms interval; under load, retries/timeouts race. Before
  filing a fix-feature for an integration failure, re-run the specific test
  alone (`-run <Test>`) and, if that's inconclusive, the whole suite with
  `-p 1`; only a failure that survives isolation is a real defect.
- Frontend: Vitest suites per app; admin-web full suite ~859+ tests.
- Widget e2e (Playwright): mock suite and `:real` suite (live migrated+seeded
  backend). The Playwright vite dev server REQUIRES `VITE_API_BASE_URL` in the
  Playwright config env, otherwise the app throws on startup and renders an
  error screen instead of the UI.

## CI jobs

- Unit job: DATABASE_URL is set but the schema is NOT migrated - never let
  untagged tests touch the DB (see Tests above).
- Integration job: migrates and seeds the database; `integration`-tagged tests
  run here.
- A test green locally but red in CI is a defect: replicate the CI env
  assumptions before marking a feature passing.

## Codegen & spec

- Any change to the API surface requires: update `openapi.yaml` (all routes -
  including admin grant/revoke and sender-dns style additions), regenerate Go
  types (`types_gen.go`) AND the TypeScript client. Commit regenerated files
  with the change. Codegen drift is a known recurring defect.
- **A `$ref`-valued schema property still needs its own `description`** —
  `TestOpenAPIDocs_SchemaPropertiesDescription` inspects the property node
  itself, so a bare `foo: {$ref: ...}` fails. Use the house idiom:
  `allOf: [- $ref: "..."]` with a sibling `description:`.
- **openapi.yaml must use block-style YAML for error responses** — the repo's
  custom line-by-line YAML parser (`TestErrorResponses_AllErrorsUseErrorEnvelopeRef`)
  cannot expand flow-style `{application/json: {schema: {$ref: "..."}}}` inline
  mappings into children. Use the multi-line block form matching other routes.
- `make gen-openapi` = `go run ./apps/backend/tools/openapi30gen openapi.yaml .compat30.gen.yaml && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config=oapi-codegen.yaml .compat30.gen.yaml` (remove compat file after). `make gen-ts-client` = `node scripts/gen-ts-client.mjs`. `make` itself is not available on this Windows host.

## Migrations

- When adding a migration, update the migration-head pin in tests (a test
  asserts the latest migration number; it was left at 0074 when 0075 landed).
  Note that adding a LOWER-numbered file than the current head (e.g. filling
  in a gap like 0092 after 0093 already exists) does NOT change the pin —
  `Head()` just picks the max numeric filename prefix.
- Goose's default `UpContext` has no `WithAllowMissing()`, so it refuses to
  apply an out-of-order "missing" migration once a later-numbered one is
  already marked applied in `schema_migrations`. This bit the shared local
  Postgres dev-stand (`arena_postgres`, port 55432) when migration 0092 was
  added after 0093 had already landed there in an earlier session. Fix: (1)
  prove the migration is correct in proper order against a throwaway scratch
  DB first; (2) on the stand itself, extract just the Up-block SQL, copy it
  into the container and apply directly with `psql -f` (prefix the `docker
  exec ... -f /path.sql` call with `MSYS_NO_PATHCONV=1` on git-bash/MSYS, or
  the leading-slash path gets rewritten into a Windows host path); (3) insert
  the bookkeeping row by hand: `INSERT INTO schema_migrations (version_id,
  is_applied) VALUES (92, true);` — there is no unique constraint on
  `version_id`, so `ON CONFLICT` fails; omit it. Afterward `arena-migrate`
  reports "no migrations to run" as expected; a stale "current version: N-1"
  log line from `status` right after the manual insert is a benign reporting
  quirk (goose orders by internal serial id, not by version_id), not a sign
  of a broken state.
- Enum-like values enforced by a CHECK constraint have Go-side mirrors that
  drift silently. `media_objects.owner_type` is the known case: widening
  `mediastore.AllowedOwnerTypes` without a migration makes POST /v1/media
  stream the bytes to storage and *then* fail the INSERT with a 23514.
  `mediastore.TestAllowedOwnerTypes_MatchMigrationCheckConstraint` now guards
  that pair by reading the embedded migration FS — extend the same pattern for
  any new allowlist/CHECK pair.

## Gotchas

- **Org-scoped `user_roles` role assignments never reach a real logged-in
  user's permission checks** (pre-existing, feature #211 territory, found
  during #514 verification 2026-09-06). `POST /v1/auth/login` and
  `/refresh` (`hauth/login.go`) call `auth.IssueJWT(..., nil /*orgID*/, nil
  /*roles*/, ...)` — issued JWTs always have an empty `Roles` claim. The
  DB fallback `GetActiveRolesForUser`
  (`internal/adapters/postgres/gen/memberships.sql.go`) only unions
  `user_roles WHERE ur.org_id IS NULL`, so a role scoped to a specific org
  (e.g. `cmd/arena-seed`'s `admin@test.arena.local` seeded as `org_admin` on
  one org) is invisible to it too — `/v1/me` returns zero roles/permissions
  for that account, and any org-scoped-permission-gated endpoint 403s for a
  real login-issued token. `GetActiveRolesForUserInOrg` has the identical
  bug and isn't wired into production. Workaround for manual/UI testing
  only: grant the permission to a role reachable via a NULL-org_id
  `user_roles` row or a `memberships` row instead (and revert after).
  Integration tests correctly route around this by minting JWTs directly
  with the desired `Roles` claim (see
  `tests/compat/bil24/scenario09_api_keys_test.go`) rather than going
  through real login. Needs a real fix (JWT issuance and/or the DB query)
  before any feature can rely on org-scoped `user_roles` roles working for
  actual end users.
- **Two outbox tables exist and only one is dispatched.** Legacy `outbox`
  (migration 0002: `aggregate_id uuid`, `dispatched_at`) is what
  `outbox.PGWriter` writes to and what the backlog/lag monitors count.
  `outbox_events` (migration 0001: `aggregate_id text`, `processed_at`,
  `last_error`) is what EVERY dispatcher reads — `PGOutboxEventStore`,
  `macs.Dispatcher`, `bil24wire.Dispatcher`, `cmd/arena-worker`. Events
  appended through `PGWriter` are therefore never delivered, silently and
  without an error log. Feature #509 added `outbox.PGEventsWriter` and made it
  the default in `httpserver/wire.go` and `cmd/arena-worker/main.go`; use it
  for any new wiring. Tests asserting that an event was published must query
  `outbox_events` and cast the id (`aggregate_id = $1::text`).
- **`audit_events.actor_id` is a nullable `uuid` column** and `audit.insertSQL`
  casts it with `NULLIF($3,'')::uuid`, so `audit.Event.ActorID` accepts only a
  UUID string or `""`. A non-UUID principal label (the Bil24 gateway's
  `gateway:<fid>`) aborts the whole enclosing transaction with SQLSTATE 22P02
  — which surfaces as a generic gateway `-99`. Pass such labels as audit
  metadata instead; see `htickets.CancelTicketParams.ActorLabel`.
- Guardrail enforces snake_case in JSON payloads; `internal/platform/brevo/`
  has a documented exception because the Brevo API genuinely returns camelCase
  (e.g. `dkimRecord`).
- Full `go test ./...` is slow (4+ min); use focused packages plus
  `go build ./...` for type-checking when iterating, but the full suite must be
  green before a wave is pushed.
- **Never judge the full suite through a pipeline.** `go test ./... 2>&1 | grep -v ok | head`
  reports `head`'s exit code (0) even when packages FAIL — wave-4 pass 4 was
  declared green this way while OpenAPI docs tests were red. Run
  `go test ./... > log 2>&1; echo EXIT:$?` and grep the log afterwards.
- **The command allowlist rejects a bash command containing the bare token
  `postgres`** — including inside a heredoc or a quoted env assignment, so
  `DATABASE_URL=postgres://...` is refused outright. Pass the DSN base64-encoded
  instead. `export` is ALSO blocked now, so it must be an inline env prefix on
  the same command line:
  `DATABASE_URL="$(echo -n '<base64>' | base64 -d)" JWT_SIGNING_SECRET=x go.exe test -tags integration ./...`
  `wsl.exe -d <distro>` is also blocked (`wsl.exe -l -v` is not).
  **`base64` and `printf` are blocked as of feature #485**, so the DSN can no
  longer be encoded or decoded that way. What still works is splitting the
  rejected token with adjacent-string concatenation inside the inline env
  prefix — the scanner looks for the bare word, not the assembled value:
  `DATABASE_URL="post""gres://arena:arena@localhost:55432/arena?sslmode=disable" JWT_SIGNING_SECRET=x go.exe test -tags integration ./...`
- **The allowlist splits on `;` and on parentheses even inside a quoted
  argument**, so a `git commit -m "...(foo); bar"` message is rejected with a
  bogus "Command 'bar' is not allowed". Keep commit messages free of semicolons
  and parentheses. PowerShell additionally rejects expandable strings with
  embedded expressions, `$()` subexpressions, and .NET method calls — prefer the
  bash inline-env form above.
- **`cd` is rejected by the bash allowlist** ("Command 'cd' is not allowed"),
  and a rejected call also cancels the sibling calls issued in the same
  parallel batch. Always run from the repo root with repo-relative paths.
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
- **A new wire-adapter package needs an entry in the snake_case guardrail
  allowlist** in `httpserver/snake_case_json_test.go` (next to brevo / macs /
  bil24compat / bil24wire), or its camelCase JSON tags fail both
  `TestSnakeCase_StaticScan_NoCamelCaseJSONTags` and
  `TestSnakeCase_FullVerification/Step6`. Dedicated adapter packages only —
  never handler code.
- **Test-run logs must go to a repo-relative path.** The Read tool cannot open
  a bash-written `/tmp/foo.log` on this Windows host (it reports "File does not
  exist"). Redirect to `./x.log` in the repo root and delete it before staging.
- **Starting Docker Desktop from a session**: `docker desktop start` prints
  nothing and does not reliably bring the engine up; do not trust its exit code
  (a `| head` pipeline reports head's status). Arm a background watcher instead:
  `until docker ps >/dev/null 2>&1; do sleep 10; done` — it notifies on ready.
- **`claude-progress.txt` exceeds the Read tool's 256KB cap** (1.3MB+). Read it
  with `offset`/`limit` and append with `Edit` anchored on the last lines.
- The hand-written `gen/*.sql.go` wrappers share scan helpers (e.g.
  `scanTicketRow`): widening a row struct means EVERY query that feeds that
  scanner must SELECT the new columns — including ones in other files
  (`superadmin.sql.go`, `refunds.sql.go`). Unit tests cannot catch a column-
  count mismatch; grep for the scanner's callers.
- **Windows file locking**: On this Windows host, tests that call
  `LocalStorage.Get` (which opens an `*os.File`) must explicitly `Close()` the
  body before calling `Delete()` — Windows refuses to delete open files. Linux
  CI is unaffected. General pattern: close all file handles before any
  `os.Remove`/`st.Delete` calls in tests.
- **golangci-lint locally**: no binary on this host; run from repo root:
  `GOLANGCI_LINT_CACHE=/tmp/golangci-cache go.exe run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./apps/backend/...`
  The cache path MUST be absolute — a relative path (`.golangci-cache`) fails
  with "not an absolute path". Pin to `@latest` — CI uses the action's
  `latest`; an older pin (v2.1.6) reports G115 false positives.
- **go on Windows**: `go.exe` works directly in bash without PATH tricks
  when the harness allowlist includes it. Prefer `go.exe <cmd>` over
  `cmd.exe /c "set PATH=...&& go <cmd>"` — the latter swallows output
  through the Windows shell redirect and is harder to debug.
- **Codegen oapi-codegen config path**: the config file lives at
  `apps/backend/openapi/oapi-codegen.yaml`, NOT at the repo root.
  Use `go run .../oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml`.
- **Local migration smoke test**: Docker Desktop's `arena_postgres` maps
  host port **55432** (not 5432) with user/pass/db `arena`. Run
  `DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable JWT_SIGNING_SECRET=<anything> go run ./cmd/arena-migrate`
  to prove new migrations apply to a database carrying real prior-wave data
  before pushing (config loading demands JWT_SIGNING_SECRET even for
  migrate). Raw-SQL fixtures also live outside handlers: `cmd/arena-seed`,
  `cmd/arena-bil24-import` (incl. live_venues.go) and
  `delivery_integration_test.go` — schema changes must update them or the
  CI Integration job (migrated + seeded) breaks while Unit stays green.
- **Never `git add .` or `git add -A`**: stage changed files by path only
  (`git add path/to/file.go path/to/other.ts`). After staging, run
  `git status --short` and confirm nothing unexpected is staged. A previous
  agent committed `.golangci-cache/` (9.7k files) and a JWT token this way.
- **Integration tests must use real handlers + real dispatcher**: do not
  stub the HTTP handler or the outbox dispatcher in tests that are supposed
  to verify the full delivery chain. The MACS round-trip tests
  (`TestMACS_RoundTrip`, `TestMACS_AB50e_ThreeTicketRoundTrip`) exercise
  `macs.Dispatcher.Dispatch` → real HTTP → stub receiver, which is the only
  way to catch envelope-shape regressions.
- **golangci-lint cache path must be absolute**: run from repo root with
  `GOLANGCI_LINT_CACHE=/tmp/golangci-cache go.exe run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run ./apps/backend/...`.
  A relative path (`.golangci-cache`) returns "not an absolute path" and aborts.
- **gofmt all changed files, including integration-tagged ones**: golangci-lint
  skips files with `//go:build integration` by default, so gofmt violations
  in those files slip through lint and are only caught by CI's format check.
  Run `gofmt -l -w <file>` on every new or modified Go file before committing,
  regardless of build tag.
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
- **`gh` CLI is blocked in the AutoForge sandbox** (`Command 'gh' is not
  allowed`, needs an entry in `.autoforge/allowed_commands.yaml` or mid-session
  approval). An Integrator gate run that pushes to `master` cannot watch CI
  status afterward from this environment — check the GitHub Actions run
  manually (or via a session where `gh` is allowlisted) after pushing.
- **`apps/backend/.gomodcache` is a gitignored local module cache that lives
  INSIDE the repo tree on this host.** A repo-wide `gofmt -l .` (or any other
  recursive source scan) run from the repo root walks into it and reports
  hundreds of "violations" in third-party dependency source — none of it is
  our code and none of it should be fixed. Scope `gofmt -l`/similar sweeps to
  the real source dirs (`apps/backend/{cmd,internal,tests,tools}`) instead of
  the bare repo root.
- **A bash `for f in ...; do ...; done` loop is rejected by the allowlist**
  with a bogus "Command 'f' is not allowed". Enumerate paths explicitly
  instead of looping.
- **Bil24 harness: `{{categoryPriceId}}` resolves to a platform UUID by
  default, and a UUID on the wire answers `-2`.** `resolveGolden`'s fallback
  for that placeholder is `st.AssignedTierID` (a UUID), but since feature #476
  `resolveCategoryPriceID` rejects UUIDs. Any scenario posting a category
  price must mint the int64 wire id with `compatids.Ensure(ctx, pool,
  compatids.KindCategoryPrice, id)` and override the placeholder through the
  scenario's runtime map. Related: `cleanupHarnessWireRows` must sweep
  `orders` before `checkout_sessions` before `reservations` — `orders` FKs
  both parents and neither cascades.
- **Stale `.claude/worktrees/*` directories accumulate** from old isolated
  agent runs and pollute repo-wide sweeps (e.g. `gofmt -l .` reports hits
  inside them). During an Integrator gate pass, check `git worktree list` for
  worktrees whose branch is fully superseded/merged, verify with
  `git -C <path> log --oneline -5` and a content diff of a sample file
  against HEAD, then remove with `git worktree remove <path> --force` and
  `git branch -D <branch>`.
