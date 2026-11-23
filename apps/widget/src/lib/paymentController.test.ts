/**
 * The embedded payment flow end to end with a fake Stripe (spec 65 E4):
 * mount, confirm with redirect 'if_required', poll until paid, declines by
 * group with the same hold kept, and the hosted fallback.
 */

import { describe, it, expect, vi } from 'vitest';
import { createPaymentController, type PaymentUiState } from './paymentController.js';
import { createPaymentSteps, type PaymentStepEvent } from './paymentSteps.js';
import type { CheckoutStatusResponse, PaymentBlock } from './checkout.js';
import { StripeUnavailableError, openEmbeddedCheckout } from './stripeElements.js';

const SECRET = 'cs_test_secret_DO_NOT_LEAK';
const BLOCK: PaymentBlock = {
  provider: 'stripe',
  mode: 'elements',
  client_secret: SECRET,
  publishable_key: 'pk_test_123',
};

function status(s: CheckoutStatusResponse['status']): CheckoutStatusResponse {
  return { status: s, checkout_token: 't', checkout_session_id: 's', items: [], tickets: [] };
}

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

function setup(
  confirmResults: Array<unknown | Error>,
  statuses: Array<CheckoutStatusResponse | Error>,
  extra: Partial<Parameters<typeof createPaymentController>[0]> = {},
) {
  const express = fakeElement();
  const payment = fakeElement();
  const checkoutHandlers: Record<string, (e: unknown) => void> = {};
  const confirm = vi.fn(async (_opts?: Record<string, unknown>) => {
    const r = confirmResults.shift() ?? { type: 'success' };
    if (r instanceof Error) throw r;
    return r as { type: 'success' } | { type: 'error'; error: Record<string, string> };
  });
  const destroy = vi.fn();
  const checkout = {
    on: vi.fn((ev: string, fn: (e: unknown) => void) => {
      checkoutHandlers[ev] = fn;
    }),
    createExpressCheckoutElement: vi.fn(() => express),
    createPaymentElement: vi.fn(() => payment),
    destroy,
    loadActions: vi.fn(),
  };
  const open = vi.fn(async () => ({ checkout, actions: { confirm } }));
  const states: PaymentUiState[] = [];
  const events: PaymentStepEvent[] = [];
  const steps = createPaymentSteps();
  steps.subscribe((e) => events.push(e));
  const getStatus = vi.fn(async () => {
    const next = statuses.length > 1 ? statuses.shift()! : statuses[0]!;
    if (next instanceof Error) throw next;
    return next;
  });
  const postHosted = vi.fn(async () => ({ redirect_url: 'https://checkout.stripe.com/c/pay/cs_live_x' }));
  const redirect = vi.fn();
  let clock = 0;
  const ctrl = createPaymentController({
    block: BLOCK,
    locale: 'cs',
    steps,
    getStatus,
    postHosted,
    redirect,
    onState: (s) => states.push(s),
    open: open as never,
    sleep: async (ms) => {
      clock += ms;
    },
    now: () => clock,
    pollIntervalMs: 1000,
    pollMaxMs: 5000,
    initTimeoutMs: 60_000,
    walletWaitMs: 20,
    lateSettleMaxMs: 5000,
    ...extra,
  });
  const last = () => states[states.length - 1]!;
  return { ctrl, express, payment, checkoutHandlers, confirm, open, getStatus, postHosted, redirect, states, events, last, steps, destroy };
}

async function ready(h: ReturnType<typeof setup>) {
  await h.ctrl.start({ express: 'E', payment: 'P' });
  h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: { applePay: false } });
  h.payment.fire('ready');
  h.payment.fire('change', { complete: true });
  h.checkoutHandlers['change']?.({ canConfirm: true, total: { total: { amount: '€52.50' } } });
}

