# MACS Integration Contract — AB-50 (Wave 4)

## Overview

MACS (Max Mobil Access Control System) is the external scanning service used for
physical gate admission. The platform feeds MACS with ticket data via two
mechanisms:

1. **JSON export** (`GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export`) —
   a one-time bulk import for a session's full ticket inventory.
2. **Webhooks** — real-time lifecycle events (issuance, cancellation) delivered to a
   per-org registered receiver.

**Design constraint:** ALL MACS-shaped mapping lives behind the
`apps/backend/internal/platform/macs` package boundary. Nothing MACS-specific
may leak into the catalog or ticketing domain. When MACS changes, only files in
that package change.

---

## Integer ID Scheme (AB-50a)

MACS identifies tickets and seats with integer IDs. The platform generates stable
integer IDs via database sequences:

- `tickets.system_ticket_id` — bigint, auto-assigned, never NULL after insert.
- `session_seats.system_seat_id` — bigint, auto-assigned, never NULL after insert.

These IDs are derived deterministically from the same database row, so they are
stable across exports and webhooks.

---

## holderStatus Values

MACS uses a 4-value integer `holderStatus` field on every ticket:

| Value | Meaning | Who sets it |
|-------|---------|-------------|
| `0` | Valid / not yet scanned — MACS admits the bearer | Platform (export + webhook) |
| `1` | Checked-in (scanned at entry) | MACS scanner — never emitted by the platform |
| `2` | Checked-out | MACS scanner — never emitted by the platform |
| `3` | Refunded / cancelled / revoked — MACS denies the bearer | Platform (export + webhook) |

The platform only ever emits `0` (active ticket) or `3` (any terminal state:
`cancelled`, `revoked`, `transferred`). Values `1` and `2` are MACS-internal
and the platform never writes them.

---

## JSON Export (AB-50b)

**Endpoint:** `GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export`

**Response:** array of Order objects. Each Order contains a `ticketList` with all
completed tickets for the session. The format matches the MACS Python importer's
expectations (camelCase field names, integer IDs, `EAN-13` barcode format marker).

**Error:** `422 macs.export_incomplete` — returned when the session's venue has no
city configured. Link the venue to a city before exporting.

**Key fields per ticket:**

| Field | Source |
|---|---|
| `id` | `tickets.system_ticket_id` |
| `seatId` | `session_seats.system_seat_id` for assigned seats; `1_000_000_000 + system_ticket_id` for GA (disjoint range, never collides with real seat IDs — seat sequences start at 1) |
| `barcode` | `ticket_credentials.payload` (type `ean13`); when the ticket has no EAN-13 credential yet, the value derived on the fly by `barcodes/ean13.PlatformCode(system_ticket_id)` (platform prefix `21` + zero-padded id + check digit). A `static_qr` credential is never sent — MACS scanners read EAN-13 only. |
| `barcodeFormat` | Always `{"id": 0, "name": "EAN-13"}` |
| `holderStatus` | `0`=valid, `3`=cancelled/revoked (see holderStatus table above) |
| `actionEvent.id` | The **session's** `actionEventId` from `compatibility_id_map` (`kind = 'action_event'`, `platform_id = sessions.id`), allocated by `compatids.Ensure`. Arena-native ids start at 1_000_000_000; sessions imported from Bil24 keep their original id. Never a hash. |
| `actionEvent.actionId` | The **event's** `actionId` from `compatibility_id_map` (`kind = 'action'`, `platform_id = events.id`), same allocation rules. |
| `actionEvent.showTime` | `sessions.start_at` formatted in the venue's local timezone (`venues.timezone`), without TZ suffix: `"2026-08-22T20:00:00"` |
| `price` | Actual sold price: `COALESCE(reservation_ga_items.unit_price, ticket_tiers.price_amount, 0)`. Falls back to `order_subtotal / ticket_count` for untiered GA. |
| `discountReason` | See discountReason vocabulary below |

**discountReason vocabulary:**

| Condition | Value |
|-----------|-------|
| Promo code applied | `"Промокод {code}"` (e.g. `"Промокод CATDANIEL"`) |
| No payment provider (external/complimentary) | `"Внешняя система"` |
| Regular paid purchase | `""` (field omitted via `omitempty`) |

**Per-ticket discount proration:**

The checkout-level discount is prorated across tickets proportionally:
`ticket.discount = ticket.price * order.discount / order.subtotal`.
The last ticket absorbs any rounding remainder so that `sum(ticket.discount) == order.discount` exactly.

---

## Webhooks (AB-50c)

### Event mapping

The platform emits 4 event types; MACS handles only 2 webhook types:

