/**
 * stripeElements.ts — the thin, testable edge to Stripe.js for the embedded
 * payment screen (spec 65 §3, §7).
 *
 * Rules kept here:
 *  - Stripe.js is loaded ONLY from js.stripe.com, as the versioned script that
 *    matches the pinned API version (2026-03-25.dahlia), never from npm and
 *    never from a host-supplied address.
 *  - Loading + init + `loadActions()` share ONE budget of 5 seconds. Past it
 *    (or on a CSP refusal, a network error, a throwing init) the caller falls
 *    back to the hosted page.
 *  - The client secret is passed to Stripe and nowhere else: not logged, not
 *    stored, not put into an error message.
 *
 * Everything Stripe-shaped is typed loosely on purpose: only the handful of
 * calls the widget makes are named, so the widget carries no Stripe package.
 */

/** Versioned Stripe.js matching API version 2026-03-25.dahlia. */
export const STRIPE_JS_URL = 'https://js.stripe.com/dahlia/stripe.js';

/** Load + init budget before the hosted fallback (spec 65 §3.5). */
export const STRIPE_INIT_TIMEOUT_MS = 5000;

/** Locales Stripe.js knows among the widget's: everything else is `auto`. */
const STRIPE_LOCALES = new Set(['en', 'ru', 'cs', 'he', 'es']);

/** The widget's locale as the Stripe `locale` option ("he-IL" -> "he"). */
export function stripeLocale(locale: string): string {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return STRIPE_LOCALES.has(lang) ? lang : 'auto';
}

// ─── Loose Stripe shapes ──────────────────────────────────────────────────────

export interface StripeErrorLike {
  type?: string;
  code?: string;
  decline_code?: string;
  declineCode?: string;
  message?: string;
}

export type ConfirmResultLike =
  | { type: 'success'; success?: unknown }
  | { type: 'error'; error: StripeErrorLike };

export interface ElementLike {
  mount(node: unknown): void;
  on(event: string, handler: (e: any) => void): void;
  destroy?(): void;
}

export interface CheckoutActionsLike {
  confirm(options?: Record<string, unknown>): Promise<ConfirmResultLike>;
}

export interface CheckoutLike {
  on(
    event: 'change',
    handler: (session: {
      canConfirm?: boolean;
      /** `total.total.amount` is the formatted total ("€52.50") the page must show. */
      total?: { total?: { amount?: string; minorUnitsAmount?: number } };
      currency?: string;
    }) => void,
  ): void;
  loadActions(): Promise<{ type: 'success'; actions: CheckoutActionsLike } | { type: 'error'; error: StripeErrorLike }>;
  createPaymentElement(options?: Record<string, unknown>): ElementLike;
  createExpressCheckoutElement(options?: Record<string, unknown>): ElementLike;
  destroy?(): void;
}

export interface StripeLike {
  initCheckoutElementsSdk(options: Record<string, unknown>): CheckoutLike;
}

export type StripeFactory = (publishableKey: string, options?: Record<string, unknown>) => StripeLike;

/** Stripe.js did not load, init failed, or the 5 s budget ran out. */
export class StripeUnavailableError extends Error {
  readonly reason: 'timeout' | 'load' | 'init';
  constructor(reason: 'timeout' | 'load' | 'init') {
    // The message never carries Stripe's own text: it may echo request data.
    super(`stripe unavailable: ${reason}`);
    this.name = 'StripeUnavailableError';
    this.reason = reason;
  }
}

// ─── Script loading ───────────────────────────────────────────────────────────

let scriptPromise: Promise<StripeFactory> | null = null;

/**
 * One Stripe instance per publishable key and locale: Stripe warns when a page
 * creates several for the same account, and a retry or a second widget must
 * not pile up more.
 */
const instances = new Map<string, StripeLike>();

/** Test hook: forgets the cached load and instances so the next call starts over. */
export function resetStripeLoaderForTests(): void {
  scriptPromise = null;
  instances.clear();
}

