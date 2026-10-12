/**
 * promoController.ts — the promo code of the cart sheet as a small state
 * machine, free of Svelte so the races can be tested without a DOM.
 *
 * The rules it enforces:
 *  - ONE request is current. Every start (apply, refresh, remove) invalidates
 *    the one before it, so an old answer can never overwrite a newer state.
 *  - A quote is accepted only for the cart it was asked about. If the cart
 *    changed while the answer was on its way, the answer is dropped and the new
 *    cart is quoted again (bounded), so the sheet never shows a discount that
 *    belongs to another cart.
 *  - A quote that cannot be had (network, server, a hung request) never blocks
 *    the purchase: the wait is bounded, an applied code is taken off with the
 *    reason, and the buyer can pay the list price or try again.
 *  - A code is "payable" only while its quote is for the current cart and no
 *    request is in flight — the sheet's pay button follows `promoPayBlocked`.
 */

import type { CheckoutQuotePayload, CheckoutQuoteResponse } from './checkout.js';
import { promoErrorMessage, toPromoQuote } from './promo.js';
import type { PromoQuote } from './promo.js';
import type { PromoStrings } from './promoI18n.js';

/** How long one quote may take before the check is given up. */
export const QUOTE_TIMEOUT_MS = 8000;
/** How many times a quote is re-asked because the cart moved under it. */
export const MAX_QUOTE_ATTEMPTS = 3;

export interface PromoState {
  /** The applied code as typed; null when none. */
  code: string | null;
  /** The platform's quote of one exact cart for `code`. */
  quote: PromoQuote | null;
  /** A request is in flight. */
  busy: boolean;
  /** Why the last attempt failed, in the buyer's language. */
  error: string | null;
  /** A neutral status line ("Code removed."). */
  note: string | null;
}

export function initialPromoState(): PromoState {
  return { code: null, quote: null, busy: false, error: null, note: null };
}

export interface PromoControllerDeps {
  /** Identity of the cart as it is NOW (`cartQuoteKey`); '' when there is no cart. */
  getCartKey: () => string;
  /** The quote payload for the cart as it is NOW, or null when nothing can be quoted. */
  buildPayload: (code: string) => CheckoutQuotePayload | null;
  /** The network call. `signal` aborts it when the wait is given up. */
  quote: (payload: CheckoutQuotePayload, signal: AbortSignal) => Promise<CheckoutQuoteResponse>;
  strings: () => PromoStrings;
  /** The notice for a 409 seat conflict, or null when `err` is not one. */
  conflictText: (err: unknown) => string | null;
  /** The platform's error code of a failed call, if it has one. */
  errorCode: (err: unknown) => string | undefined;
  /** Called with the new state after every change. */
  onChange: (state: PromoState) => void;
  timeoutMs?: number;
  maxAttempts?: number;
}

export interface PromoController {
  readonly state: PromoState;
  /** Check the typed code against the current cart. */
  apply(raw: string): Promise<void>;
  /** The buyer takes the code off. */
  remove(): void;
  /** Take the code off without a note (the start call refused it). */
  drop(): void;
  /** The cart changed (or the sheet was just rendered): bring the quote up to date. */
  cartChanged(lineCount: number): void;
}

class QuoteTimeout extends Error {
  constructor() {
    super('quote timed out');
    this.name = 'QuoteTimeout';
  }
}

export function createPromoController(deps: PromoControllerDeps): PromoController {
  const timeoutMs = deps.timeoutMs ?? QUOTE_TIMEOUT_MS;
  const maxAttempts = deps.maxAttempts ?? MAX_QUOTE_ATTEMPTS;
  let state = initialPromoState();
  /** Counts the requests started; only the latest may write the state. */
  let current = 0;

  function set(patch: Partial<PromoState>): void {
    state = { ...state, ...patch };
    deps.onChange(state);
  }

  /** One bounded network wait: aborts the request and rejects after `timeoutMs`. */
  async function ask(payload: CheckoutQuotePayload): Promise<CheckoutQuoteResponse> {
    const ctl = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        ctl.abort();
        reject(new QuoteTimeout());
      }, timeoutMs);
    });
    try {
      return await Promise.race([deps.quote(payload, ctl.signal), deadline]);
    } finally {
      clearTimeout(timer);
    }
  }

  function failureText(err: unknown): string {
    return deps.conflictText(err) ?? promoErrorMessage(deps.errorCode(err), deps.strings());
  }

  async function run(code: string, mode: 'apply' | 'refresh'): Promise<void> {
    const seq = ++current;
    const s = deps.strings();
    set({ busy: true, error: mode === 'apply' ? null : state.error, note: null });
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      const key = deps.getCartKey();
      const payload = deps.buildPayload(code);
      if (!payload || !key) {
        if (seq === current) fail(mode, s.error_generic);
        return;
      }
      let res: CheckoutQuoteResponse;
      try {
        res = await ask(payload);
      } catch (err) {
        if (seq !== current) return;
        fail(mode, failureText(err));
        return;
      }
      if (seq !== current) return; // superseded by a newer request
      if (deps.getCartKey() !== key) continue; // the cart moved: quote the new one
      if (!res.promo_applied) {
        // The code is valid but takes nothing off: nothing to apply or to keep.
        fail(mode, s.error_no_discount);
        return;
      }
      set({ code, quote: toPromoQuote(code, key, res), busy: false, error: null, note: null });
      return;
    }
    // The cart kept changing under every answer. Fail closed rather than show
    // a price that may belong to another cart.
    if (seq === current) fail(mode, s.error_generic);
  }

  /** A failed attempt: an apply leaves the field as it was, a refresh takes the code off. */
  function fail(mode: 'apply' | 'refresh', text: string): void {
    if (mode === 'apply') {
      set({ busy: false, error: text });
      return;
    }
    set({
      code: null,
      quote: null,
      busy: false,
      error: `${text} ${deps.strings().removed}`,
      note: null,
    });
  }

  return {
    get state() {
      return state;
    },
    async apply(raw: string): Promise<void> {
      const code = raw.trim();
      if (!code) {
        set({ error: deps.strings().error_empty, note: null });
        return;
      }
      await run(code, 'apply');
    },
    remove(): void {
      current += 1;
      set({ code: null, quote: null, busy: false, error: null, note: deps.strings().removed });
    },
    drop(): void {
      current += 1;
      set({ code: null, quote: null, busy: false });
    },
    cartChanged(lineCount: number): void {
      if (!state.code) return;
      if (lineCount === 0) {
        current += 1;
        set({ code: null, quote: null, busy: false, error: null, note: null });
        return;
      }
      // A request in flight re-checks the cart itself when its answer lands.
      if (state.busy) return;
      if (state.quote && state.quote.cartKey === deps.getCartKey()) return;
      void run(state.code, 'refresh');
    },
  };
}

/**
 * True while the sheet must not let the buyer go on to pay: a code is applied
 * but its quote is missing, for another cart, or still being asked for. With no
 * code applied nothing blocks — a failed or hung quote never holds the cart.
 */
export function promoPayBlocked(state: PromoState, cartKey: string): boolean {
  if (state.busy) return true;
  if (!state.code) return false;
  return !state.quote || state.quote.cartKey !== cartKey;
}