| Platform outbox event type | MACS webhook type |
|---|---|
| `v1.scanner.ticket.issued` | `order.paid` |
| `v1.ticket.cancelled` | `ticket.refunded` |
| `v1.ticket.refunded` | `ticket.refunded` |
| `v1.ticket.revoked` | `ticket.refunded` |

All other platform event types are silently skipped (outbox row is marked
processed without calling the receiver).

### Envelope shape

```json
{
  "id": 4611686019022454784,
  "created": "2026-08-22T10:00:00Z",
  "type": "order.paid",
  "data": {
    "id": 42,
    "seatId": 1000000042,
    "orderId": 42,
    "barcode": "2100000000425",
    "barcodeFormat": {"id": 0, "name": "EAN-13"},
    "actionEvent": {
      "id": 1000000001,
      "actionId": 1000000002,
      "cityName": "Moscow",
      "venueName": "Main Hall",
      "actionName": "Rock Night",
      "actionLegalOwner": "Arena LLC",
      "showTime": "2026-08-22T20:00:00"
    },
    "holderStatus": 0,
    "orderId": 42
  }
}
```

- **`id`**: derived from the outbox row UUID (low 63 bits of first 8 bytes).
  Unique per dispatch event, not per ticket. Used by MACS's `/_wh/reprocess/{id}`.
- **`created`**: `time.RFC3339` UTC string (e.g. `"2026-08-22T10:00:00Z"`).
- **`data`**: the full MACS `Ticket` shape (same as the JSON export) plus `orderId`.
  For `ticket.refunded`, `holderStatus` is forced to `3` regardless of the ticket's
  database state; an optional `reason` string may be included.
- **`data.actionEvent.id` / `data.actionEvent.actionId`**: compatibility ids from
  `compatibility_id_map` (see the export table above), not hashes. They are stable
  across dispatches and match the ids the JSON export emits for the same session.

### HMAC signing

Every webhook POST is signed with HMAC-SHA256 using the subscriber's
`signing_secret`. The signature is included in the `X-MACS-Signature` header:

```
X-MACS-Signature: sha256=<hex-encoded-hmac>
```

The HMAC is computed over the raw JSON body bytes. Verification on the MACS
receiver side is **optional** (backward compatible — the header is omitted when
`signing_secret` is empty, and MACS need not verify if not configured).

### Retry behaviour

The MACS dispatcher uses the `outbox_events` retry infrastructure
(migration 0068: `next_attempt_at`, `dead_lettered_at`). Non-2xx responses and
network errors cause the outbox to retry with exponential backoff
(`2^n` minutes, capped at 1 hour).

**Worker configuration (arena-worker):**

- `MaxAttempts`: **30** — spans approximately 24 hours before dead-lettering.
  (The previous default of 5 dead-lettered after ~31 minutes.)
- HTTP client timeout: 10 seconds per attempt.

Dead-lettered rows have `dead_lettered_at IS NOT NULL` and are not retried again.

---

## Subscriber Registration

### Admin API

| Method | Path | Description |
|---|---|---|
| `GET` | `/v1/organizations/{org_id}/macs-webhook` | Get active subscriber |
| `PUT` | `/v1/organizations/{org_id}/macs-webhook` | Register / replace subscriber |
| `DELETE` | `/v1/organizations/{org_id}/macs-webhook` | Deactivate subscriber |

**PUT request body:**
```json
{
  "callback_url": "https://macs.example.com/api/_wh/tickets",
  "signing_secret": "your-hmac-secret"
}
```

**`callback_url` must end with `/api/_wh/tickets`** — that is the only path a
MACS receiver serves (the WordPress plugin mounts its importer there in
`class-lops-macs.php`). Anything else is rejected with
`422 macs.invalid_callback_url`. The rule exists because a URL pointing
elsewhere on the same site answers `200` with an HTML page, which the outbox
reads as "delivered": every sale would be lost silently. A single trailing
slash is tolerated. Earlier revisions of this document showed
`https://macs.example.com/_wh/tickets` (no `/api`) — that value is now
rejected; re-register any subscriber created from it.

**PUT response** includes `signing_secret` in the response body so the caller
can record it. Subsequent GET responses omit the secret.

There can be at most one active MACS subscriber per org (enforced by a partial
unique index `uq_webhook_subscribers_macs_per_org`). The PUT operation
deactivates any existing active subscriber before inserting the new one.

### Database

MACS subscribers are stored in the `webhook_subscribers` table with `kind = 'macs'`.
Migration `0089_macs_webhook_subscribers.sql` adds the `kind` and `org_id` columns.

