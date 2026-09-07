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

## Related reading

- Wire-level behavior differences and result-code map:
  `apps/backend/tests/compat/bil24/BEHAVIOR_DIFFERENCES.md`.
- Design decisions for this wave: ADR-034..038,
  `08_architecture/11_architecture_decision_log_ru.md`.
- Compat gateway architecture and cutover strategy:
  `08_architecture/01_api_compatibility_gateway_ru.md`.
