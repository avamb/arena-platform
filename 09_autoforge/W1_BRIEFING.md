# W1 briefing — read this instead of app_spec.txt (one page)

You are one coding session of wave **W1: WordPress sites (Lampyris, Vino&Co) on arena via the
Bil24-compatible gateway**. The feature you claimed is a SUB-FEATURE sized for one session.
Do exactly its Steps; sibling sub-features do the rest. Never release a claim as "too large":
implement the first steps, commit by path, push, write a short note naming what remains.

## Where things are

- Spec (design authority): `08_architecture/18_bil24_compat_wave1_specification_ru.md`.
  Read ONLY the sections your feature names (e.g. `§7.4`). Do not read it end to end.
- Backlog with file:line facts: `09_autoforge/wp_bil24_compat_backlog.md` — read only your
  epic's entry (§5) and §8 (the split). Skip §4 unless you need a code fact.
- Progress notes: `tail -150 claude-progress.txt` is enough (the top is newest).
- Gateway code: `apps/backend/internal/platform/httpserver/hbil24/` (`auth.go` fid/token,
  `cmd_catalog.go`, `cmd_cart.go`, `cmd_cart_reserve.go`, `cmd_order.go`, `cmd_tickets.go`,
  `compat_ids.go`), wire structs in `internal/adapters/bil24compat/`, mount in
  `httpserver/bil24_shims.go` (≤ 400 lines), int ids in `internal/platform/compatids/`.
- Contract harness: `apps/backend/tests/compat/bil24/` (`harness_test.go`, `seed_test.go`,
  `binding_test.go`, `wpstub/`, `testdata/wp/{requests,golden,svg,wp_receiver}/`).
  Goldens are corrected ONLY to match the spec text, never to match code.
- Holds: `hcheckout/hold_api.go`; checkout: `hfeed/public_feed_checkout.go`; tickets:
  `htickets/`; MACS: `internal/platform/macs/`; seating: `hseating/`, `domain/seating/`.
- Queries: canonical SQL in `internal/adapters/postgres/queries/*.sql`, hand-written gen
  wrappers in `internal/adapters/postgres/gen/*.sql.go` (`bank_accounts.sql.go` is the style
  exemplar); widening a row struct means updating every SELECT that feeds its scanner.
- Migrations: `internal/migrations/sql/NNNN_*.sql`, next number = current head + 1; bump
  `internal/migrations/migrations_head_test.go` `expectedHead`.

## Gates for ONE sub-feature (package level; the epic runs the full suite)

```
cd apps/backend
go.exe build ./... && go.exe vet ./...
go.exe test ./internal/platform/httpserver/hbil24/... ./internal/adapters/bil24compat/... \
  ./tests/compat/bil24/... ./tests/staticanalysis/... ./internal/migrations/... <packages you touched>
gofmt -l <changed files>            # must print nothing
```
- DB tests: `//go:build integration`, run once with
  `DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable JWT_SIGNING_SECRET=x go.exe test -tags integration <pkg>`.
- Touched `openapi.yaml` → run codegen (`go run ./apps/backend/tools/openapi30gen openapi.yaml .compat30.gen.yaml`,
  `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml .compat30.gen.yaml`,
  `node scripts/gen-ts-client.mjs`, delete `.compat30.gen.yaml`) and commit generated files;
  wire the route into `buildDriftTestServer`.
- Admin-web changes: `npm run type-check`, `npm run admin:test` (only then).
- `go.exe` works directly in bash; `go` alone is not on PATH.

## Git

Stage BY PATH only (`git add path1 path2`), never `git add .`/`-A`; check `git status --short`
for foreign files (another agent's or scratch); commit; `git push origin master`. One
commit for code, at most one for the progress note. Do not rewrite history.

## Known traps

- `panic(` needs `// allow:panic:`; non-RFC3339 layouts (`02.01.2006`, `15:04`,
  `2006-01-02T15:04:05`) need `// allow:timeformat:`; `timestamptz` only; OpenAPI 3.1 has no
  `nullable:` (use `type: [X, "null"]`); every schema property needs `description`.
- Result codes: 0 OK, 1 stale gateway session, 101 user-visible business error (localized
  `description`), -1 transient, -2 invalid request, -3 not found / out of org scope, -4 auth.
- All ids on the wire are int64 (`fid` = `sales_channels.display_number`, seats =
  `system_seat_id`, tickets = `system_ticket_id`, catalog via `compatids`). Money on the wire =
  float major units with 2 dp; in DB = bigint minor units. Dates `DD.MM.YYYY`/`HH:MM` in
  `venues.timezone`.
- One order = one session; the cart is ONE mutable reservation per (gateway session, event
  session). Cancellation drives inventory, refunds are a separate consequence.
- Windows: close files before deleting in tests; `go test ./... 2>&1 | head` hides failures —
  never judge a suite through a pipeline.
