# Bil24-compatible gateway — operations runbook

Covers day-2 operation of the wave-1 compat gateway (`hbil24`/`bil24compat`, mounted at
`/compat/bil24/*`) for the two owned WordPress sites, Lampyris and Vino&Co. Design authority:
`08_architecture/18_bil24_compat_wave1_specification_ru.md`; wire-level differences from real
legacy Bil24: `apps/backend/tests/compat/bil24/BEHAVIOR_DIFFERENCES.md`.

All admin endpoints below require an authenticated platform/org operator token and, where
noted, a non-empty `X-Admin-Reason` header (see `docs/ops/superadmin_org_access.md` for the
general pattern — every write here is audit-logged the same way).

## 1. Provision a channel credential (fid + token)

A WordPress site authenticates as one `sales_channels` row. `fid` is that channel's
`display_number` (bigint, unique) — the PHP plugin does `(int)$o['fid']`, so it must already
be a small integer, never a UUID.

```
PUT /v1/organizations/{org_id}/channels/{channel_id}/gateway-credential
X-Admin-Reason: <why>
```

Response (200, **token shown once, never re-displayable**):

```json
{"fid": 1042, "token": "<64-hex>", "base_url": "https://api.example/compat/bil24",
 "image_url": "https://api.example/compat/bil24/image", "rotated_at": "2026-09-07T12:00:00Z"}
```

`base_url` and `image_url` are built from `API_PUBLIC_URL` — the deployment's public **API**
origin (e.g. `https://api.arenasoldout.com`), which is normally NOT the admin SPA origin
`APP_PUBLIC_URL` (e.g. `https://app.arenasoldout.com`). `API_PUBLIC_URL` empty falls back to
`APP_PUBLIC_URL` for single-host stands; in production with `BIL24_COMPAT_ENABLED=true` the
effective value must be a non-empty `https://` URL or the process refuses to start. The same
origin carries `GET_TICKETS_BY_ORDER`'s `pdfUrl`/`downloadUrl` and the **absolute signed**
`bigPosterUrl`/`smallPosterUrl` that `GET_ALL_ACTIONS` hands the site's artwork sync — those
poster links carry an `expires`/`sig` pair with a 24-hour TTL, and `GET /v1/media-files/{id}`
answers `401` without them, so a site syncing more than a day after a catalog read must re-read
the catalog rather than cache the URL (feature #535).

This mints a new 32-byte random token (`GenerateGatewayToken`), bcrypt-hashes it into
`sales_channels.settings.gateway.token_hash`, and sets `settings.gateway.enabled = true`. Put
`fid` and `token` into the site's Bil24-plugin config (`class-bil24-client.php` constants /
options, same place the old Bil24.pro credentials lived).

`GET /v1/organizations/{org_id}/channels/{channel_id}/gateway-credential` returns
`{fid, enabled, rotated_at}` only — never the token or its hash. `DELETE` clears the hash and
disables the channel (see §5).

## 2. Register the WordPress webhook

```
PUT /v1/organizations/{org_id}/channels/{channel_id}/wp-webhook
X-Admin-Reason: <why>
Body: {"callback_url": "https://site.example/wp-json/bil24/v1/notify", "signing_secret": "<optional, generated if omitted>"}
```

This deactivates any prior active subscriber for the channel, inserts a new
`wp_webhook_subscribers` row (`kind='bil24_wp'`), and synchronously fires a test envelope
`{"type":"test","data":null}` at the new URL (10s timeout, `X-Arena-Event-Type` +
`X-Arena-Signature` HMAC headers via `bil24wire.Sign`). The response echoes the signing secret
once and reports `test_delivery: {ok, http_status}` — a failed test delivery does **not** fail
the PUT (the site may not be listening yet during a staged rollout); re-check
`test_delivery.ok` before trusting the registration. Point `callback_url` at the site's
`bil24-notification-receiver.php` endpoint. Real deliveries go through
`internal/platform/bil24wire.Dispatcher` against `outbox_events`, not this synchronous test
call.

## 3. Rotate a credential

- **Gateway fid/token**: re-run the §1 `PUT .../gateway-credential` call. It always mints a
  fresh token and re-hashes — there is no separate "rotate" verb, PUT *is* rotation. The old
  token stops authenticating the instant the new hash is written; update the site's plugin
  config in the same maintenance window to avoid a gap.