---

## Ops Runbook

### Register a MACS receiver for an org

```bash
curl -X PUT \
  -H "Authorization: Bearer <admin-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"callback_url":"https://macs.example.com/api/_wh/tickets","signing_secret":"<secret>"}' \
  https://api.example.com/v1/organizations/<org-id>/macs-webhook
```

Record the `signing_secret` returned in the response body — it is only shown once.

### Rotate the signing secret

1. PUT with a new `signing_secret` to replace the subscriber.
2. Update the MACS receiver configuration with the new secret.

### Re-export a session to MACS

If the MACS importer database is out of sync, re-run the JSON export endpoint
and feed the result to the MACS importer. The `holderStatus` field will reflect
current ticket state (`0`=valid, `3`=cancelled/revoked).

```bash
curl -H "Authorization: Bearer <admin-jwt>" \
  https://api.example.com/v1/organizations/<org-id>/events/<event-id>/sessions/<session-id>/macs-export \
  > export.json
# Feed export.json to the MACS Python importer CLI.
```

### W1-Mb migration note — `actionEvent.id` changed key

Before W1-Mb, `actionEvent.id` was a hash of the `events.id` UUID and there was
no `actionEvent.actionId`. Both now come from `compatibility_id_map`
(`actionEvent.id` = the **session's** `action_event` id, `actionId` = the
**event's** `action` id), so the value MACS receives for an already-imported
session **changes**. MACS keys its event rows on that id, so a re-export or a
webhook for such a session lands as a *new* MACS event rather than an update of
the old one.

Production impact today: **none** — no arena-originated events exist in the
production MACS instance yet. Only test/staging sessions imported before W1-Mb
are affected. When rolling this out:

1. In MACS staging, delete (or archive) the events imported from arena before
   W1-Mb; they are keyed on the old hash and will never be updated again.
2. Re-run the `macs-export` for each affected session and re-import.
3. Confirm the imported event id matches
   `SELECT system_id FROM compatibility_id_map WHERE kind = 'action_event' AND platform_id = '<session-uuid>'`.

### Read dead-lettered outbox rows

```sql
SELECT id, event_type, payload, attempts, dead_lettered_at, created_at
FROM outbox_events
WHERE dead_lettered_at IS NOT NULL
  AND event_type IN ('v1.scanner.ticket.issued', 'v1.ticket.cancelled',
                     'v1.ticket.refunded', 'v1.ticket.revoked')
ORDER BY dead_lettered_at DESC;
```

Dead-lettered rows are permanently quarantined (they are skipped by future polls).
To redeliver manually, clear `dead_lettered_at` and reset `attempts = 0`:

```sql
UPDATE outbox_events
SET dead_lettered_at = NULL, attempts = 0, next_attempt_at = NULL
WHERE id = '<uuid>';
```

### Verify webhook delivery (non-dead-lettered)

```sql
SELECT id, event_type, attempts, next_attempt_at, dead_lettered_at
FROM outbox_events
WHERE processed_at IS NULL
  AND dead_lettered_at IS NULL
ORDER BY next_attempt_at;
```

---

## Internal Scanner Endpoints (INTERNAL / TESTING-ONLY)

The three legacy scanner endpoints (`POST /v1/scan`, `POST /v1/scanner/validate`,
`POST /v1/scanner/scan-events`) are **not the production gate**. They exist for
development and integration testing only. Do not build scanner hardware against
them — MACS is the production gate.

As of AB-50d, these endpoints apply a status gate: credentials for cancelled or
revoked tickets are rejected with `scanner.ticket_not_admissible`.

---

## Testing

### Unit tests

```bash
go test ./apps/backend/internal/platform/macs/...
```

### Integration tests (requires local PostgreSQL)

```bash
DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
go test -tags integration ./apps/backend/internal/platform/macs/...
```

Key integration tests:
- `TestMACS_RoundTrip` — seeds org → ticket → subscriber; dispatches
  `order.paid` + `ticket.refunded` to stub receiver; asserts MACS integer IDs.
- `TestMACS_CancelEnqueuesExactlyOne_OutboxEvent` — verifies the cancel flow
  writes exactly one `v1.ticket.cancelled` outbox event and the dispatcher
  maps it to one MACS `ticket.refunded` envelope.
- `TestMACS_AB50e_ThreeTicketRoundTrip` — three-ticket round-trip with HMAC
  verification, cancel-with-retry, and holderStatus assertion (3/0/0).
- `TestMACS_AB50i_ExportHandler_422_NoCityVenue` — verifies `422 macs.export_incomplete`
  when the venue has no city configured.