describe('mounting', () => {
  it('opens Stripe with the block, mounts wallets and card form, and reports the form shown once', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    expect(h.open).toHaveBeenCalledWith(
      expect.objectContaining({ publishableKey: 'pk_test_123', clientSecret: SECRET, locale: 'cs' }),
    );
    expect(h.express.mount).toHaveBeenCalledWith('E');
    expect(h.payment.mount).toHaveBeenCalledWith('P');
    expect(h.last().phase).toBe('ready');
    expect(h.last().canPay).toBe(true);
    h.payment.fire('ready');
    expect(h.events.filter((e) => e.step === 'checkout_form_view')).toHaveLength(1);
    h.ctrl.destroy();
  });

  it('knows whether a wallet is available from the express element', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: { applePay: true, googlePay: false } });
    expect(h.last().walletsAvailable).toBe(true);
    h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: null });
    expect(h.last().walletsAvailable).toBe(false);
    h.ctrl.destroy();
  });

  it('reports the chosen method from the card form and from a wallet click', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    h.payment.fire('change', { complete: false, value: { type: 'card' } });
    h.express.fire('click', { expressPaymentType: 'apple_pay', resolve: vi.fn() });
    h.express.fire('click', { expressPaymentType: 'apple_pay', resolve: vi.fn() });
    h.express.fire('click', { expressPaymentType: 'paypal', resolve: vi.fn() });
    const methods = h.events.filter((e) => e.step === 'payment_method_selected').map((e) => e.method);
    expect(methods).toEqual(['card', 'apple_pay', 'other']);
    h.ctrl.destroy();
  });
});

describe('paying', () => {
  it('confirms with redirect if_required, polls until the platform says paid, then reports success', async () => {
    const h = setup([{ type: 'success' }], [status('pending'), status('pending'), status('paid')]);
    await ready(h);
    await h.ctrl.submit();
    expect(h.confirm).toHaveBeenCalledWith({ redirect: 'if_required' });
    expect(h.getStatus).toHaveBeenCalledTimes(3);
    expect(h.last().phase).toBe('paid');
    expect(h.last().status?.status).toBe('paid');
    expect(h.events.map((e) => e.step)).toEqual([
      'checkout_form_view',
      'payment_details_complete',
      'pay_clicked',
      'payment_succeeded',
    ]);
    h.ctrl.destroy();
  });

  it('does not call a successful confirm "paid" before the platform says so', async () => {
    const h = setup([{ type: 'success' }], [status('pending')]);
    await ready(h);
    await h.ctrl.submit();
    // Never paid: the poll window ends in "still confirming", not in success.
    expect(h.last().phase).toBe('pending_slow');
    expect(h.events.some((e) => e.step === 'payment_succeeded')).toBe(false);
    h.ctrl.destroy();
  });

  it('tolerates a failing poll and keeps asking', async () => {
    const h = setup([{ type: 'success' }], [new Error('network'), status('paid')]);
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    h.ctrl.destroy();
  });

  it('hands a final non-paid state to the caller', async () => {
    const h = setup([{ type: 'success' }], [status('expired')]);
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('terminal');
    expect(h.last().status?.status).toBe('expired');
    h.ctrl.destroy();
  });

  it('confirms a wallet payment with the express event', async () => {
    const h = setup([{ type: 'success' }], [status('paid')]);
    await ready(h);
    const ev = { expressPaymentType: 'google_pay' };
    h.express.fire('confirm', ev);
    await vi.waitFor(() => expect(h.last().phase).toBe('paid'));
    expect(h.confirm).toHaveBeenCalledWith({ redirect: 'if_required', expressCheckoutConfirmEvent: ev });
    h.ctrl.destroy();
  });

  it('ignores a second press while one is running', async () => {
    const h = setup([{ type: 'success' }], [status('paid')]);
    await ready(h);
    await Promise.all([h.ctrl.submit(), h.ctrl.submit()]);
    expect(h.confirm).toHaveBeenCalledTimes(1);
    h.ctrl.destroy();
  });
});

