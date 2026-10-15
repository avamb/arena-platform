# Webhook event catalog (feature S-1)

Short reference table of outbox event types that are dispatched to external
webhook subscribers. Each event is appended to the `outbox` table inside the
same transaction as the domain mutation that produced it (transactional
outbox pattern) and delivered at-least-once by the dispatcher.

Naming convention: `v1.<aggregate>.<verb-past-tense>`.

## Generic catalog events

| Event type             | Aggregate type | Aggregate ID                 | Trigger                                                                                            |
| ---------------------- | -------------- | ---------------------------- | -------------------------------------------------------------------------------------------------- |
| `v1.order.placed`      | `order`        | order / checkout_session UUID | Checkout session transitions to a paid / placed state.                                              |
| `v1.ticket.created`    | `ticket`       | ticket UUID                  | New ticket row issued after successful checkout.                                                    |
| `v1.ticket.refunded`   | `ticket`       | ticket UUID                  | Refund webhook finalizes (`succeeded`) and `CancelTicketsByCheckoutSession` cancels linked tickets. |
| `v1.ticket.revoked`    | `ticket`       | ticket UUID                  | Complimentary issuance revocation (`POST /v1/complimentary/{id}/revoke`).                          |
| `v1.session.cancelled` | `session`      | session UUID                 | Session status transitions to `cancelled` via the session UPDATE endpoint.                          |
| `v1.order.paid`        | `order`        | order UUID                   | The last ticket of a paid order is issued: the sale is complete (`htickets`, the primary source of the MACS and Bil24-site `order.paid` webhooks). |
| `v1.order.cancelled`   | `order`        | order UUID                   | An order is cancelled (`ordering.Cancel`); the Bil24-site dispatcher turns it into `order.cancelled`. |
| `v1.ticket.cancelled`  | `ticket`       | ticket UUID                  | An operator cancels a ticket (`POST /v1/tickets/{id}/cancel`); it stops admitting at once.          |
| `v1.ticket.scanned`    | `ticket`       | ticket UUID                  | A door scan is recorded: arena's own scanner callback (`POST /v1/scanner/scan-events`) or MACS (`POST /v1/macs/scan-events`, task M1). One per new `scan_events` row with a known ticket, admitted or denied; a replayed scan emits nothing. |
| `v1.refund.blocked_scanned` | `ticket`  | ticket UUID                  | A refund of a ticket already scanned at the gate was refused (once per ticket and actor per day). |
| `v1.refund.scan_exception_granted` | `ticket` | ticket UUID          | A platform superadmin allowed one refund of a scanned ticket.                                       |

## Bil24-shaped scanner events (legacy compatibility)

These events use a Bil24-compatible payload shape and are kept distinct from
the generic catalog so legacy Bil24 scanner software can consume them
unchanged. They share the `scanner.ticket` aggregate type.

| Event type                  | Aggregate type    | Aggregate ID    | Trigger                                                       |
| --------------------------- | ----------------- | --------------- | ------------------------------------------------------------- |
| `v1.scanner.ticket.issued`   | `scanner.ticket` | ticket UUID     | After tickets are issued for a checkout session.              |
| `v1.scanner.ticket.revoked`  | `scanner.ticket` | ticket UUID     | Generic ticket cancellation (non-refund).                     |
| `v1.scanner.ticket.refunded` | `scanner.ticket` | refund UUID     | Refund webhook finalizes and tickets are cancelled.           |

## Payload schemas

All payloads are JSON objects. Timestamps are RFC3339 in UTC. Monetary
amounts are in minor units; the currency is an ISO-4217 string.

### `v1.ticket.refunded`

```json
{
  "ticket_id":           "<uuid>",
  "checkout_session_id": "<uuid>",
  "refund_id":           "<uuid>",
  "amount":              123456,
  "currency":            "EUR",
  "refunded_at":         "2026-06-30T12:34:56Z"
}
```

### `v1.ticket.revoked`

```json
{
  "ticket_id":                 "<uuid>",
  "complimentary_issuance_id": "<uuid>",
  "reason":                    "complimentary_revocation",
  "revoked_at":                "2026-06-30T12:34:56Z"
}
```

The `complimentary_issuance_id` field is omitted when the revocation is not
sourced from a complimentary issuance.

### `v1.session.cancelled`

```json
{
  "session_id":      "<uuid>",
  "event_id":        "<uuid>",
  "status":          "cancelled",
  "previous_status": "scheduled",
  "cancelled_at":    "2026-06-30T12:34:56Z"
}
```

`previous_status` is omitted when not available; `event_id` is included for
routing convenience.

### `v1.ticket.scanned`

Written in the SAME transaction as the `scan_events` row on the MACS route;
arena's own scanner callback still appends it in a separate short
transaction right after the insert.

```json
{
  "ticket_id":       "<uuid>",
  "session_id":      "<uuid>",
  "event_id":        "<uuid>",
  "org_id":          "<uuid>",
  "credential_code": "2100000001231",
  "scanned_at":      "2026-10-15T17:42:05Z",
  "result":          "admitted",
  "gate":            "Main",
  "device_id":       "dev-7",
  "scan_event_id":   "<uuid>"
}
```

A MACS scan adds `source: "macs"`, `scanner_label`, `external_scan_id`,
`subscriber_id`, `macs_result` (`admitted` / `rejected` / `duplicate`; the
latter two are stored as `result: "denied"`), `system_ticket_id` and
`first_admission` (true when this scan stamped `tickets.used_at`). A payload
without `source` comes from arena's own scanner. `scanned_at` is the moment
of the scan, which may lie days before the event was written (an offline
scanner's backlog): order by it, not by arrival.

New event types are added to this catalog in the same PR that starts
emitting them.

## Delivery semantics

- **At-least-once.** Subscribers MUST be idempotent on `(event_type, aggregate_id, occurred_at)`.
- **Ordering.** Within a single aggregate_id the dispatcher delivers in
  insertion order; cross-aggregate ordering is not guaranteed.
- **Best-effort emission.** If the outbox writer or database is unavailable
  when the emitter tries to append, the failure is logged and the HTTP
  caller is not affected. The domain mutation (refund, revocation, session
  update) still succeeds in that case, mirroring the existing scanner
  emitter behaviour. Operators reconcile dropped events out-of-band.
