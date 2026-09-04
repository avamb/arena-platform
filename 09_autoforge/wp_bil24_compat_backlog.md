# AutoForge backlog: WP sites (Lampyris, Vino&Co) on arena via the Bil24-compatible gateway — Wave W1

Updated: 2026-09-04
Status: planning artifact for AutoForge. This file is not an implementation.
Design authority: `08_architecture/18_bil24_compat_wave1_specification_ru.md` (**READ IT
FULLY FIRST** — every wire shape, DDL, result code and algorithm below refers to a section
of it). Owner decisions: `docs/migration/wp_sites_to_arena_wave1_2026-09-04.md` §7 (final).
Feature ids **450–469**, priorities **1007–1026**, category `WP Bil24 Compat W1`.
Importer: `09_autoforge/import_wp_bil24_compat_features.py` (refuses unless the queue head
is `(449, 1006)`). **No human gates** (owner decision 2026-09-04, evening: the wave runs
unattended). #450 builds the harness from the spec; #465 is verified against the MACS stub;
live MACS and the site staging runs (W1-S/W1-P/W1-V) stay interactive AFTER the wave.

## 1. Goal

Both WordPress sites switch from Bil24 to arena by changing ONE option
(`bil24_acf_sync.base_url_prod`, `fid`, `token`) and registering a webhook URL. arena must
reproduce 15 Bil24 commands, the sbt seating-plan SVG and the site webhooks byte-for-byte
against the PHP executable specification, while money stays in WooCommerce (`PAY_ORDER`).
On top of that: the site creates events in arena through a service key (W1-C), MACS gets
correct `order.paid` envelopes, and the platform finally has an Order aggregate and a
Customer entity.

## 2. Non-negotiable rules

- Guardrails `09_autoforge/00_AGENT_GUARDRAILS.md` apply, with the ADR-034 exception (the
  compat gateway IS the primary path for the two owned WP sites; guardrail #6 does not apply
  to them). #7 (gateway is an adapter, not the core), #14 (idempotency + audit + permission
  on every mutation), #15 (totals come from the platform), #33 (ticket ≠ credential) are
  load-bearing.
- **Golden tests are the definition of done.** A command feature is complete when its golden
  fixtures under `apps/backend/tests/compat/bil24/testdata/wp/` are green with STRICT key-set
  comparison. Never edit a golden file to make a test pass; if the spec is wrong, stop and
  ask (`needs_human_input`).
- Decisions in `09_autoforge/WAVE4_RUNBOOK.md` "Decisions already made" stand: one order =
  one session (`checkout_sessions.reservation_id` stays a single NOT NULL FK — the cart is
  ONE mutable reservation per session), cancellation drives inventory not the refund,
  `sold → unavailable` forbidden, MACS is not touched.
- Static gates (`apps/backend/tests/staticanalysis/*`): top-level `httpserver/*.go` ≤ 400
  lines; `bil24compat` must not import `internal/platform/httpserver`; `panic(` needs
  `// allow:panic:`; non-RFC3339 layouts need `// allow:timeformat:`; `timestamptz` only;
  OpenAPI 3.1 (no `nullable:`), drift enforced both ways, every new route documented and
  wired into `buildDriftTestServer`; migration head pin in
  `internal/migrations/migrations_head_test.go:43` bumped with every migration.
- Money: bigint minor units in DB, `float` major units (2 dp) on the Bil24 wire. IDs: int64
  on the wire, always (spec §4). Dates: `DD.MM.YYYY` / `HH:MM` / naive `showTime` in
  `venues.timezone`; RFC3339 with offset elsewhere.
- No mock data in production paths. Fixtures live in `testdata/`.

## 3. The gate — every feature, no exceptions (repeat in every feature description)

`go test ./...` full (no pipelines — `go test ./... > log 2>&1; echo EXIT:$?`), golangci-lint
with gofmt (absolute cache path), `npm run admin:test`, `npm run type-check`, admin build,
codegen drift (`openapi30gen` + `oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml`
+ `node scripts/gen-ts-client.mjs`, remove `.compat30.gen.yaml`), migration head pin, DB tests
behind `//go:build integration`, local migrate against Docker PG (`postgres://arena:arena@localhost:55432/arena`),
**never `git add .` / `-A`** (stage by path, check `git status --short`), commit AND push,
verify CI with `gh run view`. Never weaken a test to get green.

## 4. Code map facts that bind this wave (verified on c689d44)

- Gateway: `internal/adapters/bil24compat/{wire,result_codes,translate}.go` (wire),
  `internal/platform/httpserver/hbil24/{handler,bil24_compat,schema}.go` (1 920-line
  dispatcher — split it), `httpserver/bil24_shims.go` (mount, 255 lines). Route
  `POST /compat/bil24/json`, flag `BIL24_COMPAT_ENABLED` (`config.go:242`),
  `BIL24_REQUIRE_TOKEN` (`config.go:248`, production refuses `false`). Not in `openapi.yaml`.
