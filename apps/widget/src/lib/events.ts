/**
 * events.ts — CustomEvent contract for the <arena-tickets> web component.
 *
 * WID-S5: Host pages can listen for widget lifecycle events without
 * reaching into widget internals.  All events are dispatched with
 * `bubbles: true, composed: true` so they cross the Shadow DOM boundary
 * and are observable on the <arena-tickets> element itself.
 *
 * Quick-start for host pages:
 *
 * ```html
 * <arena-tickets id="widget" feed-token="…" session-id="…"></arena-tickets>
 * <script type="module">
 *   const widget = document.getElementById('widget');
 *
 *   widget.addEventListener('arena:seat_selected', (e) => {
 *     console.log('Seat selected:', e.detail.seatKey);
 *   });
 *
 *   widget.addEventListener('arena:payment_started', (e) => {
 *     console.log('Payment started, token:', e.detail.checkoutToken);
 *   });
 *
 *   widget.addEventListener('arena:order_paid', (e) => {
 *     console.log('Order paid! ref:', e.detail.orderRef);
 *   });
 * </script>
 * ```
 */

// ─── Event names ──────────────────────────────────────────────────────────────

/**
 * All CustomEvent names dispatched by the <arena-tickets> element.
 *
 * Events are namespaced with the `arena:` prefix to avoid collisions with
 * native DOM events and other widget libraries.
 */
export const ARENA_EVENTS = {
  /**
   * Fired when the user taps or clicks a seat to add it to their selection.
   * Not fired for GA (general admission) quantity changes.
   */
  SEAT_SELECTED: 'arena:seat_selected',

  /**
   * Fired when a seat is removed from the selection — either by tapping an
   * already-selected seat to deselect it, or by removing a cart line via
   * the cart sheet remove button.
   */
  SEAT_RELEASED: 'arena:seat_released',

  /**
   * Fired immediately after `POST /checkout/start` succeeds and the widget
   * is about to redirect the user to the payment provider page.
   * Use this to track funnel analytics (e.g. begin_checkout).
   */
  PAYMENT_STARTED: 'arena:payment_started',

  /**
   * Fired when the widget polls order status and receives `status: "paid"`.
   * Reliable signal that the purchase completed successfully.
   * Use this to fire purchase/conversion analytics events.
   */
  ORDER_PAID: 'arena:order_paid',

  /**
   * Fired when the widget polls order status and receives `status: "failed"`
   * or `status: "expired"`.
   */
  ORDER_FAILED: 'arena:order_failed',

  /**
   * WID-T2: Fired when the user opens the cart sheet (clicks the mini-cart bar).
   * Use this for "view_cart" analytics funnel events.
   */
  CART_OPENED: 'arena:cart_opened',

  /**
   * WID-T2: Fired when a held session is successfully recovered via
   * `POST /checkout/{token}/recover`, giving the user a fresh hold expiry.
   * Also fired on initial mount when a checkout token is restored from
   * sessionStorage (indicating an in-progress purchase is being resumed).
   */
  RECOVERY: 'arena:recovery',
} as const;

/** Union of all event name string literals. */
export type ArenaEventName = (typeof ARENA_EVENTS)[keyof typeof ARENA_EVENTS];

// ─── Event detail types ───────────────────────────────────────────────────────

/** Detail payload for `arena:seat_selected`. */
export interface ArenaSeatSelectedDetail {
  /** The seat key that was added to the selection (e.g. `"A01"`). */
  seatKey: string;
  /** ID of the event session the seat belongs to. */
  sessionId: string;
}

/** Detail payload for `arena:seat_released`. */
export interface ArenaSeatReleasedDetail {
  /** The seat key that was removed from the selection (e.g. `"A01"`). */
  seatKey: string;
  /** ID of the event session the seat belongs to. */
  sessionId: string;
}

/** Detail payload for `arena:payment_started`. */
export interface ArenaPaymentStartedDetail {
  /** Opaque checkout token from the platform. Pass to your analytics as a purchase ID. */
  checkoutToken: string;
  /** ID of the event session being purchased. */
  sessionId: string;
}

/** Detail payload for `arena:order_paid`. */
export interface ArenaOrderPaidDetail {
  /** Opaque checkout token identifying the completed order. */
  checkoutToken: string;
  /**
   * Human-readable order reference displayed on the ticket (e.g. `"ORD-12345"`).
   * May be `null` before the platform reconciles with the payment provider.
   */
  orderRef: string | null;
  /**
   * Order total in minor currency units (e.g. 2200 = €22.00), if available.
   * Use together with `currency` for analytics revenue events.
   */
  totalMinorUnits: number | null;
  /** ISO 4217 currency code (e.g. `"EUR"`, `"CZK"`), if available. */
  currency: string | null;
}

/** Detail payload for `arena:order_failed`. */
export interface ArenaOrderFailedDetail {
  /** Opaque checkout token, if one was established before the failure. */
  checkoutToken: string | null;
  /**
   * Machine-readable failure reason.
   * `"failed"`  — payment was declined or errored.
   * `"expired"` — the hold timer ran out before the user completed payment.
   */
  reason: string;
}

/** WID-T2: Detail payload for `arena:cart_opened`. */
export interface ArenaCartOpenedDetail {
  /** ID of the event session currently active in the widget. */
  sessionId: string;
  /** Total number of items (seats + GA units) in the cart at the time of opening. */
  itemCount: number;
}

/** WID-T2: Detail payload for `arena:recovery`. */
export interface ArenaRecoveryDetail {
  /** The checkout token that was recovered / resumed. */
  checkoutToken: string;
  /**
   * New expiry timestamp (ISO-8601) for the recovered hold, or an empty
   * string when the expiry is not yet known (e.g. sessionStorage resume
   * before the status response arrives).
   */
  expiresAt: string;
}

/**
 * Maps each event name to its strongly-typed detail shape.
 * Use this with `CustomEvent<ArenaEventDetailMap[K]>` for full type safety.
 */
export type ArenaEventDetailMap = {
  'arena:seat_selected': ArenaSeatSelectedDetail;
  'arena:seat_released': ArenaSeatReleasedDetail;
  'arena:payment_started': ArenaPaymentStartedDetail;
  'arena:order_paid': ArenaOrderPaidDetail;
  'arena:order_failed': ArenaOrderFailedDetail;
  'arena:cart_opened': ArenaCartOpenedDetail;
  'arena:recovery': ArenaRecoveryDetail;
};

// ─── Dispatch helper ──────────────────────────────────────────────────────────

/**
 * Dispatch a typed `CustomEvent` from `target`.
 *
 * All events are dispatched with:
 * - `bubbles: true`  — the event propagates up the regular DOM tree.
 * - `composed: true` — the event crosses the Shadow DOM boundary, making it
 *                      observable on the `<arena-tickets>` host element itself.
 *
 * @param target   The element to dispatch from.  Pass the `<arena-tickets>`
 *                 host element (obtained via `$host()` inside the component)
 *                 so the event originates from the expected target.
 * @param name     One of the `ARENA_EVENTS` constants.
 * @param detail   Strongly-typed payload matching the event name.
 */
export function dispatchWidgetEvent<K extends ArenaEventName>(
  target: EventTarget,
  name: K,
  detail: ArenaEventDetailMap[K],
): void {
  target.dispatchEvent(
    new CustomEvent(name, {
      detail,
      bubbles: true,
      composed: true,
    }),
  );
}
