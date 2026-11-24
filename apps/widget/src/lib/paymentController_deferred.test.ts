/**
 * Deferred mode of the payment controller (preload-payment="checkout"): the
 * form of a prepared checkout loads hidden while the buyer types. Before
 * reveal() nothing redirects, nothing asks for the hosted page, no step is
 * reported and nothing can be confirmed; reveal() gives Stripe the e-mail.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { createPaymentController, type PaymentUiState } from './paymentController.js';
import { createPaymentSteps, type PaymentStepEvent } from './paymentSteps.js';
import type { CheckoutStatusResponse, PaymentBlock } from './checkout.js';
import { StripeUnavailableError } from './stripeElements.js';

const BLOCK: PaymentBlock = {
  provider: 'stripe',
  mode: 'elements',
  client_secret: 'cs_test_secret_prepared',
  publishable_key: 'pk_test_123',
};

const PENDING: CheckoutStatusResponse = {
  status: 'pending',
  checkout_token: 't',
  checkout_session_id: 's',
  items: [],
  tickets: [],
} as unknown as CheckoutStatusResponse;

function fakeElement() {
  const handlers: Record<string, (e: unknown) => void> = {};
  return {
    handlers,
    on: vi.fn((ev: string, fn: (e: unknown) => void) => {
      handlers[ev] = fn;
    }),
    mount: vi.fn(),
    destroy: vi.fn(),
    fire(ev: string, payload?: unknown) {
      handlers[ev]?.(payload);
    },
  };
}

interface SetupOpts {
  deferred?: boolean;
  openFails?: boolean;
  /** open() never resolves (Stripe hangs). */
  openHangs?: boolean;
  updateEmail?: (email: string) => Promise<unknown>;
  extra?: Partial<Parameters<typeof createPaymentController>[0]>;
}

function setup(o: SetupOpts = {}) {
  const order: string[] = [];
  const express = fakeElement();
  const payment = fakeElement();
  const checkoutHandlers: Record<string, (e: unknown) => void> = {};
  const confirm = vi.fn(async () => {
    order.push('confirm');
    return { type: 'success' as const };
  });
  const updateEmail = vi.fn(
    o.updateEmail ??
      (async (email: string) => {
        order.push(`updateEmail:${email}`);
        return { type: 'success' as const };
      }),
  );
  const checkout = {
    on: vi.fn((ev: string, fn: (e: unknown) => void) => {
      checkoutHandlers[ev] = fn;
    }),
    createExpressCheckoutElement: vi.fn(() => express),
    createPaymentElement: vi.fn(() => payment),
    destroy: vi.fn(),
    loadActions: vi.fn(),
  };
  const open = vi.fn(() => {
    if (o.openFails) return Promise.reject(new StripeUnavailableError('load'));
    if (o.openHangs) return new Promise<never>(() => {});
    return Promise.resolve({ checkout, actions: { confirm, updateEmail } });
  });
  const states: PaymentUiState[] = [];
  const events: PaymentStepEvent[] = [];
  const steps = createPaymentSteps();
  steps.subscribe((e) => events.push(e));
  const postHosted = vi.fn(async () => ({ redirect_url: 'https://checkout.stripe.com/c/pay/cs_x' }));
  const redirect = vi.fn();
  const onDeferredFailure = vi.fn();
  // A test clock: polling after a confirm ends quickly instead of spinning.
  let clock = 0;
  const ctrl = createPaymentController({
    block: BLOCK,
    locale: 'en',
    steps,
    getStatus: vi.fn(async () => PENDING),
    postHosted,
    redirect,
    onState: (s) => states.push(s),
    open: open as never,
    sleep: async (ms) => {
      clock += ms;
    },
    now: () => clock,
    pollIntervalMs: 1000,
    pollMaxMs: 3000,
    lateSettleMaxMs: 3000,
    recoverPollMaxMs: 3000,
    walletWaitMs: 20,
    deferred: o.deferred ?? true,
    onDeferredFailure,
    ...o.extra,
  });
  const last = () => states[states.length - 1]!;
  return { ctrl, express, payment, checkoutHandlers, confirm, updateEmail, open, postHosted, redirect, onDeferredFailure, events, last, order };
}