- **Org service API keys** (ADR-038, unrelated to the gateway credential but same security
  model): no in-place rotate either — issue a new key
  (`POST /v1/organizations/{org_id}/api-keys`) and revoke the old one
  (`DELETE /v1/organizations/{org_id}/api-keys/{id}`) once every caller is switched over.
- **Webhook signing secret**: re-run §2 with a new `signing_secret` (or omit it to let the
  server generate one); this also rotates `callback_url` if it changed.

**Token verification cache**: every successful gateway token check is cached in-process
(`hbil24/token_cache.go`, perf fix — uncached, every `/compat/bil24/json` command paid a fresh
~50-190ms bcrypt compare) so a channel doing repeat traffic does not re-run bcrypt on every
request. The cache is keyed by the bcrypt hash string itself, so the "old token stops
authenticating the instant the new hash is written" rotation guarantee above still holds
exactly — a request presenting the old token is checked against the NEW hash (freshly read
from `sales_channels.settings` on every request) and simply misses the cache, paying a real
bcrypt compare that fails. Only successful verifications are ever cached; a wrong guess always
re-runs bcrypt. TTL defaults to 5 minutes and is configurable via `BIL24_TOKEN_CACHE_TTL`
(e.g. `2m`, `10m`); 0/unset keeps the default.

## 4. Dead-lettered outbox rows

There is no admin HTTP surface for this yet — it is SQL against the live dispatch table,
`outbox_events` (NOT the legacy unused `outbox` table — see AGENTS.md "Two outbox tables
exist"). `internal/platform/outbox` retries with exponential backoff and, after the max
attempt count, sets `dead_lettered_at` and stops retrying that row.

Find dead-lettered rows:

```sql
SELECT id, aggregate_id, event_type, attempts, last_error, dead_lettered_at
FROM outbox_events
WHERE dead_lettered_at IS NOT NULL
ORDER BY dead_lettered_at DESC;
```

Read `last_error` first — most dead letters are a webhook the site hasn't wired up yet (see
§2) or a stale `callback_url` after a domain change. Fix the root cause, then requeue the row
for one more attempt cycle:

```sql
UPDATE outbox_events
SET dead_lettered_at = NULL, next_attempt_at = NULL, attempts = 0
WHERE id = '<row id>';
```

Do this one row at a time and watch the worker logs — a bulk requeue of many rows against a
site that is still broken just re-fills the dead-letter queue.

## 5. Switch a site to the compat gateway (feature toggle)

Two independent switches, at two different levels:

- **Whole-gateway kill switch**: env var `BIL24_COMPAT_ENABLED` on the API/worker process.
  When false, the entire `/compat/bil24/*` subtree is not mounted and every request 404s —
  this is a deploy-time, all-channels switch, not a per-site one.
- **Per-channel switch**: `sales_channels.settings.gateway.enabled` (set by the §1 PUT, cleared
  by DELETE). A disabled channel with the whole gateway still mounted answers with **HTTP 200**
  and JSON envelope `resultCode=-4` ("unknown fid or channel disabled") — it does not 404, so
  do not rely on HTTP status to detect a disabled channel from outside; check `resultCode`.
  Only the `image` sub-route (`GET /compat/bil24/image`) uses a real `404` status for every
  "you may not see this" case, per spec §8, by design (an enumerable 404 there would leak which
  failure mode applies).

To switch a site live: provision its channel credential (§1), register its webhook (§2), point
the site's plugin config at the new `base_url`, and leave the old Bil24.pro credentials
untouched until the new path is verified end to end (spec's cutover strategy in
`08_architecture/01_api_compatibility_gateway_ru.md` §"Cutover Strategy": rollback is endpoint
configuration, never a code rollback).

## 6. Roll back by URL

Because switching is just a config change on the WordPress side (which URL + fid/token the
plugin points at), rollback is the same operation in reverse: change the plugin's configured
`base_url`/`fid`/`token` back to the previous (old Bil24.pro or previous arena channel)
values. Do **not** disable the channel credential (§1 DELETE) as the rollback mechanism — that
also invalidates in-flight gateway sessions/reservations for anyone still mid-checkout on the
new path. Prefer: leave the new channel enabled, just stop pointing the site at it, and
re-enable once the underlying issue is fixed.

