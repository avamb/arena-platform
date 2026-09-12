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

## Related reading

- Wire-level behavior differences and result-code map:
  `apps/backend/tests/compat/bil24/BEHAVIOR_DIFFERENCES.md`.
- Design decisions for this wave: ADR-034..038,
  `08_architecture/11_architecture_decision_log_ru.md`.
- Compat gateway architecture and cutover strategy:
  `08_architecture/01_api_compatibility_gateway_ru.md`.