async function loaded(h: ReturnType<typeof setup>) {
  await h.ctrl.start({ express: 'E', payment: 'P' });
  h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: { applePay: false } });
  h.payment.fire('ready');
  h.payment.fire('change', { complete: true, value: { type: 'card' } });
  h.checkoutHandlers['change']?.({ canConfirm: true, total: { total: { amount: '€52.50' } } });
}

const flush = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => {
  vi.useRealTimers();
});

describe('deferred: failures before reveal', () => {
  it('a Stripe.js load failure marks the controller failed, never asks for the hosted page or redirects', async () => {
    const h = setup({ openFails: true });
    await h.ctrl.start({ express: 'E', payment: 'P' });
    expect(h.ctrl.hasFailed()).toBe(true);
    expect(h.onDeferredFailure).toHaveBeenCalledTimes(1);
    expect(h.postHosted).not.toHaveBeenCalled();
    expect(h.redirect).not.toHaveBeenCalled();
    expect(await h.ctrl.reveal('a@b.cz')).toBe(false);
    h.ctrl.destroy();
  });

  it('a load timeout marks it failed without the hosted page', async () => {
    vi.useFakeTimers();
    const h = setup({ openHangs: true, extra: { loadTimeoutMs: 100, initTimeoutMs: 1000 } });
    void h.ctrl.start({ express: 'E', payment: 'P' });
    await vi.advanceTimersByTimeAsync(150);
    expect(h.ctrl.hasFailed()).toBe(true);
    expect(h.onDeferredFailure).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2000);
    expect(h.onDeferredFailure).toHaveBeenCalledTimes(1);
    expect(h.postHosted).not.toHaveBeenCalled();
    expect(h.redirect).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('a ready timeout marks it failed without the hosted page', async () => {
    vi.useFakeTimers();
    const h = setup({ extra: { initTimeoutMs: 500 } });
    await h.ctrl.start({ express: 'E', payment: 'P' });
    await vi.advanceTimersByTimeAsync(600);
    expect(h.ctrl.hasFailed()).toBe(true);
    expect(h.postHosted).not.toHaveBeenCalled();
    expect(h.redirect).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('a loaderror of the hidden form marks it failed without the hosted page', async () => {
    const h = setup();
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.payment.fire('loaderror');
    await flush();
    expect(h.ctrl.hasFailed()).toBe(true);
    expect(h.onDeferredFailure).toHaveBeenCalledTimes(1);
    expect(h.postHosted).not.toHaveBeenCalled();
    expect(h.redirect).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });
});

describe('deferred: hidden until reveal', () => {
  const formViews = (h: ReturnType<typeof setup>) => h.events.filter((e) => e.step === 'checkout_form_view');

  it('reports checkout_form_view only after reveal, the e-mail taken, shown AND ready', async () => {
    const h = setup();
    await loaded(h);
    await flush();
    expect(h.events.map((e) => e.step)).toEqual([]);
    await h.ctrl.reveal('buyer@example.com');
    // updateEmail succeeded, but the screen is not visible yet.
    expect(formViews(h)).toHaveLength(0);
    h.ctrl.markShown();
    expect(formViews(h)).toHaveLength(1);
    h.ctrl.markShown();
    expect(formViews(h)).toHaveLength(1);
    h.ctrl.destroy();
  });

  it('shown before the e-mail is taken: the form view waits for updateEmail', async () => {
    let answer!: () => void;
    const h = setup({
      updateEmail: () => new Promise((r) => (answer = () => r({ type: 'success' }))),
    });
    await loaded(h);
    const revealing = h.ctrl.reveal('buyer@example.com');
    h.ctrl.markShown();
    await flush();
    expect(formViews(h)).toHaveLength(0);
    answer();
    await revealing;
    expect(formViews(h)).toHaveLength(1);
    h.ctrl.destroy();
  });

  it('a failing updateEmail never reports the form view', async () => {
    const h = setup({
      updateEmail: async () => ({ type: 'error', error: { type: 'invalid_request_error' } }),
    });
    await loaded(h);
    h.ctrl.markShown();
    await h.ctrl.reveal('buyer@example.com');
    await flush();
    expect(formViews(h)).toHaveLength(0);
    h.ctrl.destroy();
  });

  it('shown alone (no reveal) reports nothing', async () => {
    const h = setup();
    await loaded(h);
    h.ctrl.markShown();
    await flush();
    expect(formViews(h)).toHaveLength(0);
    h.ctrl.destroy();
  });

  it('revealed and shown before ready: the form view waits for ready', async () => {
    const h = setup();
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: { applePay: false } });
    await h.ctrl.reveal('buyer@example.com');
    h.ctrl.markShown();
    expect(formViews(h)).toHaveLength(0);
    h.payment.fire('ready');
    expect(formViews(h)).toHaveLength(1);
    h.ctrl.destroy();
  });

  it('nothing can be confirmed before reveal, by the button or by a wallet', async () => {
    const h = setup();
    await loaded(h);
    expect(h.last().canPay).toBe(false);
    await h.ctrl.submit();
    const resolve = vi.fn();
    h.express.fire('click', { expressPaymentType: 'apple_pay', resolve });
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    await flush();
    expect(h.confirm).not.toHaveBeenCalled();
    expect(resolve).not.toHaveBeenCalled();
    expect(paymentFailed).toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('reveal gives Stripe the e-mail before anything can be confirmed, then pays', async () => {
    const h = setup();
    await loaded(h);
    await h.ctrl.reveal('  buyer@example.com ');
    expect(h.updateEmail).toHaveBeenCalledWith('buyer@example.com');
    expect(h.last().canPay).toBe(true);
    await h.ctrl.submit();
    expect(h.order).toEqual(['updateEmail:buyer@example.com', 'confirm']);
    h.ctrl.destroy();
  });

  it('revealed before Stripe answered: the e-mail goes as soon as the actions exist', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const h = setup();
    const realOpen = h.open.getMockImplementation()!;
    h.open.mockImplementation(async () => {
      await gate;
      return realOpen() as never;
    });
    const started = h.ctrl.start({ express: 'E', payment: 'P' });
    expect(await h.ctrl.reveal('late@example.com')).toBe(true);
    expect(h.updateEmail).not.toHaveBeenCalled();
    release();
    await started;
    await flush();
    expect(h.updateEmail).toHaveBeenCalledWith('late@example.com');
    h.ctrl.destroy();
  });

  it('a failing updateEmail is handled like a non-decline error: the hosted page', async () => {
    const h = setup({
      updateEmail: async () => ({ type: 'error', error: { type: 'invalid_request_error' } }),
    });
    await loaded(h);
    await h.ctrl.reveal('buyer@example.com');
    await flush();
    expect(h.ctrl.hasFailed()).toBe(false);
    expect(h.postHosted).toHaveBeenCalledTimes(1);
    expect(h.redirect).toHaveBeenCalledWith('https://checkout.stripe.com/c/pay/cs_x');
    expect(h.confirm).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('a throwing updateEmail is handled the same way', async () => {
    const h = setup({
      updateEmail: async () => {
        throw new Error('network');
      },
    });
    await loaded(h);
    await h.ctrl.reveal('buyer@example.com');
    await flush();
    expect(h.postHosted).toHaveBeenCalledTimes(1);
    h.ctrl.destroy();
  });

  it('after reveal a failure is the classic hosted fallback again', async () => {
    const h = setup();
    await loaded(h);
    await h.ctrl.reveal('buyer@example.com');
    h.payment.fire('loaderror');
    await flush();
    expect(h.ctrl.hasFailed()).toBe(false);
    expect(h.postHosted).toHaveBeenCalledTimes(1);
    h.ctrl.destroy();
  });
});

describe('classic mode is unchanged', () => {
  it('never calls updateEmail and falls back to the hosted page on a load failure', async () => {
    const h = setup({ deferred: false, openFails: true });
    await h.ctrl.start({ express: 'E', payment: 'P' });
    expect(h.onDeferredFailure).not.toHaveBeenCalled();
    expect(h.ctrl.hasFailed()).toBe(false);
    expect(h.postHosted).toHaveBeenCalledTimes(1);
    expect(h.redirect).toHaveBeenCalled();
  });

  it('reports the form view on ready and confirms without reveal', async () => {
    const h = setup({ deferred: false });
    await loaded(h);
    expect(h.events.filter((e) => e.step === 'checkout_form_view')).toHaveLength(1);
    await h.ctrl.submit();
    expect(h.confirm).toHaveBeenCalledTimes(1);
    expect(h.updateEmail).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });
});
