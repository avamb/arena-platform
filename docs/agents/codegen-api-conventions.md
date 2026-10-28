# Codegen, API conventions and guardrails

openapi enum renames, snake_case guardrail, CORS, CSV export, wire-adapter allowlists. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- Guardrail enforces snake_case in JSON payloads; `internal/platform/brevo/`
  has a documented exception because the Brevo API genuinely returns camelCase
  (e.g. `dkimRecord`).

- **A new wire-adapter package needs an entry in the snake_case guardrail
  allowlist** in `httpserver/snake_case_json_test.go` (next to brevo / macs /
  bil24compat / bil24wire), or its camelCase JSON tags fail both
  `TestSnakeCase_StaticScan_NoCamelCaseJSONTags` and
  `TestSnakeCase_FullVerification/Step6`. Dedicated adapter packages only —
  never handler code.

- **`corsMiddleware` sends `Vary: Origin` on EVERY response, including one
  with no Origin header — keep it that way.** The public routes are
  cacheable (`Cache-Control: public, max-age=30`); until 2026-09-29 the
  header was added only when Access-Control-Allow-Origin was, so a copy
  fetched without an Origin (the JSON URL opened in the address bar) was
  served by the browser cache to the tickets page's cross-origin fetch a
  moment later, with no CORS headers — "Something went wrong" for 30 s.

- **An inline `enum` in openapi.yaml can silently RENAME existing Go
  constants.** oapi-codegen emits an unprefixed constant per enum value
  (`External`, `Provider`) and prefixes them with the type name only once two
  enums share a value — so adding a second `enum: [provider, external]`
  anywhere turned `openapi.External` into `RefundItemSettlementExternal`
  (2026-09-22). After `gen-openapi`, check that `git diff types_gen.go` has no
  removed lines; for a read-only aggregate, a plain `type: string` with the
  values named in the description is enough. Same spec rules the tests
  enforce: no `nullable:` (OAS 3.1 — write `type: [string, "null"]`).

- **Every CSV the platform serves goes through `internal/platform/csvexport`, and `encoding/csv` is forbidden in httpserver handlers (EC-07, 2026-10-10).** The writer is the only place that knows how Excel mangles a file: UTF-8 BOM (written as the escape `ï»¿`, never a literal byte in source), `;` separator, CRLF, every text value quoted; a digits-only value of 10+ characters, a leading-zero value and a phone starting with `+` are written as `="…"` text (barcode `4600051000001`, phone `+34600111222`), any other value starting with `=`, `+`, `-`, `@`, TAB or CR gets a `'` prefix; money is a dot decimal in minor-unit/100 with the currency in its own column, dates are `YYYY-MM-DD HH:MM` in the venue's zone (carry `// allow:timeformat`). Column labels are `csvexport.Column` keys localized en/ru/es via `LocaleFromRequest` (`?locale=`, `?lang=`, `Accept-Language`). `httpserver/hexport` (mount `mount_export.go`) streams four files — session/event `sales.csv`, `summary.csv` (built from `horders.LoadSessionSummary`, the same numbers as the JSON summary), promo `redemptions.csv` — in keyset batches of 1000 with a flush per batch, counts first and answers 413 `export.too_many_rows` above 50000 rows BEFORE the first byte, and checks the org through org-scoped header queries (a foreign row is the route's own 404). Once headers are sent a failure can only abort the connection (`panic(http.ErrAbortHandler)`), never write an error envelope. `tests/staticanalysis/csvexport_guard_test.go` fails on any `encoding/csv` import in httpserver handlers outside csvexport. Excel-parse round trips live in `csvexport_test.go`, the end-to-end file in `httpserver/export_csv_integration_test.go`. The bot's "Download CSV" button is not built yet.
