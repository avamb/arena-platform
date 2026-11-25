/**
 * preparedCheckout.ts — the early order ("prepare + attach") of
 * preload-payment="checkout", with no DOM and no Svelte so tests can drive it.
 *
 * When the order sheet opens with a PLAIN cart (no promo code, no marketing
 * tick, total > 0, seats or tickets present) the widget calls checkout/start
 * with `prepare: true`: the order, the hold and the embedded Stripe session
 * exist before the buyer has typed anything, and the payment screen loads
 * hidden. On "Pay" the buyer is attached (POST .../buyer) and the hidden screen
 * is revealed. Anything else goes the classic way (checkout/start at "Pay").
 *
 * Rules kept here:
 *  - one prepare at a time (single-flight), each with its own Idempotency-Key,
 *    at most MAX_PREPARES_PER_PAGE per page view;
 *  - a prepared, unattached checkout is cancelled when the sheet closes, when
 *    the cart, the promo code or the marketing tick changes, and on pagehide;
 *    after a change it is prepared again (debounced) if the cart is still plain;
 *  - a cancel is awaited before a new prepare or a classic start, so the new
 *    hold never collides with the old one on the same places;
 *  - a cancel counts as done only when its answer says the checkout is
 *    cancelled or expired (or 404). Otherwise (409 payment_in_progress, 5xx,
 *    network) it is sent once more after CANCEL_RETRY_MS; a checkout still not
 *    cancelled then stays "live": no new hold is taken for this page view (no
 *    prepare, no classic start), and "Pay" with the same cart attaches to it;
 *  - a prepare that throws without an HTTP answer (network, timeout, 5xx) may
 *    have committed a hold: it is sent once more with the SAME Idempotency-Key
 *    (the platform replays its stored answer) to learn the token and cancel
 *    it; if that fails too, prepare is off for this page view;
 *  - "Pay" attaches at most once at a time (a second press is `busy`).
 */

import { ApiError } from '../api.js';
import type { CheckoutBuyerPayload, CheckoutBuyerResponse, CheckoutStartResponse, PaymentBlock } from './checkout.js';
import type { DeferredPaymentHandle } from './paymentController.js';

export const MAX_PREPARES_PER_PAGE = 3;
export const PREPARE_DEBOUNCE_MS = 400;
/** The wait before a cancel that did not get through is sent once more. */
export const CANCEL_RETRY_MS = 2000;
/** "Pay" is refused with this code when a prepared hold could not be released. */
export const PREPARED_STILL_HELD_CODE = 'checkout.prepared_still_held';

/**
 * The `preload-payment` attribute: '' / absent = 'auto' (Stripe.js early only
 * for a session whose payment will be embedded, `embedded_payment`), 'none' or
 * 'off' = nothing, 'stripe' (Stripe.js early, explicit), 'checkout' (early
 * order). An unknown value means 'none'.
 */
export type PreloadMode = 'auto' | 'none' | 'stripe' | 'checkout';

export function preloadMode(attr: string | null | undefined): PreloadMode {
  const v = (attr ?? '').trim().toLowerCase();
  if (v === '') return 'auto';
  if (v === 'checkout') return 'checkout';
  if (v === 'stripe') return 'stripe';
  return 'none';
}

/**
 * Whether Stripe.js (and the preconnect) may be loaded ahead of "Pay".
 * "checkout" implies "stripe" (explicit). 'auto' follows the feed session's
 * `embedded_payment`: a hosted-page organizer's buyer downloads nothing.
 */
export function loadsStripeEarly(mode: PreloadMode, embeddedPayment: boolean | null | undefined = false): boolean {
  if (mode === 'stripe' || mode === 'checkout') return true;
  return mode === 'auto' && embeddedPayment === true;
}

export interface PlainCartInput {
  /** The applied promo code, if any. */
  promoCode: string | null;
  /** A promo request is in flight (a code may be about to apply). */
  promoBusy: boolean;
  marketingTicked: boolean;
  /** Seat and GA lines in the cart. */
  lineCount: number;
  /** Cart subtotal, minor units. */
  total: number;
}

/** A cart the early order may take: everything e-mail-dependent stays classic. */
export function isPlainCart(c: PlainCartInput): boolean {
  return (
    !(c.promoCode ?? '').trim() &&
    !c.promoBusy &&
    !c.marketingTicked &&
    c.lineCount > 0 &&
    Number.isFinite(c.total) &&
    c.total > 0
  );
}

