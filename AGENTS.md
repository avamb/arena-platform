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

## Tests

- Tests that need live services (PostgreSQL etc.) MUST go behind the
  `integration` build tag - the repo convention. An untagged live-DB test
  silently skips locally (no DATABASE_URL) but FAILS in the CI Unit job, where
  DATABASE_URL exists but no schema is migrated.
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
- **openapi.yaml must use block-style YAML for error responses** — the repo's
  custom line-by-line YAML parser (`TestErrorResponses_AllErrorsUseErrorEnvelopeRef`)
  cannot expand flow-style `{application/json: {schema: {$ref: "..."}}}` inline
  mappings into children. Use the multi-line block form matching other routes.
- `make gen-openapi` = `go run ./apps/backend/tools/openapi30gen openapi.yaml .compat30.gen.yaml && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config=oapi-codegen.yaml .compat30.gen.yaml` (remove compat file after). `make gen-ts-client` = `node scripts/gen-ts-client.mjs`. `make` itself is not available on this Windows host.

## Migrations

- When adding a migration, update the migration-head pin in tests (a test
  asserts the latest migration number; it was left at 0074 when 0075 landed).
- Enum-like values enforced by a CHECK constraint have Go-side mirrors that
  drift silently. `media_objects.owner_type` is the known case: widening
  `mediastore.AllowedOwnerTypes` without a migration makes POST /v1/media
  stream the bytes to storage and *then* fail the INSERT with a 23514.
  `mediastore.TestAllowedOwnerTypes_MatchMigrationCheckConstraint` now guards
  that pair by reading the embedded migration FS — extend the same pattern for
  any new allowlist/CHECK pair.

## Gotchas

- Guardrail enforces snake_case in JSON payloads; `internal/platform/brevo/`
  has a documented exception because the Brevo API genuinely returns camelCase
  (e.g. `dkimRecord`).
- Full `go test ./...` is slow (4+ min); use focused packages plus
  `go build ./...` for type-checking when iterating, but the full suite must be
  green before a wave is pushed.
- **Windows file locking**: On this Windows host, tests that call
  `LocalStorage.Get` (which opens an `*os.File`) must explicitly `Close()` the
  body before calling `Delete()` — Windows refuses to delete open files. Linux
  CI is unaffected. General pattern: close all file handles before any
  `os.Remove`/`st.Delete` calls in tests.
- **golangci-lint locally**: no binary on this host; run
  `GOLANGCI_LINT_CACHE=.golangci-cache go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`
  from `apps/backend`. Pin to `@latest` — CI uses the action's `latest`; an
  older pin (v2.1.6) reports G115 false positives on files CI accepts.
- **Local migration smoke test**: Docker Desktop's `arena_postgres` maps
  host port **55432** (not 5432) with user/pass/db `arena`. Run
  `DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable JWT_SIGNING_SECRET=<anything> go run ./cmd/arena-migrate`
  to prove new migrations apply to a database carrying real prior-wave data
  before pushing (config loading demands JWT_SIGNING_SECRET even for
  migrate). Raw-SQL fixtures also live outside handlers: `cmd/arena-seed`,
  `cmd/arena-bil24-import` (incl. live_venues.go) and
  `delivery_integration_test.go` — schema changes must update them or the
  CI Integration job (migrated + seeded) breaks while Unit stays green.
