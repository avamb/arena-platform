/**
 * The early order of preload-payment="checkout" (lib/preparedCheckout.ts),
 * driven through the real api.ts calls with a mocked fetch, and with a real
 * payment controller on a fake Stripe where the order of calls matters.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { postCheckoutBuyer, postCheckoutCancel, postCheckoutPrepare } from '../api.js';
import {
  createPreparedCheckout,
  PREPARE_DEBOUNCE_MS,
  PREPARED_STILL_HELD_CODE,
  isPlainCart,
  loadsStripeEarly,
  preloadMode,
  type PreparedCheckout,
} from './preparedCheckout.js';
import { createPaymentController, type DeferredPaymentHandle } from './paymentController.js';
import { createPaymentSteps } from './paymentSteps.js';
import type { CheckoutBuyerPayload, CheckoutPreparePayload } from './checkout.js';
import { StripeUnavailableError } from './stripeElements.js';

// ─── A fake platform behind fetch ────────────────────────────────────────────

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: Record<string, unknown> | null;
  keepalive: boolean;
}

let calls: Call[];
let order: string[];
let tokenSeq: number;
/** Overrides per route: return a Response to replace the default answer. */
let onPrepare: (() => Response | Promise<Response>) | null;
let onBuyer: (() => Response | Promise<Response>) | null;
let onCancel: ((token: string) => Response | Promise<Response>) | null;

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function errorEnvelope(status: number, code: string): Response {
  return json(status, { error: { code, message: code } });
}

function preparedAnswer(token: string): Response {
  return json(201, {
    checkout_session: {},
    redirect_url: '',
    checkout_token: token,
    expires_at: '2026-11-24T12:15:00Z',
    payment_expires_at: '2026-11-24T12:13:00Z',
    pricing: { total: 5250, currency: 'EUR' },
    payment: { provider: 'stripe', mode: 'elements', client_secret: `cs_${token}_secret`, publishable_key: 'pk_test' },
    prepared: true,
  });
}

