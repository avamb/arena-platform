# Arena Tickets — CustomEvent Integration Guide (WID-S5)

The `<arena-tickets>` web component dispatches typed `CustomEvent`s at key
purchase-cycle milestones so host pages can integrate with their own analytics
or UX without reaching into the widget's internal Shadow DOM.

All events are dispatched with `bubbles: true, composed: true` so they cross
the Shadow DOM boundary and can be observed on the `<arena-tickets>` host
element itself, or anywhere up the DOM tree including `document`.

---

## Quick start

```html
<arena-tickets id="widget" feed-token="…" session-id="…"></arena-tickets>

<script type="module">
  const widget = document.getElementById('widget');

  widget.addEventListener('arena:seat_selected', (e) => {
    // e.detail: { seatKey: string, sessionId: string }
    console.log('Seat added to cart:', e.detail.seatKey);
  });

  widget.addEventListener('arena:seat_released', (e) => {
    // e.detail: { seatKey: string, sessionId: string }
    console.log('Seat removed from cart:', e.detail.seatKey);
  });

  widget.addEventListener('arena:payment_started', (e) => {
    // e.detail: { checkoutToken: string, sessionId: string }
    analytics.track('begin_checkout', { token: e.detail.checkoutToken });
  });

  widget.addEventListener('arena:order_paid', (e) => {
    // e.detail: { checkoutToken, orderRef, totalMinorUnits, currency }
    analytics.track('purchase', {
      transaction_id: e.detail.checkoutToken,
      value: (e.detail.totalMinorUnits ?? 0) / 100,
      currency: e.detail.currency ?? 'EUR',
    });
  });

  widget.addEventListener('arena:order_failed', (e) => {
    // e.detail: { checkoutToken: string | null, reason: string }
    console.warn('Order failed, reason:', e.detail.reason);
  });
</script>
```

---

## Event reference

### `arena:seat_selected`

Fires when the user taps or clicks a seat to **add** it to their selection.
Not fired for general-admission (GA) quantity changes.

| Field       | Type     | Description                              |
|-------------|----------|------------------------------------------|
| `seatKey`   | `string` | Seat key as returned by the API (e.g. `"A01"`) |
| `sessionId` | `string` | ID of the event session                  |

---

### `arena:seat_released`

Fires when a seat is **removed** from the selection — either by tapping an
already-selected seat to deselect it, or by pressing the remove button in the
cart sheet.

| Field       | Type     | Description               |
|-------------|----------|---------------------------|
| `seatKey`   | `string` | The deselected seat key   |
| `sessionId` | `string` | ID of the event session   |

---

### `arena:payment_started`

Fires **immediately after** `POST /checkout/start` succeeds and the widget is
about to redirect the user to the payment provider page.

Use this to fire a `begin_checkout` or equivalent analytics event.

| Field            | Type     | Description                              |
|------------------|----------|------------------------------------------|
| `checkoutToken`  | `string` | Opaque token identifying the checkout session |
| `sessionId`      | `string` | ID of the event session being purchased  |

---

### `arena:order_paid`

Fires when the widget polls `GET /checkout/{token}` and receives
`status: "paid"`.  This is the reliable signal that the purchase completed.

Use this to fire a `purchase` or `conversion` analytics event.

| Field              | Type             | Description                                         |
|--------------------|------------------|-----------------------------------------------------|
| `checkoutToken`    | `string`         | Opaque checkout token                               |
| `orderRef`         | `string \| null` | Human-readable order reference (e.g. `"ORD-12345"`) — may be `null` before reconciliation |
| `totalMinorUnits`  | `number \| null` | Order total in minor currency units (e.g. 2200 = €22.00) |
| `currency`         | `string \| null` | ISO 4217 currency code (e.g. `"EUR"`, `"CZK"`)     |

---

### `arena:order_failed`

Fires when the widget polls order status and receives `status: "failed"` or
`status: "expired"`.

| Field           | Type              | Description                                          |
|-----------------|-------------------|------------------------------------------------------|
| `checkoutToken` | `string \| null`  | Opaque checkout token (null if never established)    |
| `reason`        | `string`          | `"failed"` — payment declined or errored; `"expired"` — hold timer ran out |

---

### `arena:cart_opened`

Fires when the user opens the cart sheet from the mini-cart. Use this for a
`view_cart` analytics event. The count includes selected seats and GA units.

```typescript
interface ArenaCartOpenedDetail {
  sessionId: string;
  itemCount: number;
}
```

| Field       | Type     | Description                                      |
|-------------|----------|--------------------------------------------------|
| `sessionId` | `string` | ID of the event session currently being viewed  |
| `itemCount` | `number` | Total seats and GA units in the cart             |

```javascript
widget.addEventListener('arena:cart_opened', (e) => {
  analytics.track('view_cart', {
    session_id: e.detail.sessionId,
    item_count: e.detail.itemCount,
  });
});
```

---

### `arena:recovery`

Fires only after `POST /checkout/{token}/recover` succeeds, whether recovery
was initiated by the user or silently after a `401` status response. Restoring
a checkout token from the URL or session storage by itself does not fire this
event.

```typescript
interface ArenaRecoveryDetail {
  checkoutToken: string;
  expiresAt: string;
}
```

| Field           | Type     | Description                                      |
|-----------------|----------|--------------------------------------------------|
| `checkoutToken` | `string` | Checkout token returned by successful recovery  |
| `expiresAt`     | `string` | Fresh hold expiry as an ISO-8601 timestamp       |

```javascript
widget.addEventListener('arena:recovery', (e) => {
  analytics.track('checkout_recovered', {
    token: e.detail.checkoutToken,
    expires_at: e.detail.expiresAt,
  });
});
```

---

## TypeScript types

If you consume the widget as an npm package you can import the type definitions:

```typescript
import type {
  ARENA_EVENTS,
  ArenaSeatSelectedDetail,
  ArenaSeatReleasedDetail,
  ArenaPaymentStartedDetail,
  ArenaOrderPaidDetail,
  ArenaOrderFailedDetail,
  ArenaCartOpenedDetail,
  ArenaRecoveryDetail,
  ArenaEventDetailMap,
} from '@arena/widget';
```

`ARENA_EVENTS` is a const object with all event name strings so you avoid
magic string literals:

```typescript
import { ARENA_EVENTS } from '@arena/widget';

widget.addEventListener(ARENA_EVENTS.ORDER_PAID, (e) => {
  const detail = (e as CustomEvent<ArenaOrderPaidDetail>).detail;
  // ...
});
```

---

## Notes

- Events fire from the `<arena-tickets>` host element, not from inside the
  Shadow DOM.  Listeners attached to any ancestor (including `document`) will
  also receive the events because both `bubbles` and `composed` are `true`.
- `arena:seat_selected` / `arena:seat_released` are **client-side optimistic**:
  they reflect the widget's local selection state, not a backend reservation.
  The backend seat hold is only established when `arena:payment_started` fires.
- `arena:order_paid` is the authoritative signal — it is fired only after the
  widget reads a `status: "paid"` response from the backend.