- Today: read commands are unauthenticated and cross-tenant (`bil24_compat.go:311`,
  `events.sql:110-111` filters `visibility` only); `fid` must be a UUID
  (`translate.go:37-49`); `SCAN_TICKET` uses `GetSalesChannelByIDGlobal` (`channels.sql:17-24`);
  responses are `map[string]any` (no named structs); `CREATE_ORDER_EXT`/`CANCEL_ORDER`/
  `ADD_PROMO_CODES` return -5; unknown command returns -1.
- Holds: `hcheckout/hold_api.go` `CreateSeatedHold:151`, `CreateGAHold:276`, `ReleaseHold:380`;
  price lock `reservation_ga_items.unit_price` (0063/0087) via `priceresolve.ForTier`.
  `ComputePricingLines` at `hcheckout/handler.go:185` with `PricingRules` (bp).
- Checkout chain: `hfeed/public_feed_checkout.go` (buyer name/phone validated then dropped
  at `:308-338`), `confirmPublicCheckout:985`, payment webhook enqueues
  `checkout.issue_tickets` (`hcheckout/payment_intents.go:809-819`),
  `htickets.IssueTicketsForCheckout` (`tickets.go:157`, `holder_email` always NULL).
- Cancel: `POST /v1/tickets/{id}/cancel` → `htickets/cancel.go:108`, refund_mode
  none|manual|automatic, emits `v1.ticket.cancelled` (`hscanner/scanner_events.go:86`).
  Bug: `RevokeTicketArtifactsTx` (`cancel.go:568`) revokes `"qr"` but the CHECK says
  `static_qr` → QR never revoked.
- MACS: `internal/platform/macs/{export,dispatcher}.go`, stub `macs/stub`, subscriber
  `hcatalog/macs_webhook.go`, worker fan-out `cmd/arena-worker/main.go:173-176`
  (`multiDispatcher`), outbox `MaxAttempts 30`. `actionEvent.id` = UUID hash of the EVENT
  (`17_macs:72`), `barcodeFormat` = literal `EAN-13` over a 64-hex token.
- Seating: `hseating/layout_svg.go` `RenderBSSLayoutSVG:211` (namespace
  `http://bil24.pro/sbt`, swatch circles, states 0/1/3/4), `domain/seating/svg_import.go`
  `ImportSVG:66` (Inkscape conventions), versions `hseating/versions.go` (`svg` XOR
  `geometry`), bind `hseating/bind.go:129` (`category_tier_map`), 0088 seat ids retired on
  rebind. Public `GET /v1/event-sessions/{id}/layout.svg` (`mount_seating.go:27`).
- Schema: `sales_channels.display_number` (0072), `settings jsonb` (0045),
  `fee_percent numeric(5,2)` (0010); `webhook_subscribers.kind/org_id` (0089);
  `ticket_credentials.type CHECK (static_qr,pdf)` (0027); `barcodes(authority_id,
  external_ref)` UNIQUE (0029); `venues.timezone` is the ONLY timezone column (0050:41);
  `sessions.currency` (0081); permissions are `.create/.read/.update/.delete/.publish`
  (there is NO `event.write`). No `api_keys`, `orders`, `customers` tables. Next migration
  **0090**, head pin currently `0089_macs_webhook_subscribers.sql`.
- Auth: `auth.Actor{Type}` has `service` with no issuer; `enforceOrgMembership`
  (`server_orgauth.go:56`) + sub-package twins `hcatalog/orgauth.go:46`, `hiam:44`,
  `hpayments:43`, `hbankaccounts:45`, `hseating/authz.go:33`.
- i18n: `internal/platform/i18n/locales/en.toml` only.
- Site facts (PHP): fid is `(int)`; fid/token/locale auto-injected on every command
  (`class-bil24-client.php:12-14`); `resultCode` absent = success except GET_ALL_ACTIONS;
  `resultCode 1` = stale session → re-create user; RESERVATION response `seatList` = whole
  cart, quantity = row count; `holderStatus` `NEVER_USE`/`REFUND` in exports, `REFUNDED`
  compared by consumers; webhook receiver has NO auth, 400 without `type`/`data`, dedups
  `ticket.refunded` by `data.id`; MACS success = body `{"status":"OK"}`.

## 5. Features

Dependencies are strict: a feature may start only when every dependency has `passes=1`.

---

### 450 — W1-0 [MAJOR]: contract fixtures, golden harness, wpstub — built from the spec (К6)

**Category:** WP Bil24 Compat W1.