describe('declines', () => {
  it.each([
    [{ type: 'card_error', code: 'card_declined', decline_code: 'insufficient_funds' }, 'no_funds'],
    [{ type: 'card_error', code: 'card_declined', decline_code: 'do_not_honor' }, 'bank_declined'],
    [{ type: 'card_error', code: 'expired_card' }, 'input_error'],
    [{ type: 'validation_error', code: 'incorrect_cvc' }, 'input_error'],
    [{ type: 'card_error', code: 'authentication_required' }, 'auth_failed'],
    [{ type: 'card_error', code: 'processing_error' }, 'other'],
    [{ code: 'expired_card' }, 'input_error'],
  ])('%j is the group %s', async (error, group) => {
    const h = setup([{ type: 'error', error }], [status('pending')]);
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('declined');
    expect(h.last().group).toBe(group);
    const failed = h.events.find((e) => e.step === 'payment_declined');
    expect(failed).toEqual({ step: 'payment_declined', group, attempt: '1' });
    h.ctrl.destroy();
  });

  it('keeps the same hold: no hosted fallback, a second try is possible and counted', async () => {
    const h = setup(
      [
        { type: 'error', error: { type: 'card_error', code: 'card_declined', decline_code: 'insufficient_funds' } },
        { type: 'error', error: { type: 'card_error', code: 'expired_card' } },
        { type: 'success' },
      ],
      [status('paid')],
    );
    await ready(h);
    await h.ctrl.submit();
    await h.ctrl.submit();
    await h.ctrl.submit();
    expect(h.postHosted).not.toHaveBeenCalled();
    const attempts = h.events.filter((e) => e.step === 'payment_declined').map((e) => e.attempt);
    expect(attempts).toEqual(['1', '2']);
    expect(h.last().phase).toBe('paid');
    h.ctrl.destroy();
  });

  it('is a decline only for a card or validation error, never for an integration, session or network one', async () => {
    const { isCardDecline } = await import('./paymentController.js');
    expect(isCardDecline({ type: 'card_error', code: 'card_declined' })).toBe(true);
    expect(isCardDecline({ type: 'validation_error', code: 'incomplete_number' })).toBe(true);
    expect(isCardDecline({ code: 'insufficient_funds' })).toBe(true);
    for (const type of ['invalid_request_error', 'api_error', 'api_connection_error', 'authentication_error', 'rate_limit_error']) {
      expect(isCardDecline({ type, code: 'resource_missing' }), type).toBe(false);
    }
    // A known decline code counts whatever the type (a 3-D Secure failure is
    // typed invalid_request_error).
    expect(isCardDecline({ type: 'invalid_request_error', code: 'payment_intent_authentication_failure' })).toBe(true);
    expect(isCardDecline({ code: 'something_unknown' })).toBe(false);
    expect(isCardDecline(undefined)).toBe(false);
  });
});

describe('hosted fallback', () => {
  function failingOpen() {
    return vi.fn(async () => {
      throw new StripeUnavailableError('timeout');
    });
  }

  it('redirects to the hosted page when Stripe does not open', async () => {
    const h = setup([], [status('pending')]);
    // Rebuild with a failing opener.
    const states: PaymentUiState[] = [];
    const redirect = vi.fn();
    const postHosted = vi.fn(async () => ({ redirect_url: 'https://checkout.stripe.com/c/pay/x' }));
    const ctrl = createPaymentController({
      block: BLOCK,
      locale: 'en',
      steps: createPaymentSteps(),
      getStatus: h.getStatus,
      postHosted,
      redirect,
      onState: (s) => states.push(s),
      open: failingOpen() as never,
    });
    await ctrl.start({ express: 'E', payment: 'P' });
    expect(postHosted).toHaveBeenCalledTimes(1);
    expect(redirect).toHaveBeenCalledWith('https://checkout.stripe.com/c/pay/x');
    expect(states.map((s) => s.phase)).toContain('falling_back');
  });

  it.each(['checkout.hosted_fallback_unavailable', 'checkout.not_payable'])(
    'a 409 %s means start again, with no redirect',
    async (code) => {
      const states: PaymentUiState[] = [];
      const redirect = vi.fn();
      const ctrl = createPaymentController({
        block: BLOCK,
        locale: 'en',
        steps: createPaymentSteps(),
        getStatus: vi.fn(async () => status('pending')),
        postHosted: vi.fn(async () => {
          throw Object.assign(new Error('409'), { status: 409, code });
        }),
        redirect,
        onState: (s) => states.push(s),
        open: failingOpen() as never,
      });
      await ctrl.start({ express: 'E', payment: 'P' });
      expect(redirect).not.toHaveBeenCalled();
      expect(states[states.length - 1]!.phase).toBe('unavailable');
    },
  );

  it('keeps polling when a payment is already running on the embedded session', async () => {
    const states: PaymentUiState[] = [];
    const ctrl = createPaymentController({
      block: BLOCK,
      locale: 'en',
      steps: createPaymentSteps(),
      getStatus: vi.fn(async () => status('paid')),
      postHosted: vi.fn(async () => {
        throw Object.assign(new Error('409'), { status: 409, code: 'checkout.payment_in_progress' });
      }),
      redirect: vi.fn(),
      onState: (s) => states.push(s),
      open: failingOpen() as never,
      sleep: async () => {},
    });
    await ctrl.start({ express: 'E', payment: 'P' });
    expect(states[states.length - 1]!.phase).toBe('paid');
  });

  it('falls back when the card form reports a load error', async () => {
    const h = setup([], [status('pending')]);
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.payment.fire('loaderror', { error: { message: 'blocked' } });
    await vi.waitFor(() => expect(h.redirect).toHaveBeenCalled());
    h.ctrl.destroy();
  });

  it('falls back when the card form never becomes ready (a blocked frame)', async () => {
    const states: PaymentUiState[] = [];
    const redirect = vi.fn();
    const checkout = {
      on: vi.fn(),
      createExpressCheckoutElement: () => fakeElement(),
      createPaymentElement: () => fakeElement(),
      destroy: vi.fn(),
    };
    const ctrl = createPaymentController({
      block: BLOCK,
      locale: 'en',
      steps: createPaymentSteps(),
      getStatus: vi.fn(async () => status('pending')),
      postHosted: vi.fn(async () => ({ redirect_url: 'https://h/x' })),
      redirect,
      onState: (s) => states.push(s),
      open: (async () => ({ checkout, actions: { confirm: vi.fn() } })) as never,
      initTimeoutMs: 15,
    });
    await ctrl.start({ express: 'E', payment: 'P' });
    await vi.waitFor(() => expect(redirect).toHaveBeenCalledWith('https://h/x'));
    ctrl.destroy();
  });
});