export interface PreparedCheckout {
  token: string;
  block: PaymentBlock;
  /** The hold (the sheet's countdown). */
  expiresAt: string | null;
  /** The payment window (the payment screen's clock). */
  paymentExpiresAt: string | null;
  total: number | null;
  currency: string;
  /** The cart this checkout was prepared for. */
  cartKey: string;
  /** The hidden form failed to load: "Pay" goes the classic way. */
  failed: boolean;
}

export type PayOutcome =
  /** Attached and revealed: show the payment screen of `prepared`. */
  | { kind: 'attached'; prepared: PreparedCheckout; attach: CheckoutBuyerResponse | null }
  /**
   * The platform refused the buyer (422, or 400 `checkout.invalid_email`), or a
   * prepared hold could not be released (`checkout.prepared_still_held`):
   * show the error on the sheet.
   */
  | { kind: 'refused'; error: unknown }
  /** No usable prepared checkout (any is cancelled by now): run checkout/start. */
  | { kind: 'classic' }
  /** Another "Pay" is already running: do nothing. */
  | { kind: 'busy' };

export interface PayInput {
  /** The cart is plain and the sheet's marketing box is not ticked. */
  plain: boolean;
  cartKey: string;
  email: string;
  body: CheckoutBuyerPayload;
}

export interface PreparedCheckoutDeps {
  /** checkout/start with prepare:true for the cart as it is now. */
  prepare: (idempotencyKey: string) => Promise<CheckoutStartResponse>;
  attach: (token: string, body: CheckoutBuyerPayload) => Promise<CheckoutBuyerResponse | null | void>;
  /**
   * Cancel (keepalive). Done only when the answer says `cancelled: true` or a
   * status `cancelled`/`expired`, or the call fails with 404.
   */
  cancel: (token: string) => Promise<unknown>;
  /** The wait before a cancel is sent once more (default CANCEL_RETRY_MS). */
  cancelRetryMs?: number;
  /** The prepared checkout appeared, was replaced or went away. */
  onChange: (prepared: PreparedCheckout | null) => void;
  newIdempotencyKey?: () => string;
  maxPrepares?: number;
  debounceMs?: number;
  /** How long "Pay" waits for the hidden screen of a just-prepared checkout (default 1 s). */
  handleWaitMs?: number;
}

export interface PreparedCheckoutManager {
  /** Called whenever the sheet, the cart or the plain-cart condition changes. */
  update(want: boolean, cartKey: string): void;
  /** The hidden payment screen of `token` is mounted (or gone, with null). */
  setHandle(token: string, handle: DeferredPaymentHandle | null): void;
  /** The hidden form of `token` failed to load. */
  markFailed(token: string): void;
  /** The sheet's "Pay". */
  pay(input: PayInput): Promise<PayOutcome>;
  /** After `attached`: the checkout is the widget's now and is never cancelled here. */
  consume(token: string): void;
  /** pagehide: cancel what is prepared and stop. */
  pageHide(): void;
  /** Every cancel sent so far has answered. */
  settled(): Promise<void>;
  readonly current: PreparedCheckout | null;
  readonly prepareCount: number;
  destroy(): void;
}