**Problem.** `tests/compat/bil24/testdata/vinoandco_fixtures.json` (20 synthetic fixtures,
no RESERVATION/GET_CART/CREATE_USER/PAY_ORDER) and `BEHAVIOR_DIFFERENCES.md` are stale
(claim CREATE_ORDER_EXT returns 0). Every later feature is judged by golden fixtures that do
not exist yet. The PHP wire shapes are already transcribed in spec §7 and the pseudonymized
real order sample is in `testdata/wp/bil24_orders_pseudonymized.json`.

**Steps.** (1) `testdata/wp/requests/<COMMAND>/<case>.json` verbatim from spec §7 (15
commands, 4 RESERVATION shapes, both promo keys, string/int `orderId`); (2)
`testdata/wp/golden/<COMMAND>/<case>.json` per §7 with placeholders resolved by the harness,
STRICT key-set comparison; (3) `harness_test.go` (integration tag) booting the real server
with seeded org/channel/venue+TZ/seated + GA sessions/tiers/promo/subscribers, scenarios
1–10 of §15.3 as `t.Run` steps that `t.Skip` with the feature id until implemented; (4)
`wpstub` replaying `bil24-notification-receiver.php`; (5) golden SVG skeleton for §8 from the
Akropolis geometry (synthetic, header says so); (6) BINDING key-set test of the sample
against §9.3; (7) `BEHAVIOR_DIFFERENCES.md` rewritten. No command implementation here.

**Done when.** Harness + wpstub green in the Integration job; every scenario step present
(skipped with its feature id); all later features can reference existing golden files.

---

### 451 — W1-A1 [MAJOR]: per-channel gateway, integer `fid`, token on every command, credential provisioning (К1, К2)

**Problem.** `bil24_compat.go:311` serves every org's events to any caller; `fid` must be a
UUID (`translate.go:37`) while the site sends `(int)`; read commands skip
`validateGatewayToken` (`:1245`); `SCAN_TICKET` resolves the channel globally (`:1399`);
`gateway_token_hash` has no admin API.

**Steps.**
1. `sales_channels.settings.gateway = {enabled, token_hash, token_rotated_at, default_locale}`
   (read legacy `gateway_token_hash` as fallback). `fid` → `sales_channels.display_number`
   → channel → `org_id`; every command (incl. GET_ALL_ACTIONS/GET_SEAT_LIST/GET_SCHEMA/
   GET_ORDER_INFO) validates fid+token when `requireToken`; disabled/unknown → -4.
2. All queries the gateway makes take `org_id`; `SCAN_TICKET` resolves org via
   `barcodes → tickets → sessions → events.org_id` and rejects cross-org; delete
   `GetSalesChannelByIDGlobal`.
3. `PUT/GET/DELETE /v1/organizations/{org_id}/channels/{id}/gateway-credential`
   (`channel.update`, `X-Admin-Reason`; token returned once, response
   `{fid, token, base_url, image_url, rotated_at}`), OpenAPI + codegen, audit event
   `v1.channel.gateway_credential.rotated`.
4. Admin-web: "Bil24-compatible gateway" section on the channel page modelled on the MACS
   webhook section (`organizations.tsx:1041-1060`), secret shown once, tests.
5. Tests: tenant isolation (org B events never appear for org A fid), token on reads,
   SCAN_TICKET cross-org → -3, provisioning 200/403/404, admin-web tests.

**Done when.** Golden `GET_ALL_ACTIONS/isolation.json` and `SCAN_TICKET/cross_org.json`
green; a channel with `enabled=false` gets -4 on every command.

---

### 452 — W1-A2 [MAJOR]: migration 0090 `compatibility_id_map`, `compatids` package, integer IDs on the wire (К3)

**Problem.** Every ID the gateway emits is a UUID string (`bil24_compat.go:324`, `:568`,
`schema.go:141`); the site casts to `(int)` → 0. `compatibility_id_map` is a TODO in
`translate.go:9,35,46`.

**Steps.**
1. Migration `0090_compatibility_ids.sql` exactly as spec §3.1 (sequence from 1e9, map
   table, `session_seats.system_seat_id_source`); bump head pin; gen queries
   (`compat_ids.sql`, hand-written gen file in the `bank_accounts.sql.go` style).
2. Package `internal/platform/compatids`: `Ensure`, `EnsureMany`, `Resolve`,
   `RegisterExternal` (rejects ≥1e9 for `bil24`), all transactional, unit + integration tests.
3. `bil24compat.TranslateLegacyID` → parses int64 (string or number) and resolves through
   the map; UUID input no longer accepted from clients. Named response structs in
   `bil24compat` for every command of spec §7 with `int64` ids (replace `map[string]any`).