function stripeFor(factory: StripeFactory, publishableKey: string, locale: string): StripeLike {
  const key = `${publishableKey}|${locale}`;
  let s = instances.get(key);
  if (!s) {
    s = factory(publishableKey, { locale });
    instances.set(key, s);
  }
  return s;
}

/**
 * Loads Stripe.js once per page and resolves with the `Stripe` factory. A
 * failure is not cached, so a later attempt (the buyer pressing "pay" again)
 * can try again.
 */
export function loadStripeJs(): Promise<StripeFactory> {
  if (scriptPromise) return scriptPromise;
  const p = new Promise<StripeFactory>((resolve, reject) => {
    if (typeof document === 'undefined' || typeof window === 'undefined') {
      reject(new StripeUnavailableError('load'));
      return;
    }
    const existing = (window as unknown as { Stripe?: StripeFactory }).Stripe;
    if (typeof existing === 'function') {
      resolve(existing);
      return;
    }
    const script = document.createElement('script');
    script.src = STRIPE_JS_URL;
    script.async = true;
    script.onload = () => {
      const factory = (window as unknown as { Stripe?: StripeFactory }).Stripe;
      if (typeof factory === 'function') resolve(factory);
      else reject(new StripeUnavailableError('load'));
    };
    // A CSP refusal and a network failure both land here.
    script.onerror = () => reject(new StripeUnavailableError('load'));
    document.head.appendChild(script);
  });
  scriptPromise = p;
  // Do not keep a failure: the next attempt must be able to retry.
  p.catch(() => {
    if (scriptPromise === p) scriptPromise = null;
  });
  return p;
}

// ─── Opening a checkout ───────────────────────────────────────────────────────

export interface OpenedCheckout {
  checkout: CheckoutLike;
  actions: CheckoutActionsLike;
}

export interface OpenOptions {
  publishableKey: string;
  clientSecret: string;
  locale: string;
  /** Replaceable in tests. */
  loadStripe?: () => Promise<StripeFactory>;
  timeoutMs?: number;
}

/**
 * Loads Stripe.js, starts the Checkout Elements SDK and waits for its
 * actions, all inside one time budget. Rejects with StripeUnavailableError.
 */
export function openEmbeddedCheckout(opts: OpenOptions): Promise<OpenedCheckout> {
  const load = opts.loadStripe ?? loadStripeJs;
  const budget = opts.timeoutMs ?? STRIPE_INIT_TIMEOUT_MS;

  const work = (async (): Promise<OpenedCheckout> => {
    let factory: StripeFactory;
    try {
      factory = await load();
    } catch {
      throw new StripeUnavailableError('load');
    }
    try {
      const stripe = stripeFor(factory, opts.publishableKey, stripeLocale(opts.locale));
      const checkout = stripe.initCheckoutElementsSdk({
        clientSecret: opts.clientSecret,
        // Stripe's own look, no widget colours or fonts (owner decision).
        elementsOptions: { appearance: { theme: 'stripe' } },
      });
      const loaded = await checkout.loadActions();
      if (loaded.type !== 'success') throw new Error('loadActions failed');
      return { checkout, actions: loaded.actions };
    } catch {
      throw new StripeUnavailableError('init');
    }
  })();

  // A caller that runs its own single deadline passes Infinity: no second timer.
  if (!Number.isFinite(budget)) return work;

  return new Promise<OpenedCheckout>((resolve, reject) => {
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      reject(new StripeUnavailableError('timeout'));
    }, budget);
    work.then(
      (v) => {
        clearTimeout(timer);
        if (timedOut) {
          // Too late: the caller already fell back to the hosted page.
          try {
            v.checkout.destroy?.();
          } catch {
            /* ignore */
          }
          return;
        }
        resolve(v);
      },
      (e) => {
        clearTimeout(timer);
        if (!timedOut) reject(e);
      },
    );
  });
}