## 7. Stand orgs `lampyris-staging` / `vino-staging`

These slugs are **not** present in `cmd/arena-seed` as of this wave — they are the intended
naming for the two sites' staging organizations (per
`docs/migration/wp_sites_to_arena_wave1_2026-09-04.md` and the W1-S interactive step in
`09_autoforge/wp_bil24_compat_backlog.md` §7), but nothing seeds them automatically yet.
Create them through the normal organization-creation admin flow, then provision each with its
own channel/credential (§1) and webhook (§2) — do not share a single org/channel between the
two sites, they must stay tenant-isolated like any other two organizations. If a future
session adds them to `arena-seed`, update this section with the exact seed command.

## 8. Creating events from the site (event bundle)

Design authority: `08_architecture/19_event_bundle_arena_native_spec_ru.md`. This is the
arena-native path for a site (or, in future iterations, a chat bot) to create an event **without
Bil24 in the loop**: one idempotent `POST` creates or edits the event + session + venue + price
categories + poster + publication in a single call.

- **Route**: `POST /v1/organizations/{org_id}/imports/event-bundle`, body `source: "arena"`,
  `Authorization: Bearer ak_…` carrying the `import.bil24_session` scope (same permission as the
  Bil24 relay import; the name is legacy, renaming it is cosmetic and deferred). The legacy
  `POST .../imports/bil24-session` route still exists unchanged and is now a thin alias that pins
  `source: "bil24"` — a body declaring `source: "arena"` there is rejected with 422
  `import.source_mismatch`.
- **Identifiers**: every Bil24-style `*Id` field (`action.actionId`, `actionEvent.actionEventId`,
  `venue.venueId`, `categoryList[].categoryPriceId`) is OPTIONAL for `source=arena` — omit it and
  arena mints a compat id ≥ 1e9; supply one only when editing an existing object (it must already
  be ≥ 1e9 and belong to this organization, otherwise 404 `import.compat_id_unknown`).
- **Idempotency key**: `externalRef` (mandatory for `source=arena`, e.g.
  `wp:lampyris-staging:product:4711`), not `actionEventId`. A repeat call with the same
  `externalRef` edits the existing session in place instead of creating a duplicate.
- **No seats**: `seatList`/`svg` are accepted but not materialised for `source=arena` in this
  wave (warning `import.seating_not_imported`, never a 422) — this is the general-admission-only
  branch. A seated event still goes through the multi-call Bil24-relay chain (`§13.2` below).