describe('returning from a bank page', () => {
  it('settles from the platform status without mounting a form', async () => {
    const h = setup([], [status('pending'), status('paid')]);
    await h.ctrl.awaitSettlement(10_000);
    expect(h.last().phase).toBe('paid');
    expect(h.open).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });
});

describe("Stripe's own total (confirm throws unless the page shows it)", () => {
  it('passes the total of the session change event on to the pay button state', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    expect(h.last().totalText).toBe('€52.50');
    h.checkoutHandlers['change']?.({ canConfirm: true, total: { total: { amount: '€47.25' } } });
    expect(h.last().totalText).toBe('€47.25');
    h.ctrl.destroy();
  });

  it('keeps the last known total when a later event carries none', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    h.checkoutHandlers['change']?.({ canConfirm: false });
    expect(h.last().totalText).toBe('€52.50');
    h.ctrl.destroy();
  });
});

describe('wallet click', () => {
  it('resolves the click at once, then reports the method and the press', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    const order: string[] = [];
    h.steps.subscribe((e) => order.push(e.step));
    const resolve = vi.fn(() => order.push('resolve'));
    h.express.fire('click', { expressPaymentType: 'google_pay', resolve });
    expect(resolve).toHaveBeenCalledTimes(1);
    expect(order).toEqual(['resolve', 'payment_method_selected', 'pay_clicked']);
    h.ctrl.destroy();
  });

  it('still reports the click when resolve throws or is missing', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    h.express.fire('click', {
      expressPaymentType: 'link',
      resolve: () => {
        throw new Error('x');
      },
    });
    h.express.fire('click', { expressPaymentType: 'apple_pay' });
    expect(h.events.filter((e) => e.step === 'pay_clicked')).toHaveLength(2);
    h.ctrl.destroy();
  });
});

