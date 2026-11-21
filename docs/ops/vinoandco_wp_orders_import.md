# Vino&Co: orders of the WordPress era and the refund outside the platform (P-14, spec 62 section 14.6)

Two pieces: the table that matches a WordPress order to the platform's order and
tickets (loaded once when WordPress is stopped), and the operator action "Refund
outside the platform" on the order card of the event center.

## 1. The correspondence file

`arena-wp-orders-import` reads a CSV, checks every line against the platform and
stores it in `wp_order_links`. The operator produces the file (WooCommerce order
id, the AllPay order id kept in the order's meta, and the platform order number
and ticket numbers that the Bil24-protocol gateway returned to the site); this
tool never connects to WordPress.

```
DATABASE_URL=... JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-wp-orders-import \
    --org-id <organization uuid> --file orders.csv --dry-run
```

| column | meaning |
|---|---|
| `wc_order_id` | WooCommerce order id (text up to 64 characters) |
| `allpay_order_id` | the AllPay order id, may be empty |
| `platform_order_system_id` | the platform order number, `orders.system_id` (the number the site got as `orderId`) |
| `ticket_system_ids` | the order's tickets, `tickets.system_ticket_id`, separated by `;` `,` `|` or a space (may be empty) |

Comma or semicolon separated, header line required, UTF-8 (a BOM is fine).

Checks per line: the order exists in this organization (`unknown_order`), every
listed ticket exists (`unknown_ticket`) and belongs to that order
(`ticket_not_in_order`), the line is readable (`bad_row`), a WooCommerce order
does not appear twice with different data (`duplicate_wc_order`).

`--dry-run` prints `rows / inserted / updated / unchanged / mismatches` and one
`mismatch line N ...` line per problem, and writes nothing. Without `--dry-run`
any mismatch stops the run before it writes (fix the file, or pass
`--allow-mismatches` to store the valid lines anyway). The run is idempotent: a
line identical to the stored one is "unchanged"; a corrected line is an
"update". Keyed by (organization, `wc_order_id`).

## 2. Refund outside the platform

On the card of a paid order (permission `refund.create`, held by the owner and by
the manager) the button "Refund outside the platform" opens a dialog. The
operator first returns the money in the AllPay cabinet (full or partial, as much
as is due), then in the dialog ticks the tickets, types the FINAL amount returned
for each (at most the price of the ticket) and the AllPay refund number, checks
the summary and records it. The password is asked again, as for any money action.

API: `POST /v1/organizations/{org_id}/orders/{order_id}/external-refund` with
`{"items": [{"ticket_id", "amount_minor", "provider_refund_ref"}]}`.

In ONE transaction the platform voids the tickets (places go back on sale,
barcodes stop admitting), books a refund with `settlement = external` per ticket
(`refunds.external_ref` keeps the AllPay number), moves the order to
`partially_refunded` or `refunded`, writes the order history and an audit event,
and after the commit announces the cancellation through the outbox (the sites and
MACS hear `ticket.refunded`). The platform moves no money and does not e-mail the
buyer.

Rules:

- One final refund per ticket. A second attempt answers 409
  `order.external_refund_exists` with the amount already recorded. If the
  provider returned more afterwards, the record is corrected by the platform
  operator (superadmin), it is not a second action.
- The amount is positive and at most what the buyer paid for that ticket
  (`order_items.total`): 422 `order.refund_exceeds_price`.
- A ticket not in the order: 422 `order.ticket_not_in_order`. A ticket that is not
  active: 409 `ticket.not_active`. A ticket already scanned at the gate is refused
  like in the ticket cancellation route.
- Any refusal rolls back the whole call: nothing is half recorded.

The session summary (`GET .../sessions/{session_id}/summary`), its Excel export
and the event summary count these refunds under `settlement: external`.