4. Rewire `GET_ALL_ACTIONS` (still flat for now), `GET_SEAT_LIST`, `GET_SCHEMA`,
   `RESERVATION`, `UN_RESERVE`, `SCAN_TICKET` to emit/accept ints: `seatId` =
   `session_seats.system_seat_id`, `ticketId` = `system_ticket_id`.
5. Tests: idempotent minting (two calls → same id), external registration, ranges, the
   `bil24compat_layout_188_test.go` sentinels still satisfied.

**Done when.** No UUID appears in any gateway response (golden strict); `testdata/wp/requests`
with numeric ids resolve.

---

### 453 — W1-A3 [NORMAL]: result codes 1/101/-1 and localized descriptions (К5)

**Problem.** `-1` means "unknown command" (`bil24_compat.go:268-277`) but the site treats
`1` as stale session and shows `description` to the buyer; only `en.toml` exists.

**Steps.** Add `ResultCodeSessionExpired = 1`, `ResultCodeUserVisible = 101`,
`ResultCodeTransient = -1`; unknown command → -2; update `result_codes.go` header comment and
tests (`bil24_compat_157_test.go`, `bil24_374_test.go`) — this is the documented decision of
spec §6, not a weakening. Add `locales/ru.toml`, `he.toml`, `cs.toml` with all `bil24.*`
keys of §6; locale negotiation `ru-RU→ru`, unknown → channel default → `en`; description
for 0 = `"OK"`. Table-driven tests for every key in every locale (no missing keys).

**Done when.** Golden error cases (`RESERVATION/seat_taken_ru.json`, `…_he.json`) green.

---

### 454 — W1-A4 [MAJOR]: customers (0091), identity resolution, `CREATE_USER`, gateway sessions (C6 part 1)

**Problem.** No customer exists; buyer data is dropped (`public_feed_checkout.go:308-338`);
the gateway has no userId/sessionId.