- **Response**: the same `ImportBil24SessionResponse` shape as the legacy route, now always
  carrying `external_ref` and `compat_ids` (`action_id`, `action_event_id`, `venue_id`,
  `category_price_ids` — the last one positionally aligned with the request's `categoryList`).
  Save `compat_ids.action_event_id` on the caller's side immediately; it is exactly what
  `GET_ALL_ACTIONS` will report once the event is `published`.

Minimal curl example against a staging org (replace `$ORG_ID` and the `ak_…` key):

```bash
curl -s -X POST "https://staging.example.com/v1/organizations/$ORG_ID/imports/event-bundle" \
  -H "Authorization: Bearer ak_xxxxxxxxxxxxxxxx" \
  -H "Content-Type: application/json" \
  --data @apps/backend/tests/compat/bil24/testdata/wp/event_bundle/arena_ga_lampyris.json
```

That fixture is the exact staging-Lampyris example body from spec §3 (also used by the
`TestEventBundleFixture_DecodesAndValidates` decode/validation test in
`apps/backend/tests/compat/bil24/event_bundle_fixture_526_test.go`). A successful call answers
something like:

```json
{
  "event_id": "…", "session_id": "…", "created": true,
  "external_ref": "wp:lampyris-staging:product:4711",
  "compat_ids": {
    "action_id": 1000000007, "action_event_id": 1000000008,
    "venue_id": 1000000003, "category_price_ids": [1000000012, 1000000013]
  }
}
```

**Round-trip to `GET_ALL_ACTIONS`**: once the bundle publishes (`publish: true` in the body, or a
follow-up bundle that flips it), the event surfaces through the same compat gateway `GET_ALL_ACTIONS`
command (§1's fid/token) any Bil24-relay event does — `actionId`/`actionEventId`/`venueId`/
`categoryPriceId` in the response are exactly `compat_ids`, `day`/`time` are rendered in the
venue's timezone, and `bigPosterUrl` points at `/v1/media-files/{poster_media_id}`. The site does
not need to wait for that sync to know the ids — it already has them in the bundle's own response.

**The API key MUST be bound to the site's sales channel, or no webhooks flow.** Publishing an event
sets `events.status = 'published'` and emits `v1.event.published`, but the WP webhook fan-out finds
its receivers through `event_publications → agent_feed_tokens (active, not revoked) →
webhook_subscribers (kind = 'bil24_wp')`, keyed by **sales channel**. An event that was never
published *into a channel* therefore has zero subscribers, and the outbox row is dispatched with
nothing to deliver — the site that created the event never receives `event.created`.

Since feature #536 the import closes that gap by itself: when a bundle with `publish: true` is
posted by an organization API key whose `channel_id` is set, arena publishes the event into that
channel inside the same transaction — reusing the channel's active feed token, or minting one with
the label `auto:event-bundle` — and echoes the result as
`publication: {channel_id, feed_token_id, publication_id}` in the response. Repeating the bundle
reuses the same token and publication (no duplicates).

If the key is **not** bound to a channel, the response carries `publication: null` plus the warning
`import.channel_publication_skipped`, and the site will never see a webhook. Fix it by creating the
key with a `channel_id` (§1 provisions the channel) — an existing key cannot be re-pointed, so issue
a new one and retire the old — then re-post the same bundle (same `externalRef`); the publication
appears without any manual admin step. The `§2` webhook subscriber must still be registered on that
same channel.

## 9. Orders: colliding open orders and late payments on expired orders

Two related money-safety behaviors were fixed in the order aggregate (`hbil24/cmd_order_create.go`,
`hbil24/cmd_order_pay.go`, `internal/platform/ordering`) and are worth knowing about when a site
reports a payment problem.

### 9.1 CREATE_ORDER_EXT: `bil24.open_order_exists` (resultCode 101)

A customer may hold at most one `pending_payment` order per event session
(`orders_one_pending_per_customer_session_uq`). CREATE_ORDER_EXT used to assume that finding a
second open order for the same customer+session always meant "the first one expired, mint a
replacement" — and expired it unconditionally, even when its hold was still fully live. Two
gateway buyers who happen to share the checkout identity the WordPress plugin sent (email/phone —
whatever the buyer typed at checkout, not a stable account id) could have buyer A's still-valid
hold silently stolen by buyer B's checkout a few milliseconds later; A's subsequent PAY_ORDER then
failed even though WooCommerce had already charged them.

CREATE_ORDER_EXT now only expires the existing open order when its hold is actually gone — the
reservation's state is not `draft`/`active`, or its `expires_at` has already passed. When the old
hold is still live, the request is refused with `resultCode 101` and description key
`bil24.open_order_exists` ("you already have an unpaid order for this event, complete or cancel it
first"), and nothing is written — the whole transaction, including the checkout session the request
just inserted, rolls back. The customer's original order is untouched and stays payable.

This is NOT triggered by the same gateway session resending CREATE_ORDER_EXT for its own cart
(bounced off the WooCommerce payment page, edited the basket, came back) — that case still updates
the existing order in place, unchanged from before.

If a site reports `open_order_exists` for what looks like one buyer: check whether two browser
tabs/devices under the same identity are checking out the same event concurrently. The fix is
working as intended — the buyer (or their second tab) needs to finish or cancel the first order.

### 9.2 The payment window (owner decision 2026-09-13) — PAY_ORDER never parks in manual_review

A buyer gets a FIXED window to pay, starting the instant the site creates (or restarts) the order:
`CREATE_ORDER_EXT` sets `orders.expires_at = reservations.expires_at = now + payment_window_seconds
+ payment_grace_seconds`, both settable per channel under `settings.gateway`
(`payment_window_seconds`, default 1200s/20min, bounds 60..7200; `payment_grace_seconds`, default
120s, bounds 0..600 — the grace absorbs only the site's own network latency delivering PAY_ORDER,
never extends how long the buyer has to authorize the card). Every `CREATE_ORDER_EXT` — a fresh
order or the same-cart update-in-place — restarts this window from the moment it runs, and the
response carries two new fields alongside the pre-existing `expiration` (which the spec already
defines, RFC3339, the hold's own deadline): `paymentDeadline` (unix epoch seconds, `now + window`,
the instant the SITE must stop accepting payment) and `paymentTimeout` (the window in seconds). A
plain cart `RESERVATION RESERVE`/`UN_RESERVE` on the same session never shortens a hold whose
expiry an open order has already pushed out — the underlying TTL-refresh primitive only ever moves
`expires_at` forward.

By owner decision, **there is no manual review anywhere in the gateway path any more** — the owner
has no staff for it, so every PAY_ORDER outcome resolves automatically:

| Order state at PAY_ORDER | Hold | Result |
|---|---|---|
| `paid` | — | `resultCode 0`, idempotent, no writes |
| `pending_payment`, within the window | live | pays normally, `resultCode 0`, tickets issued synchronously |
| `pending_payment`, within the window | TTL'd but still exclusively ours | re-acquired (`order_events.hold_reacquired`), then pays, `resultCode 0` |
| `pending_payment`, within the window | lost to someone else's RESERVE | the order is **cancelled** automatically (`ordering.Cancel`, `order_events.cancelled` with `payload.reason="hold_expired"`), `resultCode 101 bil24.hold_expired` |
| `pending_payment`, past the window | any | the order is **expired** in place (mirrors `order.expire_sweep`'s own per-row step) and its hold released, `order_events.hold_expired` with `payload.reason="payment_window_elapsed"`, `resultCode 101 bil24.order_expired` |
| `expired` | — | `resultCode 101 bil24.order_expired`, NO revival, no writes |
| `cancelled`/`refunded`/`partially_refunded`/`abandoned` | — | `resultCode 101 bil24.order_cancelled`, no writes |
| `manual_review` (**legacy rows only** — nothing writes this status any more) | — | `resultCode 101 bil24.hold_expired`, no writes |

Every branch is idempotent: a replayed PAY_ORDER (the WordPress plugin retries any non-zero
`resultCode`) answers the same result again without writing a second event or touching inventory
twice.

**Concurrency.** PAY_ORDER takes a row-level lock on the order (`LockOrderForUpdate`) as the FIRST
statement of its transaction and makes its ENTIRE decision — pay, expire, or leave alone — from the
row it reads UNDER that lock, never from a value read before the transaction opened. Concurrent
PAY_ORDER calls for the SAME order (a WordPress retry storm, or several requests landing right at
the payment-window boundary) therefore serialize on the lock: only the first to observe
`pending_payment` with a securable hold before the deadline actually pays; every later call sees
the row AFTER that transaction committed and answers consistently with whatever it became. As a
second, independent safety net, `payment_intents.provider_payment_id` is a GLOBAL unique index and
is deterministic per order+method, so two transactions racing to complete the SAME payment can
never both succeed even if the lock were somehow bypassed.

**What this replaces.** Before this decision, an `expired` order attempted a "revival"
(`ordering.ReviveForPayment`) back to `pending_payment` when its hold could still be secured, and a
hold that could not be secured parked the order (and its checkout session) in `manual_review` with
an operator alert (`payParkManualReview`). Both mechanisms, and the `ordering.ErrOpenOrderConflict`
edge case they created (reviving an order could collide with a customer's newer legitimately-open
order for the same session), are REMOVED — a fixed payment window makes them unreachable: an order
past its window is expired outright rather than revival-eligible, and a hold lost within the window
is cancelled automatically rather than parked for a human. A `manual_review` order in a live
database predates this change; nothing new will ever create one, and PAY_ORDER's terminal-status
handling for it is kept only so a stale row does not retry-loop.

## Related reading

- Wire-level behavior differences and result-code map:
  `apps/backend/tests/compat/bil24/BEHAVIOR_DIFFERENCES.md`.
- Design decisions for this wave: ADR-034..038,
  `08_architecture/11_architecture_decision_log_ru.md`.
- Compat gateway architecture and cutover strategy:
  `08_architecture/01_api_compatibility_gateway_ru.md`.