function randomKey(): string {
  try {
    const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
    if (c?.randomUUID) return c.randomUUID();
  } catch {
    /* fall through */
  }
  return `prep-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

function usableBlock(b: PaymentBlock | undefined): b is PaymentBlock {
  return !!b && b.mode === 'elements' && !!b.client_secret && !!b.publishable_key;
}

/** The cancel answer says the checkout is gone. */
function cancelConfirmed(answer: unknown): boolean {
  if (!answer || typeof answer !== 'object') return false;
  const a = answer as { cancelled?: unknown; status?: unknown };
  if (a.cancelled === true) return true;
  return a.status === 'cancelled' || a.status === 'expired';
}

/** A prepare that failed with a definite HTTP answer (4xx): nothing was held. */
function definiteRefusal(err: unknown): boolean {
  return err instanceof ApiError && err.status >= 400 && err.status < 500;
}

/** The buyer's own mistake on attach: shown on the sheet, the checkout kept. */
function buyerRefusal(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 422 || err.code === 'checkout.invalid_email');
}

function toPrepared(res: CheckoutStartResponse, block: PaymentBlock, cartKey: string): PreparedCheckout {
  return {
    token: res.checkout_token,
    block,
    expiresAt: res.expires_at ?? null,
    paymentExpiresAt: res.payment_expires_at ?? res.expires_at ?? null,
    total: res.pricing?.total ?? null,
    currency: res.pricing?.currency ?? '',
    cartKey,
    failed: false,
  };
}

export function createPreparedCheckout(deps: PreparedCheckoutDeps): PreparedCheckoutManager {
  const max = deps.maxPrepares ?? MAX_PREPARES_PER_PAGE;
  const debounce = deps.debounceMs ?? PREPARE_DEBOUNCE_MS;
  const newKey = deps.newIdempotencyKey ?? randomKey;

  let want = false;
  let key = '';
  let current: PreparedCheckout | null = null;
  let inflight: Promise<void> | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let count = 0;
  // The channel or the cart cannot be prepared: no more attempts this page view.
  let disabled = false;
  let paying = false;
  let stopped = false;
  const cancels = new Set<Promise<void>>();
  const handles = new Map<string, DeferredPaymentHandle>();
  const handleWaiters = new Map<string, () => void>();
  const handleWaitMs = deps.handleWaitMs ?? 1000;
  const cancelRetryMs = deps.cancelRetryMs ?? CANCEL_RETRY_MS;
  // A prepared checkout whose cancel never got through: it still holds the
  // places, so no new hold is taken; "Pay" with the same cart attaches to it.
  let live: PreparedCheckout | null = null;

  function attemptCancel(token: string): Promise<boolean> {
    try {
      return Promise.resolve(deps.cancel(token)).then(cancelConfirmed, (err: unknown) =>
        err instanceof ApiError && err.status === 404,
      );
    } catch {
      return Promise.resolve(false);
    }
  }

  /** Cancels `token`, once more after CANCEL_RETRY_MS; true when it is gone. */
  function cancelToken(token: string): Promise<boolean> {
    const p = (async () => {
      if (await attemptCancel(token)) return true;
      await new Promise<void>((r) => setTimeout(r, cancelRetryMs));
      return attemptCancel(token);
    })();
    const tracked = p.then(
      () => {},
      () => {},
    );
    cancels.add(tracked);
    void tracked.then(() => cancels.delete(tracked));
    return p;
  }

  /** Cancels a prepared checkout; when that fails it stays live and prepare stops. */
  async function cancelPrepared(c: PreparedCheckout): Promise<boolean> {
    const ok = await cancelToken(c.token);
    if (!ok) {
      disabled = true;
      clearTimer();
      live = { ...c, failed: false };
    }
    return ok;
  }

  function dropCurrent(): Promise<boolean> {
    const c = current;
    if (!c) return Promise.resolve(true);
    current = null;
    handles.delete(c.token);
    deps.onChange(null);
    return cancelPrepared(c);
  }

  /** Brings a live checkout back as the prepared one (its hidden screen mounts again). */
  function restoreLive(): void {
    if (!live || current) return;
    current = live;
    live = null;
    deps.onChange(current);
  }

  function liveFits(cartKey: string): boolean {
    return !!live && !current && live.cartKey === cartKey;
  }

  function settled(): Promise<void> {
    return Promise.all([...cancels]).then(() => {});
  }

  function clearTimer(): void {
    if (timer) clearTimeout(timer);
    timer = null;
  }

  function canPrepare(): boolean {
    return want && !stopped && !current && !inflight && !paying && !disabled && count < max;
  }

  function schedule(): void {
    clearTimer();
    if (!canPrepare()) return;
    // The first prepare goes at once; a re-prepare after a change waits for
    // the buyer to stop changing things.
    if (count === 0) run();
    else timer = setTimeout(() => {
      timer = null;
      run();
    }, debounce);
  }

  function run(): void {
    if (!canPrepare()) return;
    const forKey = key;
    let stale = false;
    inflight = (async () => {
      // The previous hold must be gone before this one takes the same places.
      await settled();
      if (disabled) {
        // A cancel did not get through: no new hold. The live checkout comes
        // back if it fits the cart on the sheet.
        if (want && !stopped && !paying && liveFits(key)) restoreLive();
        return;
      }
      if (!want || stopped || key !== forKey) {
        stale = true;
        return;
      }
      count += 1;
      const idem = newKey();
      let res: CheckoutStartResponse;
      try {
        res = await deps.prepare(idem);
      } catch (err) {
        if (definiteRefusal(err)) {
          if ((err as ApiError).code === 'checkout.prepare_unavailable') disabled = true;
          return;
        }
        // No HTTP answer (network, timeout) or a 5xx: the hold may exist
        // without the widget knowing its token. The same key once more: the
        // platform replays its stored answer, and the hold is cancelled.
        let replay: CheckoutStartResponse;
        try {
          replay = await deps.prepare(idem);
        } catch {
          disabled = true;
          return;
        }
        if (!replay.checkout_token) {
          disabled = true;
          return;
        }
        if (usableBlock(replay.payment)) {
          void cancelPrepared(toPrepared(replay, replay.payment, forKey));
        } else {
          disabled = true;
          void cancelToken(replay.checkout_token);
        }
        // Prepared again (fresh key) once the cancel is through, if still wanted.
        stale = true;
        return;
      }
      const block = res.payment;
      if (!usableBlock(block) || !res.checkout_token) {
        // Not an embedded answer: this channel pays the classic way.
        disabled = true;
        if (res.checkout_token) void cancelToken(res.checkout_token);
        return;
      }
      if (!want || stopped || key !== forKey) {
        // The sheet closed or the cart moved while it was being prepared.
        stale = true;
        void cancelPrepared(toPrepared(res, block, forKey));
        return;
      }
      current = toPrepared(res, block, forKey);
      deps.onChange(current);
    })().finally(() => {
      inflight = null;
      if (stale) schedule();
    });
  }

  function waitForHandle(token: string): Promise<void> {
    return new Promise<void>((resolve) => {
      const t = setTimeout(() => {
        handleWaiters.delete(token);
        resolve();
      }, handleWaitMs);
      handleWaiters.set(token, () => {
        clearTimeout(t);
        resolve();
      });
    });
  }

  async function classic(): Promise<PayOutcome> {
    // From here this page view pays the classic way: a new prepare must not
    // race the classic start for the same places.
    disabled = true;
    const before = live;
    await dropCurrent();
    await settled();
    if (live && live === before) {
      // Left over from earlier: one more try before a new hold is taken.
      live = null;
      await cancelPrepared(before);
    }
    if (live) {
      // A prepared hold is still on the places: a classic start would take a
      // second one. The buyer sees an error and the checkout stays live.
      return {
        kind: 'refused',
        error: new ApiError('a prepared checkout still holds the places', {
          status: 409,
          code: PREPARED_STILL_HELD_CODE,
        }),
      };
    }
    return { kind: 'classic' };
  }

  return {
    update(nextWant, nextKey) {
      want = nextWant;
      key = nextKey;
      if (current && !paying && (!want || current.cartKey !== key)) void dropCurrent();
      else if (want && !paying && !stopped && !inflight && liveFits(key)) restoreLive();
      schedule();
    },

    setHandle(token, handle) {
      if (handle) {
        handles.set(token, handle);
        const w = handleWaiters.get(token);
        handleWaiters.delete(token);
        w?.();
      } else handles.delete(token);
    },

    markFailed(token) {
      if (current && current.token === token) current.failed = true;
    },

    async pay(input) {
      if (paying) return { kind: 'busy' };
      paying = true;
      clearTimer();
      try {
        // A prepare on its way is waited for: it is the quickest path.
        if (inflight) await inflight;
        // A checkout whose cancel never got through still holds the places:
        // with the same cart it is the one to pay (never a second hold). A
        // cancel still on its way is waited for first: it decides which.
        if (!current) await settled();
        if (input.plain && liveFits(input.cartKey)) restoreLive();
        const c = current;
        // A prepare that just answered: its hidden screen mounts on the next
        // render, so give it a moment to hand over its handle.
        if (c && !handles.has(c.token)) await waitForHandle(c.token);
        const h = c ? handles.get(c.token) : undefined;
        if (!c || !input.plain || c.cartKey !== input.cartKey || c.failed || !h || h.failed()) {
          return await classic();
        }
        let attached: CheckoutBuyerResponse | null = null;
        try {
          attached = (await deps.attach(c.token, input.body)) ?? null;
        } catch (err) {
          // 422 and 400 checkout.invalid_email: the buyer fixes it on the sheet.
          if (buyerRefusal(err)) return { kind: 'refused', error: err };
          // 404, 409 (not payable, another buyer), a network error.
          return await classic();
        }
        if (current !== c || c.failed || h.failed()) return await classic();
        // Not awaited: Stripe's updateEmail must not hold the screen back; the
        // controller blocks confirm until it answered.
        void h.reveal(input.email);
        return { kind: 'attached', prepared: c, attach: attached };
      } finally {
        paying = false;
      }
    },

    consume(token) {
      if (current && current.token === token) {
        current = null;
        handles.delete(token);
        deps.onChange(null);
      }
    },

    pageHide() {
      clearTimer();
      want = false;
      if (current && !paying) void dropCurrent();
    },

    settled,

    get current() {
      return current;
    },

    get prepareCount() {
      return count;
    },

    destroy() {
      stopped = true;
      clearTimer();
      if (current && !paying) void dropCurrent();
    },
  };
}