beforeEach(() => {
  calls = [];
  order = [];
  tokenSeq = 0;
  onPrepare = null;
  onBuyer = null;
  onCancel = null;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = String(input);
      const call: Call = {
        url,
        method: init.method ?? 'GET',
        headers: (init.headers ?? {}) as Record<string, string>,
        body: typeof init.body === 'string' ? (JSON.parse(init.body) as Record<string, unknown>) : null,
        keepalive: init.keepalive === true,
      };
      calls.push(call);
      if (url.endsWith('/checkout/start')) {
        order.push('prepare');
        if (onPrepare) return onPrepare();
        tokenSeq += 1;
        return preparedAnswer(`tok${tokenSeq}`);
      }
      const m = /\/v1\/public\/checkout\/([^/]+)\/(buyer|cancel)$/.exec(url);
      if (m && m[2] === 'buyer') {
        order.push(`attach:${m[1]}`);
        if (onBuyer) return onBuyer();
        return json(200, {
          checkout_token: m[1],
          status: 'pending',
          expires_at: '2026-11-24T12:16:00Z',
          payment_expires_at: '2026-11-24T12:14:00Z',
        });
      }
      if (m && m[2] === 'cancel') {
        order.push(`cancel:${m[1]}`);
        if (onCancel) return onCancel(m[1]!);
        return json(200, { checkout_token: m[1], status: 'cancelled', cancelled: true });
      }
      return json(404, {});
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const PREPARE_BODY: CheckoutPreparePayload = {
  session_id: 'sess-1',
  seats: ['A-1-1'],
  capabilities: ['stripe_elements'],
  locale: 'en',
  prepare: true,
};

const BUYER: CheckoutBuyerPayload = {
  buyer: { email: 'buyer@example.com', name: null, phone: null },
  locale: 'en',
};

function make(extra: { handleWaitMs?: number; cancelRetryMs?: number } = {}) {
  const changes: Array<PreparedCheckout | null> = [];
  let keySeq = 0;
  const m = createPreparedCheckout({
    cancelRetryMs: 5,
    ...extra,
    prepare: (k) => postCheckoutPrepare('feed-1', PREPARE_BODY, '', k),
    attach: (t, b) => postCheckoutBuyer(t, b, ''),
    cancel: (t) => postCheckoutCancel(t, '', { keepalive: true }),
    onChange: (p) => changes.push(p),
    newIdempotencyKey: () => `idem-${++keySeq}`,
  });
  return { m, changes };
}

function fakeHandle(failed = false) {
  const reveal = vi.fn(async (email: string) => {
    order.push(`reveal:${email}`);
    return true;
  });
  const h: DeferredPaymentHandle & { reveal: typeof reveal } = { reveal, failed: () => failed };
  return h;
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 10; i++) await new Promise((r) => setTimeout(r, 0));
};

const prepares = () => calls.filter((c) => c.url.endsWith('/checkout/start'));
const cancels = () => calls.filter((c) => c.url.endsWith('/cancel'));
const attaches = () => calls.filter((c) => c.url.endsWith('/buyer'));

async function preparedWithHandle(handle = fakeHandle()) {
  const t = make();
  t.m.update(true, 'cart-A');
  await flush();
  const p = t.m.current!;
  t.m.setHandle(p.token, handle);
  return { ...t, p, handle };
}

// ─── Pure helpers ─────────────────────────────────────────────────────────────

describe('the preload-payment attribute', () => {
  it('reads "checkout", "stripe" and nothing else', () => {
    expect(preloadMode('checkout')).toBe('checkout');
    expect(preloadMode(' Checkout ')).toBe('checkout');
    expect(preloadMode('stripe')).toBe('stripe');
    expect(preloadMode('')).toBe('auto');
    expect(preloadMode(undefined)).toBe('auto');
    expect(preloadMode('none')).toBe('none');
    expect(preloadMode(' OFF ')).toBe('none');
    expect(preloadMode('yes')).toBe('none');
  });

  it('"checkout" implies the Stripe.js preload', () => {
    expect(loadsStripeEarly('checkout')).toBe(true);
    expect(loadsStripeEarly('stripe')).toBe(true);
    expect(loadsStripeEarly('none')).toBe(false);
    expect(loadsStripeEarly('none', true)).toBe(false);
    expect(loadsStripeEarly('stripe', false)).toBe(true);
  });

  it('auto loads early only when the session payment is embedded', () => {
    expect(loadsStripeEarly('auto', true)).toBe(true);
    expect(loadsStripeEarly('auto', false)).toBe(false);
    expect(loadsStripeEarly('auto', undefined)).toBe(false);
    expect(loadsStripeEarly('auto')).toBe(false);
  });
});

describe('a plain cart', () => {
  const base = { promoCode: null, promoBusy: false, marketingTicked: false, lineCount: 1, total: 2500 };
  it('is prepared', () => {
    expect(isPlainCart(base)).toBe(true);
  });
  it('is not with a promo code, a promo on its way or the marketing tick', () => {
    expect(isPlainCart({ ...base, promoCode: 'FRIEND10' })).toBe(false);
    expect(isPlainCart({ ...base, promoBusy: true })).toBe(false);
    expect(isPlainCart({ ...base, marketingTicked: true })).toBe(false);
  });
  it('is not when free or empty', () => {
    expect(isPlainCart({ ...base, total: 0 })).toBe(false);
    expect(isPlainCart({ ...base, lineCount: 0 })).toBe(false);
  });
});

// ─── Prepare and cancel ───────────────────────────────────────────────────────

describe('prepare', () => {
  it('runs when the sheet opens with a plain cart: no buyer, own Idempotency-Key', async () => {
    const { m, changes } = make();
    m.update(true, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(1);
    const c = prepares()[0]!;
    expect(c.body).toMatchObject({ prepare: true, capabilities: ['stripe_elements'], session_id: 'sess-1' });
    for (const k of ['buyer', 'holder_email', 'promo_code', 'marketing_consent', 'consent']) {
      expect(c.body).not.toHaveProperty(k);
    }
    expect(c.headers['Idempotency-Key']).toBe('idem-1');
    expect(changes).toHaveLength(1);
    expect(changes[0]).toMatchObject({ token: 'tok1', expiresAt: '2026-11-24T12:15:00Z', cartKey: 'cart-A', failed: false });
    expect(m.current?.block.client_secret).toBe('cs_tok1_secret');
  });

  it('does nothing while the cart is not plain (promo, marketing) or the attribute is not "checkout"', async () => {
    // The widget passes want=false in all those cases.
    const { m } = make();
    m.update(false, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(0);
  });

  it('is single-flight', async () => {
    const { m } = make();
    m.update(true, 'cart-A');
    m.update(true, 'cart-A');
    m.update(true, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(1);
  });

  it('is cancelled when the sheet closes (keepalive), and the screen goes away', async () => {
    const { m, changes } = await preparedWithHandle();
    m.update(false, 'cart-A');
    await flush();
    expect(cancels()).toHaveLength(1);
    expect(cancels()[0]!.url).toBe('/v1/public/checkout/tok1/cancel');
    expect(cancels()[0]!.keepalive).toBe(true);
    expect(changes[changes.length - 1]).toBeNull();
    expect(m.current).toBeNull();
  });

  it('is cancelled on a cart change and prepared again after the debounce, the cancel first', async () => {
    vi.useFakeTimers();
    const { m } = make();
    m.update(true, 'cart-A');
    await vi.advanceTimersByTimeAsync(0);
    expect(m.current?.token).toBe('tok1');
    m.update(true, 'cart-B');
    await vi.advanceTimersByTimeAsync(0);
    expect(cancels()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(300);
    expect(prepares()).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(150);
    expect(prepares()).toHaveLength(2);
    expect(order).toEqual(['prepare', 'cancel:tok1', 'prepare']);
    expect(prepares()[1]!.headers['Idempotency-Key']).toBe('idem-2');
    expect(m.current).toMatchObject({ token: 'tok2', cartKey: 'cart-B' });
  });

  it('a promo code or the marketing tick (want=false) cancels it and does not prepare again', async () => {
    vi.useFakeTimers();
    const { m } = make();
    m.update(true, 'cart-A');
    await vi.advanceTimersByTimeAsync(0);
    m.update(false, 'cart-A');
    await vi.advanceTimersByTimeAsync(2000);
    expect(cancels()).toHaveLength(1);
    expect(prepares()).toHaveLength(1);
  });

  it('prepares at most 3 times per page view', async () => {
    vi.useFakeTimers();
    const { m } = make();
    for (let i = 0; i < 6; i++) {
      m.update(true, `cart-${i}`);
      await vi.advanceTimersByTimeAsync(500);
    }
    expect(prepares()).toHaveLength(3);
    expect(m.prepareCount).toBe(3);
  });

  it('an answer that lands after the sheet closed is cancelled at once', async () => {
    let answer!: (r: Response) => void;
    onPrepare = () => new Promise<Response>((r) => (answer = r));
    const { m, changes } = make();
    m.update(true, 'cart-A');
    await flush();
    m.update(false, 'cart-A');
    answer(preparedAnswer('late'));
    await flush();
    expect(cancels().map((c) => c.url)).toEqual(['/v1/public/checkout/late/cancel']);
    expect(changes).toEqual([]);
  });

  it('is cancelled on pagehide', async () => {
    const { m } = await preparedWithHandle();
    m.pageHide();
    await flush();
    expect(cancels()).toHaveLength(1);
    expect(cancels()[0]!.keepalive).toBe(true);
  });

  it('stops after checkout.prepare_unavailable; "Pay" then goes the classic way', async () => {
    vi.useFakeTimers();
    onPrepare = () => errorEnvelope(409, 'checkout.prepare_unavailable');
    const { m } = make();
    m.update(true, 'cart-A');
    await vi.advanceTimersByTimeAsync(0);
    m.update(true, 'cart-B');
    await vi.advanceTimersByTimeAsync(1000);
    expect(prepares()).toHaveLength(1);
    const out = await m.pay({ plain: true, cartKey: 'cart-B', email: 'a@b.cz', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(attaches()).toHaveLength(0);
  });
});

// ─── Pay ──────────────────────────────────────────────────────────────────────

describe('pay with a prepared checkout', () => {
  it('attaches the buyer, then reveals with the e-mail', async () => {
    const { m, handle } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('attached');
    if (out.kind !== 'attached') return;
    expect(out.prepared.token).toBe('tok1');
    expect(out.attach?.payment_expires_at).toBe('2026-11-24T12:14:00Z');
    expect(attaches()).toHaveLength(1);
    expect(attaches()[0]!.body).toEqual(BUYER);
    expect(handle.reveal).toHaveBeenCalledWith('buyer@example.com');
    expect(order).toEqual(['prepare', 'attach:tok1', 'reveal:buyer@example.com']);
    // Consumed by the widget: never cancelled afterwards.
    m.consume('tok1');
    m.update(false, 'cart-A');
    m.pageHide();
    await flush();
    expect(cancels()).toHaveLength(0);
  });

  it('a second "Pay" while the first runs attaches nothing more', async () => {
    const { m } = await preparedWithHandle();
    const [a, b] = await Promise.all([
      m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER }),
      m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER }),
    ]);
    expect(a.kind).toBe('attached');
    expect(b.kind).toBe('busy');
    expect(attaches()).toHaveLength(1);
  });

  it('attach 409 cancels the prepared checkout, waits for it, then says classic', async () => {
    onBuyer = () => errorEnvelope(409, 'checkout.not_payable');
    const { m, handle } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(order).toEqual(['prepare', 'attach:tok1', 'cancel:tok1']);
    expect(handle.reveal).not.toHaveBeenCalled();
    // No new prepare may race the classic start.
    m.update(true, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(1);
  });

  it('a network error on attach is classic too', async () => {
    onBuyer = () => {
      throw new TypeError('Failed to fetch');
    };
    const { m } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(cancels()).toHaveLength(1);
  });

  it('attach 422 is the buyer\'s to fix: refused, the prepared checkout stays', async () => {
    onBuyer = () => errorEnvelope(422, 'checkout.buyer_name_required');
    const { m, handle } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('refused');
    if (out.kind === 'refused') expect((out.error as { code?: string }).code).toBe('checkout.buyer_name_required');
    expect(cancels()).toHaveLength(0);
    expect(handle.reveal).not.toHaveBeenCalled();
    expect(m.current?.token).toBe('tok1');
    onBuyer = null;
    const again = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(again.kind).toBe('attached');
  });

  it('a cart that is no longer plain (marketing ticked on the sheet) goes classic, the prepared one cancelled', async () => {
    const { m } = await preparedWithHandle();
    const out = await m.pay({ plain: false, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(attaches()).toHaveLength(0);
    expect(order).toEqual(['prepare', 'cancel:tok1']);
  });

  it('a failed hidden form goes classic without an attach', async () => {
    const { m } = await preparedWithHandle(fakeHandle(true));
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(attaches()).toHaveLength(0);
    expect(cancels()).toHaveLength(1);
  });

  it('a prepare still on its way is waited for', async () => {
    let answer!: (r: Response) => void;
    onPrepare = () => new Promise<Response>((r) => (answer = r));
    const { m } = make();
    m.update(true, 'cart-A');
    await flush();
    const paying = m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    answer(preparedAnswer('tokW'));
    // The hidden screen mounts on the next render and hands over its handle.
    await flush();
    m.setHandle('tokW', fakeHandle());
    const out = await paying;
    expect(out.kind).toBe('attached');
    expect(order).toEqual(['prepare', 'attach:tokW', 'reveal:buyer@example.com']);
  });

  it('a hidden screen that never mounts: classic after a short wait', async () => {
    const { m } = make({ handleWaitMs: 20 });
    m.update(true, 'cart-A');
    await flush();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(attaches()).toHaveLength(0);
    expect(cancels()).toHaveLength(1);
  });
});

// ─── A prepare without an answer (M1) ─────────────────────────────────────────

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

describe('a prepare that throws without an HTTP answer', () => {
  it('is sent once more with the SAME key, and the replayed hold is cancelled', async () => {
    let n = 0;
    onPrepare = () => {
      n += 1;
      if (n === 1) throw new TypeError('Failed to fetch');
      tokenSeq += 1;
      return preparedAnswer(`tok${tokenSeq}`);
    };
    const { m, changes } = make({ handleWaitMs: 20 });
    m.update(true, 'cart-A');
    await flush();
    await m.settled();
    expect(prepares()).toHaveLength(2);
    expect(prepares()[0]!.headers['Idempotency-Key']).toBe('idem-1');
    expect(prepares()[1]!.headers['Idempotency-Key']).toBe('idem-1');
    expect(cancels().map((c) => c.url)).toEqual(['/v1/public/checkout/tok1/cancel']);
    // The replayed hold is never shown as the prepared one.
    expect(changes.filter((c) => c?.token === 'tok1')).toEqual([]);
    // Once the cancel is through a fresh prepare (new key) may run.
    await wait(PREPARE_DEBOUNCE_MS + 50);
    expect(prepares()).toHaveLength(3);
    expect(prepares()[2]!.headers['Idempotency-Key']).toBe('idem-2');
    expect(m.current?.token).toBe('tok2');
  });

  it('a 5xx is retried the same way', async () => {
    let n = 0;
    onPrepare = () => {
      n += 1;
      if (n === 1) return errorEnvelope(502, 'http_502');
      tokenSeq += 1;
      return preparedAnswer(`tok${tokenSeq}`);
    };
    const { m } = make();
    m.update(true, 'cart-A');
    await flush();
    await m.settled();
    expect(prepares()).toHaveLength(2);
    expect(prepares()[1]!.headers['Idempotency-Key']).toBe('idem-1');
    expect(cancels()).toHaveLength(1);
    m.destroy();
  });

  it('a retry that also fails turns prepare off for the page view', async () => {
    vi.useFakeTimers();
    onPrepare = () => {
      throw new TypeError('Failed to fetch');
    };
    const { m } = make();
    m.update(true, 'cart-A');
    await vi.advanceTimersByTimeAsync(0);
    expect(prepares()).toHaveLength(2);
    m.update(true, 'cart-B');
    await vi.advanceTimersByTimeAsync(2000);
    expect(prepares()).toHaveLength(2);
    const out = await m.pay({ plain: true, cartKey: 'cart-B', email: 'a@b.cz', body: BUYER });
    expect(out.kind).toBe('classic');
  });

  it('a 4xx answer is not retried: nothing was held', async () => {
    onPrepare = () => errorEnvelope(409, 'checkout.seat_unavailable');
    const { m } = make();
    m.update(true, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(1);
    expect(cancels()).toHaveLength(0);
    m.destroy();
  });
});

// ─── A cancel that does not get through (M2) ──────────────────────────────────

describe('a cancel counts only when the checkout is gone', () => {
  it('409 payment_in_progress is sent once more; through the second time, prepare goes on', async () => {
    let n = 0;
    onCancel = (t) => {
      n += 1;
      return n === 1 ? errorEnvelope(409, 'checkout.payment_in_progress') : json(200, { checkout_token: t, status: 'cancelled', cancelled: true });
    };
    const { m } = await preparedWithHandle();
    m.update(true, 'cart-B');
    await wait(PREPARE_DEBOUNCE_MS + 50);
    expect(cancels()).toHaveLength(2);
    expect(prepares()).toHaveLength(2);
    expect(m.current?.token).toBe('tok2');
    m.destroy();
  });

  it('404 and status expired count as done', async () => {
    onCancel = () => errorEnvelope(404, 'checkout.not_found');
    const a = await preparedWithHandle();
    a.m.update(false, 'cart-A');
    await a.m.settled();
    expect(cancels()).toHaveLength(1);
    calls = [];
    order = [];
    onCancel = (t) => json(200, { checkout_token: t, status: 'expired', cancelled: false });
    const b = await preparedWithHandle();
    b.m.update(false, 'cart-A');
    await b.m.settled();
    expect(cancels()).toHaveLength(1);
  });

  it('never cancelled (5xx, then "paid"): no new hold, and Pay with the same cart attaches to it', async () => {
    let n = 0;
    onCancel = (t) => {
      n += 1;
      return n === 1 ? errorEnvelope(503, 'http_503') : json(200, { checkout_token: t, status: 'paid', cancelled: false });
    };
    const { m, changes } = await preparedWithHandle();
    // The sheet closes: the cancel fails twice.
    m.update(false, 'cart-A');
    await m.settled();
    expect(cancels()).toHaveLength(2);
    expect(m.current).toBeNull();
    // The sheet opens again with the same cart: the live checkout comes back, no prepare.
    m.update(true, 'cart-A');
    await flush();
    expect(prepares()).toHaveLength(1);
    expect(m.current?.token).toBe('tok1');
    expect(changes[changes.length - 1]?.token).toBe('tok1');
    m.setHandle('tok1', fakeHandle());
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('attached');
    expect(attaches().map((c) => c.url)).toEqual(['/v1/public/checkout/tok1/buyer']);
    expect(prepares()).toHaveLength(1);
  });

  it('never cancelled: a cart change prepares nothing; Pay restores it with the original cart', async () => {
    onCancel = () => errorEnvelope(409, 'checkout.payment_in_progress');
    const { m } = await preparedWithHandle();
    m.update(true, 'cart-B');
    await m.settled();
    await wait(PREPARE_DEBOUNCE_MS + 50);
    expect(prepares()).toHaveLength(1);
    expect(m.current).toBeNull();
    // The buyer put the cart back: Pay attaches to the live checkout.
    m.update(true, 'cart-A');
    const paying = m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    await flush();
    m.setHandle('tok1', fakeHandle());
    const out = await paying;
    expect(out.kind).toBe('attached');
    expect(prepares()).toHaveLength(1);
  });

  it('never cancelled and the cart changed: Pay tries once more, then refuses instead of a second hold', async () => {
    onCancel = () => errorEnvelope(409, 'checkout.payment_in_progress');
    const { m } = await preparedWithHandle();
    m.update(true, 'cart-B');
    await m.settled();
    expect(cancels()).toHaveLength(2);
    const out = await m.pay({ plain: true, cartKey: 'cart-B', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('refused');
    if (out.kind === 'refused') expect((out.error as { code?: string }).code).toBe(PREPARED_STILL_HELD_CODE);
    expect(cancels()).toHaveLength(4);
    expect(prepares()).toHaveLength(1);
    expect(attaches()).toHaveLength(0);
    // The extra try gets through: classic is allowed again.
    onCancel = null;
    const again = await m.pay({ plain: true, cartKey: 'cart-B', email: 'buyer@example.com', body: BUYER });
    expect(again.kind).toBe('classic');
    expect(prepares()).toHaveLength(1);
  });

  it('an attach failure whose cancel does not get through refuses instead of a classic start', async () => {
    onBuyer = () => errorEnvelope(409, 'checkout.buyer_already_set');
    onCancel = () => errorEnvelope(500, 'http_500');
    const { m } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('refused');
    if (out.kind === 'refused') expect((out.error as { code?: string }).code).toBe(PREPARED_STILL_HELD_CODE);
    expect(cancels()).toHaveLength(2);
  });
});

describe('attach 400 checkout.invalid_email (L3)', () => {
  it('is the buyer\'s to fix like a 422: refused, the prepared checkout stays', async () => {
    onBuyer = () => errorEnvelope(400, 'checkout.invalid_email');
    const { m, handle } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'bad@', body: BUYER });
    expect(out.kind).toBe('refused');
    if (out.kind === 'refused') expect((out.error as { code?: string }).code).toBe('checkout.invalid_email');
    expect(cancels()).toHaveLength(0);
    expect(handle.reveal).not.toHaveBeenCalled();
    expect(m.current?.token).toBe('tok1');
    onBuyer = null;
    const again = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(again.kind).toBe('attached');
  });

  it('another 400 is not the buyer\'s: classic', async () => {
    onBuyer = () => errorEnvelope(400, 'validation.bad_request');
    const { m } = await preparedWithHandle();
    const out = await m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(cancels()).toHaveLength(1);
  });
});

// ─── With a real controller on a fake Stripe ──────────────────────────────────

function fakeStripe(opts: { openFails?: boolean } = {}) {
  const el = () => {
    const handlers: Record<string, (e: unknown) => void> = {};
    return {
      handlers,
      on: (ev: string, fn: (e: unknown) => void) => {
        handlers[ev] = fn;
      },
      mount: vi.fn(),
      destroy: vi.fn(),
    };
  };
  const express = el();
  const payment = el();
  const updateEmail = vi.fn(async (email: string) => {
    order.push(`updateEmail:${email}`);
    return { type: 'success' as const };
  });
  const checkout = {
    on: () => {},
    createExpressCheckoutElement: () => express,
    createPaymentElement: () => payment,
    destroy: vi.fn(),
    loadActions: vi.fn(),
  };
  const open = vi.fn(async () => {
    if (opts.openFails) throw new StripeUnavailableError('load');
    return { checkout, actions: { confirm: vi.fn(), updateEmail } };
  });
  return { open, payment, express, updateEmail };
}

function controllerFor(m: ReturnType<typeof make>['m'], token: string, stripe: ReturnType<typeof fakeStripe>) {
  const steps = createPaymentSteps();
  const stepsSeen: string[] = [];
  steps.subscribe((e) => stepsSeen.push(e.step));
  const postHosted = vi.fn(async () => ({ redirect_url: 'https://checkout.stripe.com/x' }));
  const redirect = vi.fn();
  const ctrl = createPaymentController({
    block: m.current!.block,
    locale: 'en',
    steps,
    getStatus: vi.fn(),
    postHosted,
    redirect,
    onState: () => {},
    open: stripe.open as never,
    walletWaitMs: 5,
    deferred: true,
    onDeferredFailure: () => m.markFailed(token),
  });
  m.setHandle(token, { reveal: (e) => ctrl.reveal(e), failed: () => ctrl.hasFailed() });
  return { ctrl, postHosted, redirect, stepsSeen };
}

describe('the hidden payment screen', () => {
  it('attach comes before Stripe gets the e-mail; the form view only once shown', async () => {
    const t = make();
    t.m.update(true, 'cart-A');
    await flush();
    const stripe = fakeStripe();
    const c = controllerFor(t.m, 'tok1', stripe);
    await c.ctrl.start({ express: 'E', payment: 'P' });
    stripe.express.handlers['availablepaymentmethodschange']?.({ availablePaymentMethods: {} });
    stripe.payment.handlers['ready']?.({});
    expect(c.stepsSeen).toEqual([]);
    const out = await t.m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('attached');
    await flush();
    expect(order).toEqual(['prepare', 'attach:tok1', 'updateEmail:buyer@example.com']);
    // Attached and e-mail taken, but the widget has not switched to the screen yet.
    expect(c.stepsSeen).not.toContain('checkout_form_view');
    c.ctrl.markShown();
    expect(c.stepsSeen).toContain('checkout_form_view');
    c.ctrl.destroy();
  });

  it('a Stripe failure before Pay: no redirect, and Pay goes the classic way', async () => {
    const t = make();
    t.m.update(true, 'cart-A');
    await flush();
    const stripe = fakeStripe({ openFails: true });
    const c = controllerFor(t.m, 'tok1', stripe);
    await c.ctrl.start({ express: 'E', payment: 'P' });
    expect(c.postHosted).not.toHaveBeenCalled();
    expect(c.redirect).not.toHaveBeenCalled();
    expect(t.m.current?.failed).toBe(true);
    const out = await t.m.pay({ plain: true, cartKey: 'cart-A', email: 'buyer@example.com', body: BUYER });
    expect(out.kind).toBe('classic');
    expect(attaches()).toHaveLength(0);
    expect(order).toEqual(['prepare', 'cancel:tok1']);
    expect(c.redirect).not.toHaveBeenCalled();
    c.ctrl.destroy();
  });
});
