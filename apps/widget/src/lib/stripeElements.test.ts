import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  STRIPE_FORM_READY_TIMEOUT_MS,
  STRIPE_JS_LOAD_TIMEOUT_MS,
  STRIPE_JS_URL,
  StripeUnavailableError,
  openEmbeddedCheckout,
  resetStripeLoaderForTests,
  stripeLocale,
  type StripeFactory,
} from './stripeElements.js';

afterEach(() => {
  vi.useRealTimers();
  resetStripeLoaderForTests();
});

describe('Stripe.js source and budget', () => {
  it('loads the versioned script from js.stripe.com only, matching the pinned dahlia API', () => {
    expect(STRIPE_JS_URL).toBe('https://js.stripe.com/dahlia/stripe.js');
    expect(new URL(STRIPE_JS_URL).host).toBe('js.stripe.com');
  });

  it('gives the script five seconds to load and the whole form fifteen to be ready', () => {
    expect(STRIPE_JS_LOAD_TIMEOUT_MS).toBe(5000);
    expect(STRIPE_FORM_READY_TIMEOUT_MS).toBe(15000);
  });
});

describe('stripeLocale', () => {
  it.each([
    ['en', 'en'],
    ['ru', 'ru'],
    ['cs', 'cs'],
    ['he', 'he'],
    ['he-IL', 'he'],
    ['es_ES', 'es'],
    ['de', 'auto'],
    ['', 'auto'],
  ])('%s -> %s', (input, out) => {
    expect(stripeLocale(input)).toBe(out);
  });
});

function factoryFor(checkout: unknown, seen: { key?: string; opts?: unknown; init?: unknown } = {}): StripeFactory {
  return ((key: string, opts?: unknown) => {
    seen.key = key;
    seen.opts = opts;
    return {
      initCheckoutElementsSdk: (o: unknown) => {
        seen.init = o;
        return checkout;
      },
    };
  }) as unknown as StripeFactory;
}

describe('openEmbeddedCheckout', () => {
  it('uses initCheckoutElementsSdk with the secret, the Stripe theme and the widget locale', async () => {
    const seen: { key?: string; opts?: unknown; init?: any } = {};
    const actions = { confirm: vi.fn() };
    const checkout = { loadActions: vi.fn(async () => ({ type: 'success', actions })) };
    const out = await openEmbeddedCheckout({
      publishableKey: 'pk_test_1',
      clientSecret: 'cs_secret',
      locale: 'he',
      loadStripe: async () => factoryFor(checkout, seen),
    });
    expect(out.actions).toBe(actions);
    expect(seen.key).toBe('pk_test_1');
    expect(seen.opts).toEqual({ locale: 'he' });
    expect(seen.init.clientSecret).toBe('cs_secret');
    expect(seen.init.elementsOptions.appearance).toEqual({ theme: 'stripe' });
  });

  it('rejects when the script cannot load', async () => {
    await expect(
      openEmbeddedCheckout({
        publishableKey: 'pk',
        clientSecret: 'cs',
        locale: 'en',
        loadStripe: async () => {
          throw new Error('CSP');
        },
      }),
    ).rejects.toMatchObject({ name: 'StripeUnavailableError', reason: 'load' });
  });

  it('rejects when init throws, and never puts the secret into the error', async () => {
    const err = await openEmbeddedCheckout({
      publishableKey: 'pk',
      clientSecret: 'cs_SECRET_VALUE',
      locale: 'en',
      loadStripe: async () =>
        (() => {
          throw new Error('bad key cs_SECRET_VALUE');
        }) as unknown as StripeFactory,
    }).catch((e: unknown) => e as StripeUnavailableError);
    expect(err).toBeInstanceOf(StripeUnavailableError);
    expect(err.reason).toBe('init');
    expect(String(err.message)).not.toContain('cs_SECRET_VALUE');
  });

  it('rejects when loadActions answers an error', async () => {
    const checkout = { loadActions: vi.fn(async () => ({ type: 'error', error: { message: 'x' } })) };
    await expect(
      openEmbeddedCheckout({
        publishableKey: 'pk',
        clientSecret: 'cs',
        locale: 'en',
        loadStripe: async () => factoryFor(checkout),
      }),
    ).rejects.toMatchObject({ reason: 'init' });
  });

  it('gives up after the budget and destroys a checkout that arrives late', async () => {
    vi.useFakeTimers();
    let release!: (v: unknown) => void;
    const destroy = vi.fn();
    const checkout = {
      destroy,
      loadActions: vi.fn(
        () => new Promise((r) => (release = r)),
      ),
    };
    const p = openEmbeddedCheckout({
      publishableKey: 'pk',
      clientSecret: 'cs',
      locale: 'en',
      loadStripe: async () => factoryFor(checkout),
    });
    const settled = p.catch((e: unknown) => e as StripeUnavailableError);
    await vi.advanceTimersByTimeAsync(STRIPE_FORM_READY_TIMEOUT_MS + 1);
    const err = await settled;
    expect(err).toBeInstanceOf(StripeUnavailableError);
    expect(err.reason).toBe('timeout');
    release({ type: 'success', actions: {} });
    await vi.advanceTimersByTimeAsync(0);
    expect(destroy).toHaveBeenCalled();
  });

  it('does not time out when it finishes inside the budget', async () => {
    vi.useFakeTimers();
    const checkout = { loadActions: vi.fn(async () => ({ type: 'success', actions: {} })) };
    const p = openEmbeddedCheckout({
      publishableKey: 'pk',
      clientSecret: 'cs',
      locale: 'en',
      loadStripe: async () => factoryFor(checkout),
    });
    await vi.advanceTimersByTimeAsync(10);
    await expect(p).resolves.toBeTruthy();
  });

  it('creates one Stripe instance per key and locale, however many checkouts open', async () => {
    const created: string[] = [];
    const factory = ((key: string, o?: { locale?: string }) => {
      created.push(`${key}|${o?.locale}`);
      return {
        initCheckoutElementsSdk: () => ({
          loadActions: async () => ({ type: 'success', actions: {} }),
        }),
      };
    }) as unknown as StripeFactory;
    const base = { clientSecret: 'cs', loadStripe: async () => factory };
    await openEmbeddedCheckout({ ...base, publishableKey: 'pk_a', locale: 'en' });
    await openEmbeddedCheckout({ ...base, publishableKey: 'pk_a', locale: 'en' });
    await openEmbeddedCheckout({ ...base, publishableKey: 'pk_b', locale: 'en' });
    expect(created).toEqual(['pk_a|en', 'pk_b|en']);
  });

  it('runs no timer of its own when the caller passes an infinite budget', async () => {
    vi.useFakeTimers();
    const checkout = { loadActions: vi.fn(async () => ({ type: 'success', actions: {} })) };
    const p = openEmbeddedCheckout({
      publishableKey: 'pk',
      clientSecret: 'cs',
      locale: 'en',
      timeoutMs: Number.POSITIVE_INFINITY,
      loadStripe: async () => factoryFor(checkout),
    });
    await expect(p).resolves.toBeTruthy();
    expect(vi.getTimerCount()).toBe(0);
  });
});