**Steps.**
1. Migration `0091_customers.sql` exactly as spec §3.2 (+ head pin, gen queries).
2. Package `internal/platform/customers`: normalization (email lower/trim, phone E.164 via
   `github.com/nyaruka/phonenumbers` with region from the org's venue country), `Resolve`
   per spec §12.2 (strong-key conflict → merge candidate, never auto-merge), `Touch`,
   `MarkVerified`, `LinkOrg`. Unit tests: 12+ cases incl. the family-with-one-phone case.
3. `CREATE_USER` per spec §7.3 (`gateway_sessions`, 30-day sliding TTL, 43-char token);
   session lookup helper used by all later commands returning result code 1 when missing/
   expired; `last_seen_at`/`expires_at` refresh.
4. Public feed checkout: persist `Buyer{}` via `customers.Resolve` (stop discarding name/
   phone); `reservations.customer_id`.
5. `GET /v1/organizations/{org_id}/customers` (+`/{id}`) per spec §12.3 with `customer.read`;
   OpenAPI + codegen; admin-web minimal list/card (org scope).

**Done when.** Golden `CREATE_USER/*` green; a second CREATE_USER with the same email
returns a new sessionId but the same `userId`.

---

### 455 — W1-A5 [MAJOR]: session cart — mutable hold, 4 RESERVATION shapes, `GET_CART`, `UN_RESERVE_ALL` (К4)

**Problem.** One RESERVATION = one immutable reservation with a delta response
(`bil24_compat.go:1049-1825`); the site expects an accumulating per-session cart whose
response lists the whole cart and whose quantity is the row count.

**Steps.**
1. `hcheckout/hold_api.go`: `ExtendHold(tx, reservationID, seats|ga)`, `ShrinkHold`,
   `RefreshHoldExpiry`, `ReacquireHold` reusing `LockSessionSeatsForHold`, `AllocateGAUnitsTx`,
   `IncrementSessionSeatStatusVersion`, `reservation_ga_items` price lock. Property-style
   integration test: concurrent extend/shrink never double-holds a seat/unit.
2. `RESERVATION` per spec §7.4: RESERVE/UN_RESERVE by `categoryList` or `seatList`,
   `UN_RESERVE_ALL`, one reservation per (gateway session, event session), currency guard,
   TTL refresh, `cartTimeout`, response = whole cart with financial fields.
3. `GET_CART` per spec §7.5 (`totalSum` only, per-actionEvent `chargePercent` =
   `sales_channels.fee_percent`, discount by session promo codes — until 457 lands the
   discount is 0).
4. Errors mapped to 1/101/-2/-3 with localized descriptions; GA `pwyw` → 101.
5. Widget/public schema still see the holds (seat_status_version bumps) — regression tests.

**Done when.** Golden `RESERVATION/{reserve_category,reserve_seat,unreserve,unreserve_all,
seat_taken,sold_out}.json` and `GET_CART/*` green; harness scenario 3 passes.

---

### 456 — W1-A6 [MAJOR]: Order aggregate (0092) and `ordering` package for all three checkout surfaces (A1)

**Problem.** There is no `orders` table; the order is `checkout_sessions` + `tickets`;
`GET /v1/admin/orders` lists checkout sessions; `holder_email` is never written.

**Steps.**
1. Migration `0092_orders.sql` exactly as spec §3.3 (pg_trgm, orders/order_items/
   order_events, `tickets.order_id`, permissions, partial unique index for one open order
   per customer+session); head pin; gen queries (`orders.sql`).
2. Package `internal/platform/ordering`: `CreateOrderFromCheckout`, `MarkPaid`, `Cancel`,
   `Expire`, `ReconcileLines` (spec §14.1/§7.7 step 3); worker job `order.expire_sweep`.
3. Wire into `hcheckout` confirm (`/checkout/{id}/confirm`), `hfeed.confirmPublicCheckout`
   (`public_feed_checkout.go:985`) and the payment webhook (`payment_intents.go:809`) →
   `MarkPaid`; `IssueTicketsForCheckout` sets `tickets.order_id`, `order_items.ticket_id`,
   `holder_email = orders.buyer_email`; emit `v1.order.paid` after the last ordinal.
4. `GET /v1/organizations/{org_id}/orders` (search `q` over buyer_* via trgm, filters),
   `GET …/orders/{id}`, `POST …/orders/{id}/cancel`; `GET /v1/admin/orders` reads `orders`.
   OpenAPI + codegen. Admin-web: org-scoped "Orders" page (table + drawer with items and
   timeline), permission-driven nav, tests.
5. Integration tests: public feed purchase creates one order with buyer fields; expire
   sweep; one-open-order rule.

**Done when.** A public feed checkout produces `orders` + `order_items` rows with buyer
name/phone/email; `v1.order.paid` appears once per order in `outbox_events`.

---

### 457 — W1-B1 [MAJOR]: `CREATE_ORDER_EXT`, `ADD_PROMO_CODES`, `CHECK_KDP`, `GET_ORDER_INFO`

**Problem.** All four return -5 or a stub (`bil24_compat.go:249-267`, `:650`).

**Steps.**
1. `ADD_PROMO_CODES` / `CHECK_KDP` per spec §7.6 (both list keys, ≤10, new/exist/error,
   `gateway_sessions.promo_codes`, first applicable code wins; document the single-code
   limitation in `BEHAVIOR_DIFFERENCES.md`). Discount now visible in `GET_CART`.
2. `CREATE_ORDER_EXT` per spec §7.7: session → reservation of the action event (create
   empty + fill from lines when absent) → `ReconcileLines` → `customers.Resolve(fullName/
   phone/email)` → one-open-order rule → checkout session confirm with
   `PricingRules{PlatformFeeBP: fee_percent×100}` + promo → `orders` pending_payment with
   `external_ref` → response `{orderId, externalOrderId, sum, discount, charge, totalSum,
   currency, expiration}`. Client `total`/`chargePercent`/`expectedPrice` only logged in
   `order_events.created.payload.client_reported`.
3. `GET_ORDER_INFO` per spec §7.8 (strict mode, `order` object, `userMessage`).
4. Errors: -2 empty lines/orderId, 101 wrong-session line, 101 sold out on reconcile.

**Done when.** Golden `CREATE_ORDER_EXT/{ga,seated,promo,repeat_same_order}.json`,
`ADD_PROMO_CODES/*`, `CHECK_KDP/*`, `GET_ORDER_INFO/*` green; harness scenario 6 passes.

---

### 458 — W1-B2 [MAJOR]: `PAY_ORDER`, `GET_TICKETS_BY_ORDER`, `SEND_TICKETS_TO_EMAIL`, `CANCEL_RESERVATION`, `CANCEL_ORDER`

**Problem.** No "external payment confirmed" path exists; `payment_provider_configs` has a
`manual` slug (`payment_configs_types.go:30-36`) but nothing uses it.

**Steps.**
1. `PAY_ORDER` per spec §7.9: idempotent; hold reacquire on expiry (`ReacquireHold`),
   `manual_review` + 101 + audit alert on failure; amount mismatch recorded not blocked;
   tx: `payment_intents(provider='manual', state='succeeded')`, checkout completed,
   reservation converted, promo redemption, `ordering.MarkPaid`, customer org link +
   `verified_at`; after commit synchronous `IssueTicketsForCheckout` + fallback job;
   no platform delivery e-mail unless `settings.gateway.platform_email`.
2. `GET_TICKETS_BY_ORDER` per spec §7.10 (`pdfUrl` via new required `PUBLIC_BASE_URL`,
   string or int `orderId`, empty lists before issuance).
3. `SEND_TICKETS_TO_EMAIL` (delivery jobs), `CANCEL_RESERVATION`, `CANCEL_ORDER` per §7.11–7.12.
4. Config: `PUBLIC_BASE_URL` required when `BIL24_COMPAT_ENABLED` (config test), `.env.example`.
5. Integration tests: pay → tickets issued in-request; pay after expiry with free seats →
   reacquired; with seats taken → manual_review; double PAY_ORDER → 0 and one ticket set.

**Done when.** Golden `PAY_ORDER/*`, `GET_TICKETS_BY_ORDER/*`, `CANCEL_*/*` green; harness
scenarios 2 and 5 pass end-to-end up to the outbox event.

---

### 459 — W1-B3 [MAJOR]: `GET_ALL_ACTIONS` full nested catalog

**Problem.** Flat `actionList` without `actionEventList`, cities, venues, categories, dates
in RFC3339 (`bil24_compat.go:298-348`).

**Steps.** Implement spec §7.1 exactly: country/city/venue lists via `compatids`, published
events of the org with upcoming scheduled sessions, `day`/`time` in `venues.timezone`
(skip + warn when NULL), `sellEndTime`, `availability` remaining, `categoryLimitList` = GA
tiers only with `placement:false`, `seatingPlanId = actionEventId` for seated/hybrid,
`minPrice/maxPrice`, posters from session media, `age`, `organizerId/Name`, localized
name/description. One SQL round-trip per list (no N+1 on sessions), integration test with
100 events × 3 sessions under 200 ms.

**Done when.** Golden `GET_ALL_ACTIONS/{ga,seated,hybrid,dst_jerusalem,no_timezone}.json` green.

---

### 460 — W1-B4 [NORMAL]: `GET_SEAT_LIST` full form

**Problem.** `availableCount = capacity`, no `placement`/`available`/`location`, BSS ints
(`bil24_compat.go:470-592`).

**Steps.** Implement spec §7.2: `currency`, `categoryList` with tri-state `placement`,
`availability` remaining, `tariffIdMap {}`; `seatList` with `available` bool, `location`,
GA units as pseudo-seats for hybrid, `[]` for pure GA, `availableOnly` filter; -3 outside
scope. Keep `GET_SCHEMA` consistent (int `seatId`).

**Done when.** Golden `GET_SEAT_LIST/{seated,hybrid,ga,available_only}.json` green.

---

### 461 — W1-B5 [NORMAL]: `GET /compat/bil24/image?type=seatingPlan` in sbt/1.0 format (Ф1)

**Problem.** Only `/v1/event-sessions/{uuid}/layout.svg` exists, namespace
`http://bil24.pro/sbt`, swatch circles instead of `<metadata>`, states 0–5
(`hseating/layout_svg.go:84-381`); the site fetches by int `actionEventId` + `fid` and reads
namespace `http://www.w3.org/2015/sbt/1.0` (`bil24-seat-picker.js:389-394`).

**Steps.** `hseating.RenderSBT10SVG` per spec §8 (metadata categories with `sbt:index`,
`sbt:cat` = index, states 1/4, `viewBox`, GA zones as decor); route mounted in
`bil24_shims.go` without JWT, `fid` → channel → org, published session only, ETag/304;
OpenAPI; golden SVG test (canonical XML compare) + a DOM-level test that replays the JS
parsing rules (`attrNS`, `catByIndex`, ancestor `sbt:sect`).

**Done when.** Golden `image/{seated,hybrid}.svg` green; 404 for GA sessions / other org.

---

### 462 — W1-B6 [NORMAL]: EAN-13 credentials, barcode federation, revoke fix (Ф2)

**Problem.** No EAN-13 generator exists; `"EAN-13"` is a literal over a 64-hex token
(`macs/export.go:475-478`); `RevokeTicketArtifactsTx` revokes `"qr"` (`cancel.go:568`) so
`static_qr` survives cancellation.

**Steps.** Migration `0093_ean13_credentials.sql` (spec §3.4, head pin); package
`internal/platform/barcodes/ean13` (`Encode("21", id)`, `Valid`, tests against the real
`2402604868419` and 100 generated codes); issuance writes `ticket_credentials(ean13)` +
`barcodes(platform authority)`; revoke fixes (`static_qr`, `pdf`, `ean13`, barcodes
revoked); `SCAN_TICKET` and `/v1/scanner/validate` accept EAN-13; arena PDF prints the number
under the QR; backfill job `tickets.backfill_ean13` for stand data.

**Done when.** Every new ticket has a valid EAN-13 in `ticket_credentials` and `barcodes`;
cancelling revokes all three credential types (test asserts `revoked_at` on each).

---

### 463 — W1-B7 [MAJOR]: `orderexport` projection, `bil24wire` encoder, `bil24_wp` webhook subscriber and dispatcher (Ф3, Ф4)

**Problem.** Webhooks exist only for MACS (int `holderStatus`, event UUID hash, no order
envelope); the site needs `{type, data}` with the 36/17/14-key Bil24 shapes.

**Steps.**
1. Migration `0094_wp_webhook_subscribers.sql` (spec §3.5, head pin).
2. `internal/platform/orderexport`: move the DB projection out of `macs/export.go`
   (`Order`, `Ticket`, proration, sold price, discountReason) into a neutral struct; `macs`
   becomes an encoder over it (no behaviour change for MACS in this feature — golden
   `sample_tickets.json` stays binding).
3. `internal/platform/bil24wire`: encoder per spec §9.3 (string `holderStatus`
   `NEVER_USE`/`REFUND`, `category` string, `seatLocation` null/object, naive `showTime`,
   `actionEvent.id` = session `actionEventId`, `refundDate` RFC3339 offset, full key sets)
   with a BINDING key-set test against `testdata/wp/bil24_orders_pseudonymized.json`.
4. Dispatcher per spec §9.2: event mapping table, envelope `{id, created, type, data}`,
   `X-Arena-Signature` optional, 2xx = success, third member of `multiDispatcher`;
   `v1.order.cancelled`, `v1.event.published/updated`, `v1.session.updated/cancelled`
   producers in `ordering`/`hcatalog`.
5. `PUT/GET/DELETE /v1/organizations/{org_id}/channels/{id}/wp-webhook` with synchronous
   `test` delivery in the PUT response; OpenAPI; admin-web section next to 451's.
6. Integration: real cancel handler → outbox → dispatcher → `wpstub` receives
   `ticket.refunded` once (retry on 503, dedup on replay); `order.paid` carries all tickets.

**Done when.** `wpstub` scenarios in `testdata/wp/wp_receiver/` green; MACS golden unchanged.

---

### 464 — W1-B8 [NORMAL]: `REFUND_TICKET` gateway extension

**Problem.** Refund from the site's event centre today ends with a manual step in the Bil24
manager (`class-lops-tickets.php:6-13` four-state model).

**Steps.** Command per spec §7.13 wrapping the cancel transaction of `htickets/cancel.go:174-276`
with `refund_mode='manual'`, actor `gateway:<fid>`, org scoping via the order's channel,
idempotent on already-cancelled, `refundPrice` → `tickets.refund_price`, `orders.status`
refunded/partially_refunded, `order_events.ticket_refunded`; then the standard
`v1.ticket.cancelled` → `ticket.refunded` to the site and MACS.

**Done when.** Golden `REFUND_TICKET/{ok,repeat,other_org}.json` green; harness scenario 4.

---

### 465 — W1-M [MAJOR]: MACS envelope fixes (М1–М5) over `orderexport`

**Problem.** MACS gets `order.paid` as a single ticket (from `v1.scanner.ticket.issued`),
any 2xx counts as delivered although MACS answers 200 `{"status":"Error"}`, and
`actionEvent.id` is a UUID hash of the event (`17_macs:72`).

**Steps.** Spec §10: M1 `order.paid` from `v1.order.paid` with `{id, status:"PAID",
ticketList}` (single-ticket synthetic order for complimentary tickets); M2 success only on
2xx **and** `{"status":"OK"}`; M3 `actionEvent.id` = session `actionEventId`, `actionId`;
M4 EAN-13 barcode, callback URL must end with `/api/_wh/tickets`; M5 stub tightened,
AB-50g/50h/50i tests and `sample_tickets.json` updated (binding), contract doc reconciled
with a runbook note on the key change. Live MACS verification is a later interactive item.

**Done when.** Round-trip delivers `order.paid` with N tickets once; a stub
`{"status":"Error"}` produces a retry (`next_attempt_at` set).

---

### 466 — W1-C1 [MAJOR]: organization API keys and service principal (C1, ADR-029/038)

**Problem.** No non-JWT server-to-server credential exists except feed tokens
(`agent_feed_tokens`); `auth.ActorType` `service` has no issuer.

**Steps.** Migration `0095_api_keys.sql` (spec §3.6, head pin); package
`internal/platform/apikeys` (issue `ak_<prefix12>_<secret43>`, bcrypt, lookup by prefix,
scope validation excluding `platform.*`/`admin.*`/`api_key.manage`); middleware in
`applyAuth` producing `Actor{Type: service}` with permissions = scopes; every
`enforceOrgMembership`/`orgauth.go` twin accepts a service actor only for `api_keys.org_id`
(table-driven test over all five twins); rate limit 600/min by key id; audit actor
`api_key:<id>`; `POST/GET/DELETE /v1/organizations/{org_id}/api-keys` (`api_key.manage`,
`X-Admin-Reason`), OpenAPI; admin-web "API keys" tab (issue with scope picker, shown once,
revoke). Integration: a key with the §13.1 scope set can create event → session → tiers →
publish; a key of org A gets 403 on org B; revoked → 401.

**Done when.** The §13.4 "no seats" flow runs end-to-end under a key in an integration test
and the event shows up in `GET_ALL_ACTIONS` for the channel.

---

### 467 — W1-C3 [MAJOR]: sbt-SVG importer and `POST /v1/organizations/{org_id}/imports/bil24-session` (C3-arena)

**Problem.** `domain/seating/svg_import.go` parses Inkscape conventions only; nothing can
register Bil24 ids for an imported session; a rebind retires seat ids (0088:72-77).

**Steps.** `seating.ImportSBTSVG` per spec §13.3 (geometry `seats[].external_id`,
`categories[].external_id`, canonicalization/checksum aware; tests on the real fixture from
450 and a synthetic one); materialization uses `external_id` as `system_seat_id`
(`system_seat_id_source='bil24'`) and re-applies it on rebind; endpoint per spec §13.2
(venue/city/country upsert with mandatory timezone for new venues, event/session/tiers upsert
via `compatids.RegisterExternal`, plan + version + bind + blocked seats for `available:false`,
`publish`, warnings, 409 when a session with sales would change its seat set), permission
`import.bil24_session`, OpenAPI + codegen. Integration: import twice → `created:false`, same
ids; `GET_SEAT_LIST` then returns Bil24 `seatId`s; `image` returns the same `sbt:id`s.

**Done when.** Harness scenario 8 passes; a Bil24-imported hybrid session sells through the
full 457/458 flow with original ids in `order.paid`.

---

### 468 — W1-C7 [NORMAL]: customer database import tool with dry-run (C7)

**Problem.** Owner decision #11 requires loading Bil24/WooCommerce/GSheets/Brevo customer
bases per organizer; no tool exists. (Running the import on real data stays interactive.)

**Steps.** Migration `0096_customer_imports.sql` (spec §3.7, head pin); worker job
`customer.import` (modes dry_run/apply) with parsers for `bil24_orders_json` (fixed mapper
of spec §12.4) and CSV with `mapping.columns`; every row through `customers.Resolve` without
auto-merge, `row_hash` idempotency, consent `source='import:<label>'` never verified,
`customer_org_links(source='import')` + aggregates, `customer_attributes`; endpoints
`POST /v1/admin/customer-imports`, `POST …/{id}/dry-run`, `POST …/{id}/apply`, `GET …/{id}`,
`GET …/{id}/rows` (`platform.superadmin`, `X-Admin-Reason`); OpenAPI; admin-web page under
Platform (upload via media, mapping form, report). Tests on `testdata/wp/bil24_orders_pseudonymized.json`
(68 orders): counts in the dry-run report, apply twice → all `matched`.

**Done when.** Dry-run and apply on the real sample produce the expected report and are
idempotent; no marketing consent is `verified` after import.

---

### 469 — W1-D [NORMAL]: documentation, OpenAPI completeness, ADRs, runbook

**Steps.** `08_architecture/11_architecture_decision_log_ru.md`: ADR-034…038 as in spec
§17; `01_api_compatibility_gateway_ru.md` answers its own "Open Questions" from the spec;
`tests/compat/bil24/BEHAVIOR_DIFFERENCES.md` current; `17_macs_integration_contract.md`
updated after 465; ops runbook `docs/ops/bil24_gateway.md` (provision a channel, register
the WP webhook, rotate, read dead letters, switch a site, roll back by URL); `AGENTS.md`
conventions learned in this wave; `openapi.yaml` covers `/compat/bil24/json` (oneOf by
`command`) and `/compat/bil24/image`; `.env.example` + `deploy/` for `PUBLIC_BASE_URL`.

**Done when.** Docs tests (`openapi_docs`, guardrail greps) green; runbook reviewed.

## 6. Out of scope (this wave)

Payment adapters in runtime, taxes, order-history migration, legacy Bil24 widget commands
(`AUTH`, `GET_ORDERS`, … stay -5), seat-map editor (wave 1.1, C4), organizer shell, any change
inside MACS, PHP changes on the sites (contract only, spec §13.4), buyer accounts/OTP/Telegram.

## 7. Acceptance for the whole wave

Harness scenarios 1–10 of spec §15.3 green in CI (Integration job); `go test ./...` green
without pipelines; lint 0 issues; codegen no drift; admin/widget suites green; stand
redeployed with migrations 0090–0096 applied to the existing stand database; Lampyris
staging (`staging.lampyrisevents.com`) completes a GA purchase, a seated purchase, a refund
and a MACS import against the stand (interactive W1-S).
