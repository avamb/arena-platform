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

## JSON Export (AB-50b)

**Endpoint:** `GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export`

**Response:** array of Order objects. Each Order contains a `ticketList` with all
completed tickets for the session. The format matches the MACS Python importer's
expectations (camelCase field names, integer IDs, `EAN-13` barcode format marker).

**Key fields per ticket:**

| Field | Source |
|---|---|
| `id` | `tickets.system_ticket_id` |
| `seatId` | `session_seats.system_seat_id` (or `system_ticket_id` for GA) |
| `barcode` | `ticket_credentials.payload` (type `qr`) or fallback to `system_ticket_id` |
| `holderStatus` | 0=valid, 3=cancelled (`tickets.status`) |
| `actionEvent.id` | First 8 bytes of `events.id` UUID → int64 |

---

## Webhooks (AB-50c)

### Event mapping

MACS handles only two event types:

| Platform event type | MACS webhook type |
|---|---|
| `v1.scanner.ticket.issued` | `order.paid` |
| `v1.ticket.cancelled` | `ticket.refunded` |
| `v1.ticket.refunded` | `ticket.refunded` |
| `v1.ticket.revoked` | `ticket.refunded` |

All other event types are silently skipped (no-op — outbox row is marked processed).

### Envelope shape

```json
{
  "id": 42,
  "created": "2026-08-22T10:00:00",
  "type": "order.paid",
  "data": {
    "ticketId": 42,
    "sessionId": "...",
    "checkoutId": "..."
  }
}
```

The `created` field uses local time without timezone suffix (MACS requirement).

### HMAC signing

Every webhook POST is signed with HMAC-SHA256 using the subscriber's
`signing_secret`. The signature is included in the `X-MACS-Signature` header:

```
X-MACS-Signature: sha256=<hex-encoded-hmac>
```

Verification on the MACS side is optional (backward compatible).

### Retry behaviour

The MACS dispatcher reuses the existing `outbox_events` retry infrastructure
(migration 0068: `next_attempt_at`, `dead_lettered_at`). A non-2xx HTTP response
or a network error causes the outbox to retry with exponential backoff. After the
configured maximum attempts (default: 5) the row is dead-lettered.

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
  "callback_url": "https://macs.example.com/_wh/tickets",
  "signing_secret": "your-hmac-secret"
}
```

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
  -d '{"callback_url":"https://macs.example.com/_wh/tickets","signing_secret":"<secret>"}' \
  https://api.example.com/v1/organizations/<org-id>/macs-webhook
```

Record the `signing_secret` returned in the response body — it is only shown once.

### Rotate the signing secret

1. PUT with a new `signing_secret` to replace the subscriber.
2. Update the MACS receiver configuration with the new secret.

### Verify webhook delivery

Check the `outbox_events` table for unprocessed rows:

```sql
SELECT id, event_type, attempts, next_attempt_at, dead_lettered_at
FROM outbox_events
WHERE processed_at IS NULL
ORDER BY next_attempt_at;
```

Dead-lettered rows (all retry attempts exhausted) have `dead_lettered_at IS NOT NULL`.

### Re-export a session to MACS

If the MACS importer database is out of sync, re-run the JSON export endpoint
and feed the result to the MACS importer. The `holderStatus` field will reflect
current ticket state (0=valid, 3=cancelled).

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