describe('a wallet confirm that cannot run', () => {
  it('tells the wallet the payment failed when the form is not ready yet', async () => {
    const h = setup([], [status('pending')]);
    await h.ctrl.start({ express: 'E', payment: 'P' });
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    expect(paymentFailed).toHaveBeenCalledWith({ reason: 'fail' });
    expect(h.confirm).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('tells the wallet when a payment is already running', async () => {
    let finish!: (v: unknown) => void;
    const h = setup([], [status('pending')]);
    h.confirm.mockImplementationOnce(() => new Promise((r) => (finish = r)) as never);
    await ready(h);
    const first = h.ctrl.submit();
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    expect(paymentFailed).toHaveBeenCalledTimes(1);
    expect(h.confirm).toHaveBeenCalledTimes(1);
    finish({ type: 'error', error: { type: 'card_error', code: 'expired_card' } });
    await first;
    h.ctrl.destroy();
  });

  it('does not touch paymentFailed when it does confirm', async () => {
    const h = setup([{ type: 'success' }], [status('paid')]);
    await ready(h);
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    await vi.waitFor(() => expect(h.last().phase).toBe('paid'));
    expect(paymentFailed).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });
});

describe('errors that are not a decline', () => {
  it.each(['api_connection_error', 'invalid_request_error', 'api_error', 'authentication_error'])(
    'a %s goes to the hosted page, not to a decline message',
    async (type) => {
      const h = setup([{ type: 'error', error: { type, code: 'whatever' } }], [status('pending')]);
      await ready(h);
      await h.ctrl.submit();
      expect(h.postHosted).toHaveBeenCalledTimes(1);
      expect(h.redirect).toHaveBeenCalledWith('https://checkout.stripe.com/c/pay/cs_live_x');
      expect(h.events.some((e) => e.step === 'payment_declined')).toBe(false);
      expect(h.states.some((s) => s.phase === 'declined')).toBe(false);
      h.ctrl.destroy();
    },
  );

  it('a throwing confirm goes to the hosted page too', async () => {
    const h = setup([new Error('network')], [status('pending')]);
    await ready(h);
    await h.ctrl.submit();
    expect(h.redirect).toHaveBeenCalled();
    expect(h.events.some((e) => e.step === 'payment_declined')).toBe(false);
    h.ctrl.destroy();
  });

  it('after a confirm, a 409 from the hosted page returns the buyer to the same form, never "start again"', async () => {
    const h = setup([{ type: 'error', error: { type: 'api_error' } }], [status('pending')]);
    h.postHosted.mockRejectedValueOnce(
      Object.assign(new Error('409'), { status: 409, code: 'checkout.hosted_fallback_unavailable' }),
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.redirect).not.toHaveBeenCalled();
    expect(h.last().phase).toBe('declined');
    expect(h.last().group).toBe('other');
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    h.ctrl.destroy();
  });

  it('keeps polling when the hosted answer says a payment is already running', async () => {
    const h = setup([new Error('network')], [status('paid')]);
    h.postHosted.mockRejectedValueOnce(
      Object.assign(new Error('409'), { status: 409, code: 'checkout.payment_in_progress' }),
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    h.ctrl.destroy();
  });
});

describe('one deadline from start()', () => {
  it('gives the opener no budget of its own', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    expect(h.open).toHaveBeenCalledWith(expect.objectContaining({ timeoutMs: Number.POSITIVE_INFINITY }));
    h.ctrl.destroy();
  });

  it('uses the hosted page when load/init has not finished by the deadline, and destroys a late checkout', async () => {
    const destroy = vi.fn();
    let release!: (v: unknown) => void;
    const h = setup([], [status('pending')], {
      initTimeoutMs: 20,
      open: (() => new Promise((r) => (release = r))) as never,
    });
    const started = h.ctrl.start({ express: 'E', payment: 'P' });
    await vi.waitFor(() => expect(h.redirect).toHaveBeenCalledTimes(1));
    release({ checkout: { destroy }, actions: {} });
    await started;
    expect(destroy).toHaveBeenCalled();
    expect(h.redirect).toHaveBeenCalledTimes(1);
  });

  it('counts the card form becoming ready against the same budget, and stops counting once it is', async () => {
    const quick = setup([], [status('pending')], { initTimeoutMs: 30 });
    await quick.ctrl.start({ express: 'E', payment: 'P' });
    quick.payment.fire('ready');
    await new Promise((r) => setTimeout(r, 80));
    expect(quick.postHosted).not.toHaveBeenCalled();
    quick.ctrl.destroy();

    const slow = setup([], [status('pending')], { initTimeoutMs: 30 });
    await slow.ctrl.start({ express: 'E', payment: 'P' });
    await vi.waitFor(() => expect(slow.redirect).toHaveBeenCalled());
    slow.ctrl.destroy();
  });
});

describe('two deadlines: script load 5 s, form ready 15 s', () => {
  function realOpen(h: { checkout: unknown }, loadStripe: () => Promise<unknown>) {
    return {
      open: openEmbeddedCheckout as never,
      loadStripe: loadStripe as never,
      _h: h,
    };
  }
  const factoryFor = (checkout: unknown) =>
    (() => ({ initCheckoutElementsSdk: () => checkout })) as never;

  it('falls back when Stripe.js has not loaded in 5 s', async () => {
    vi.useFakeTimers();
    try {
      const h = setup([], [status('pending')], {
        initTimeoutMs: undefined,
        ...realOpen({ checkout: null }, () => new Promise(() => {})),
      });
      void h.ctrl.start({ express: 'E', payment: 'P' });
      await vi.advanceTimersByTimeAsync(4900);
      expect(h.postHosted).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(200);
      expect(h.postHosted).toHaveBeenCalledTimes(1);
      expect(h.redirect).toHaveBeenCalledTimes(1);
      h.ctrl.destroy();
    } finally {
      vi.useRealTimers();
    }
  });

  it('falls back at once when the script fails to load', async () => {
    vi.useFakeTimers();
    try {
      const h = setup([], [status('pending')], {
        initTimeoutMs: undefined,
        ...realOpen({ checkout: null }, async () => {
          throw new StripeUnavailableError('load');
        }),
      });
      void h.ctrl.start({ express: 'E', payment: 'P' });
      await vi.advanceTimersByTimeAsync(0);
      expect(h.redirect).toHaveBeenCalledTimes(1);
      h.ctrl.destroy();
    } finally {
      vi.useRealTimers();
    }
  });

  it('does NOT fall back when the script loaded and loadActions answers at 6 s: the form gets ready', async () => {
    vi.useFakeTimers();
    try {
      const express = fakeElement();
      const payment = fakeElement();
      const checkout = {
        on: vi.fn(),
        createExpressCheckoutElement: () => express,
        createPaymentElement: () => payment,
        destroy: vi.fn(),
        loadActions: () =>
          new Promise((r) => setTimeout(() => r({ type: 'success', actions: { confirm: vi.fn() } }), 6000)),
      };
      const h = setup([], [status('pending')], {
        initTimeoutMs: undefined,
        ...realOpen({ checkout }, async () => factoryFor(checkout)),
      });
      void h.ctrl.start({ express: 'E', payment: 'P' });
      await vi.advanceTimersByTimeAsync(6100);
      expect(h.postHosted).not.toHaveBeenCalled();
      expect(h.last().phase).toBe('loading');
      payment.fire('ready');
      expect(h.last().phase).toBe('ready');
      await vi.advanceTimersByTimeAsync(20_000);
      expect(h.postHosted).not.toHaveBeenCalled();
      h.ctrl.destroy();
    } finally {
      vi.useRealTimers();
    }
  });

  it('falls back at 15 s when the form never becomes ready', async () => {
    vi.useFakeTimers();
    try {
      const checkout = {
        on: vi.fn(),
        createExpressCheckoutElement: () => fakeElement(),
        createPaymentElement: () => fakeElement(),
        destroy: vi.fn(),
        loadActions: async () => ({ type: 'success', actions: { confirm: vi.fn() } }),
      };
      const h = setup([], [status('pending')], {
        initTimeoutMs: undefined,
        ...realOpen({ checkout }, async () => factoryFor(checkout)),
      });
      void h.ctrl.start({ express: 'E', payment: 'P' });
      await vi.advanceTimersByTimeAsync(14_900);
      expect(h.postHosted).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(200);
      expect(h.postHosted).toHaveBeenCalledTimes(1);
      h.ctrl.destroy();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('the form view waits for the wallet answer', () => {
  it('reports it once both are known, with the wallets field', async () => {
    const h = setup([], [status('pending')]);
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.payment.fire('ready');
    expect(h.events.some((e) => e.step === 'checkout_form_view')).toBe(false);
    h.express.fire('availablepaymentmethodschange', { availablePaymentMethods: { applePay: true } });
    expect(h.events.filter((e) => e.step === 'checkout_form_view')).toEqual([
      { step: 'checkout_form_view', wallets_available: true },
    ]);
    h.ctrl.destroy();
  });

  it('reports it without the wallets field when the wallet event never comes', async () => {
    const h = setup([], [status('pending')], { walletWaitMs: 10 });
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.payment.fire('ready');
    await vi.waitFor(() => expect(h.events.some((e) => e.step === 'checkout_form_view')).toBe(true));
    expect(h.events.find((e) => e.step === 'checkout_form_view')).toEqual({ step: 'checkout_form_view' });
    h.ctrl.destroy();
  });

  it('reports the completed details when the Payment Element says complete, once, and not on canConfirm', async () => {
    const h = setup([], [status('pending')]);
    await h.ctrl.start({ express: 'E', payment: 'P' });
    h.checkoutHandlers['change']?.({ canConfirm: true });
    expect(h.events.some((e) => e.step === 'payment_details_complete')).toBe(false);
    h.payment.fire('change', { complete: false });
    expect(h.events.some((e) => e.step === 'payment_details_complete')).toBe(false);
    h.payment.fire('change', { complete: true });
    h.payment.fire('change', { complete: true });
    expect(h.events.filter((e) => e.step === 'payment_details_complete')).toHaveLength(1);
    h.ctrl.destroy();
  });
});

describe('after Stripe accepted the payment the buyer never starts over', () => {
  it('keeps asking past the first minute, backing off, and ends paid', async () => {
    const pendings = Array.from({ length: 12 }, () => status('pending'));
    const waits: number[] = [];
    let clock = 0;
    const h = setup([{ type: 'success' }], [...pendings, status('paid')], {
      pollIntervalMs: 1000,
      pollMaxMs: 5000,
      slowPollIntervalMs: 4000,
      lateSettleMaxMs: 120_000,
      sleep: async (ms) => {
        waits.push(ms);
        clock += ms;
      },
      now: () => clock,
    });
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    expect(h.states.some((s) => s.phase === 'pending_slow')).toBe(false);
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    // 1 s while inside the fast window, 4 s after it.
    expect(waits.slice(0, 5)).toEqual([1000, 1000, 1000, 1000, 1000]);
    expect(waits.slice(5).every((w) => w === 4000)).toBe(true);
    h.ctrl.destroy();
  });

  it('marks the payment received for the whole wait, and ends in "still confirming" only when the long window is over', async () => {
    let clock = 0;
    const h = setup([{ type: 'success' }], [status('pending')], {
      pollIntervalMs: 1000,
      pollMaxMs: 3000,
      slowPollIntervalMs: 2000,
      lateSettleMaxMs: 9000,
      sleep: async (ms) => {
        clock += ms;
      },
      now: () => clock,
    });
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('pending_slow');
    expect(h.last().paymentReceived).toBe(true);
    const afterConfirm = h.states.slice(h.states.findIndex((s) => s.phase === 'submitting'));
    expect(afterConfirm.filter((s) => s.phase === 'confirming').every((s) => s.paymentReceived === true)).toBe(true);
    h.ctrl.destroy();
  });

  it('a payment_in_progress answer from /hosted is "going through": polls, no error, no redirect', async () => {
    const h = setup([{ type: 'error', error: { type: 'api_error' } }], [status('pending'), status('paid')]);
    h.postHosted.mockRejectedValueOnce(
      Object.assign(new Error('409'), { status: 409, code: 'checkout.payment_in_progress' }),
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    expect(h.last().paymentReceived).toBe(true);
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    expect(h.redirect).not.toHaveBeenCalled();
    h.ctrl.destroy();
  });

  it('when confirm throws and /hosted says payment_in_progress it waits, it does not offer a new checkout', async () => {
    const h = setup([new Error('network')], [status('pending'), status('paid')]);
    h.postHosted.mockRejectedValueOnce(
      Object.assign(new Error('409'), { status: 409, code: 'checkout.payment_in_progress' }),
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    expect(h.states.some((s) => s.phase === 'unavailable' || s.phase === 'declined')).toBe(false);
    h.ctrl.destroy();
  });

  it('a not_payable answer still checks the platform once: a paid order is paid, not "start again"', async () => {
    const h = setup([new Error('network')], [status('paid')]);
    h.postHosted.mockRejectedValueOnce(
      Object.assign(new Error('409'), { status: 409, code: 'checkout.not_payable' }),
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    h.ctrl.destroy();
  });
});

describe('a wallet payment that fails', () => {
  it('tells the wallet sheet when confirm throws', async () => {
    const h = setup([new Error('network')], [status('pending')]);
    await ready(h);
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    await vi.waitFor(() => expect(paymentFailed).toHaveBeenCalledWith({ reason: 'fail' }));
    h.ctrl.destroy();
  });

  it('tells the wallet sheet when confirm answers a decline, and still shows the decline', async () => {
    const h = setup([{ type: 'error', error: { type: 'card_error', code: 'expired_card' } }], [status('pending')]);
    await ready(h);
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    await vi.waitFor(() => expect(h.last().phase).toBe('declined'));
    expect(paymentFailed).toHaveBeenCalledTimes(1);
    h.ctrl.destroy();
  });

  it('tells the wallet sheet when confirm answers a non-decline error', async () => {
    const h = setup([{ type: 'error', error: { type: 'api_error' } }], [status('pending')]);
    await ready(h);
    const paymentFailed = vi.fn();
    h.express.fire('confirm', { paymentFailed });
    await vi.waitFor(() => expect(paymentFailed).toHaveBeenCalledTimes(1));
    h.ctrl.destroy();
  });
});

describe('the client secret', () => {
  it('reaches Stripe only: not the UI state, not the step events, not the status calls', async () => {
    const h = setup([{ type: 'success' }], [status('paid')]);
    await ready(h);
    await h.ctrl.submit();
    expect(JSON.stringify(h.states)).not.toContain(SECRET);
    expect(JSON.stringify(h.events)).not.toContain(SECRET);
    expect(JSON.stringify(h.getStatus.mock.calls)).not.toContain(SECRET);
    expect(JSON.stringify(h.postHosted.mock.calls)).not.toContain(SECRET);
    h.ctrl.destroy();
  });

  it('destroys the elements and the checkout on teardown', async () => {
    const h = setup([], [status('pending')]);
    await ready(h);
    h.ctrl.destroy();
    expect(h.express.destroy).toHaveBeenCalled();
    expect(h.payment.destroy).toHaveBeenCalled();
    expect(h.destroy).toHaveBeenCalled();
  });
});

describe('a confirm was attempted: never "start again" (no second payment)', () => {
  const conflict = () =>
    Object.assign(new Error('409'), { status: 409, code: 'checkout.hosted_fallback_unavailable' });
  const down = () => Object.assign(new Error('503'), { status: 503, code: 'checkout.payment_start_failed' });

  it('confirm throws, hosted 409, status stays pending: polls a while, then back on the same form', async () => {
    const h = setup([new Error('network')], [status('pending')]);
    h.postHosted.mockRejectedValueOnce(conflict());
    await ready(h);
    await h.ctrl.submit();
    expect(h.getStatus.mock.calls.length).toBeGreaterThan(1);
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    expect(h.last().phase).toBe('declined');
    expect(h.last().group).toBe('other');
    expect(h.last().canPay).toBe(true);
    expect(h.open).toHaveBeenCalledTimes(1);
    // The same form can be confirmed again.
    await h.ctrl.submit();
    expect(h.confirm).toHaveBeenCalledTimes(2);
    h.ctrl.destroy();
  });

  it('confirm throws, hosted 503, status stays pending: back on the same form', async () => {
    const h = setup([new Error('network')], [status('pending')]);
    h.postHosted.mockRejectedValueOnce(down());
    await ready(h);
    await h.ctrl.submit();
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    expect(h.last().phase).toBe('declined');
    expect(h.last().group).toBe('other');
    h.ctrl.destroy();
  });

  it('confirm throws, hosted 409, the status becomes paid while polling: paid', async () => {
    const h = setup([new Error('network')], [status('pending'), status('pending'), status('paid')]);
    h.postHosted.mockRejectedValueOnce(conflict());
    await ready(h);
    await h.ctrl.submit();
    expect(h.last().phase).toBe('paid');
    expect(h.states.some((s) => s.phase === 'unavailable')).toBe(false);
    expect(h.events.some((e) => e.step === 'payment_succeeded')).toBe(true);
    h.ctrl.destroy();
  });

  it('an init failure with no confirm still offers "start again"', async () => {
    const h = setup([], [status('pending')]);
    h.open.mockRejectedValueOnce(new Error('stripe did not load'));
    h.postHosted.mockRejectedValueOnce(conflict());
    await h.ctrl.start({ express: 'E', payment: 'P' });
    expect(h.confirm).not.toHaveBeenCalled();
    expect(h.last().phase).toBe('unavailable');
    h.ctrl.destroy();
  });

  it('a 3-D Secure failure stays on the form as auth_failed, with no hosted fallback', async () => {
    const h = setup(
      [{ type: 'error', error: { type: 'invalid_request_error', code: 'payment_intent_authentication_failure' } }],
      [status('pending')],
    );
    await ready(h);
    await h.ctrl.submit();
    expect(h.postHosted).not.toHaveBeenCalled();
    expect(h.last().phase).toBe('declined');
    expect(h.last().group).toBe('auth_failed');
    h.ctrl.destroy();
  });
});
