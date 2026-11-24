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
 *  - "Pay" attaches at most once at a time (a second press is `busy`).
 */

import { ApiError } from '../api.js';
import type { CheckoutBuyerPayload, CheckoutBuyerResponse, CheckoutStartResponse, PaymentBlock } from './checkout.js';
import type { DeferredPaymentHandle } from './paymentController.js';

export const MAX_PREPARES_PER_PAGE = 3;
export const PREPARE_DEBOUNCE_MS = 400;

/** The `preload-payment` attribute: '' (nothing), 'stripe' (Stripe.js only), 'checkout'. */
export type PreloadMode = 'none' | 'stripe' | 'checkout';

export function preloadMode(attr: string | null | undefined): PreloadMode {
  const v = (attr ?? '').trim().toLowerCase();
  if (v === 'checkout') return 'checkout';
  if (v === 'stripe') return 'stripe';
  return 'none';
}

/** "checkout" implies the Stripe.js preload of "stripe". */
export function loadsStripeEarly(mode: PreloadMode): boolean {
  return mode === 'stripe' || mode === 'checkout';
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
  /** The platform refused the buyer (422): show the error on the sheet. */
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
  /** Fire-and-forget cancel (keepalive); its failure is ignored. */
  cancel: (token: string) => Promise<unknown>;
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

  function cancelToken(token: string): Promise<void> {
    let p: Promise<void>;
    try {
      p = Promise.resolve(deps.cancel(token)).then(
        () => {},
        () => {},
      );
    } catch {
      p = Promise.resolve();
    }
    cancels.add(p);
    void p.then(() => cancels.delete(p));
    return p;
  }

  function dropCurrent(): Promise<void> {
    const c = current;
    if (!c) return Promise.resolve();
    current = null;
    handles.delete(c.token);
    deps.onChange(null);
    return cancelToken(c.token);
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
      if (!want || stopped || key !== forKey) {
        stale = true;
        return;
      }
      count += 1;
      let res: CheckoutStartResponse;
      try {
        res = await deps.prepare(newKey());
      } catch (err) {
        if (err instanceof ApiError && err.code === 'checkout.prepare_unavailable') disabled = true;
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
        void cancelToken(res.checkout_token);
        return;
      }
      current = {
        token: res.checkout_token,
        block,
        expiresAt: res.expires_at ?? null,
        paymentExpiresAt: res.payment_expires_at ?? res.expires_at ?? null,
        total: res.pricing?.total ?? null,
        currency: res.pricing?.currency ?? '',
        cartKey: forKey,
        failed: false,
      };
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
    await dropCurrent();
    await settled();
    return { kind: 'classic' };
  }

  return {
    update(nextWant, nextKey) {
      want = nextWant;
      key = nextKey;
      if (current && !paying && (!want || current.cartKey !== key)) void dropCurrent();
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
          if (err instanceof ApiError && err.status === 422) return { kind: 'refused', error: err };
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
