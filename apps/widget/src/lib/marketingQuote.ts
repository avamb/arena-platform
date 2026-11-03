/**
 * marketingQuote.ts — the price of the cart with the mailing tick (spec 51-1).
 *
 * When the buyer ticks the organizer's mailing box, the reward code may take
 * something off the order. The sheet does not work that out itself: it asks the
 * platform (`checkout/quote` with `marketing_consent`) and shows what comes
 * back, so the "Pay €45" button matches the payment page.
 *
 * The rules:
 *  - ONE request is current; an older answer never writes.
 *  - A quote is shown only for the cart AND the typed code it was asked about.
 *  - It never blocks paying. A failed, slow or refused quote simply leaves the
 *    list price on the button; the payment page applies the discount anyway.
 */

import type { CheckoutQuotePayload, CheckoutQuoteResponse } from './checkout.js';
import type { MarketingConsentPayload } from './marketing.js';
import { toPromoQuote, type PromoQuote } from './promo.js';

/** How long the tick's quote may take before it is given up. */
export const MARKETING_QUOTE_TIMEOUT_MS = 8000;

export interface MarketingQuoteState {
  /** `marketingQuoteKey` of the quote below; '' when there is none. */
  key: string;
  /** The platform's price of the cart with the tick, when it takes something off. */
  quote: PromoQuote | null;
  busy: boolean;
}

export function initialMarketingQuoteState(): MarketingQuoteState {
  return { key: '', quote: null, busy: false };
}

/** Identity of what the tick's quote was asked about: the cart and the typed code. */
export function marketingQuoteKey(cartKey: string, code: string | null): string {
  return cartKey ? `${cartKey}#${(code ?? '').trim()}` : '';
}

export interface MarketingQuoterDeps {
  getCartKey: () => string;
  /** The applied promo code, or null. */
  getCode: () => string | null;
  /** The quote payload of the cart as it is NOW (with the typed code), or null. */
  buildPayload: (code: string | null) => CheckoutQuotePayload | null;
  /** The tick as it would be sent, or undefined when it earns nothing. */
  consent: () => MarketingConsentPayload | undefined;
  quote: (payload: CheckoutQuotePayload, signal: AbortSignal) => Promise<CheckoutQuoteResponse>;
  onChange: (state: MarketingQuoteState) => void;
  timeoutMs?: number;
}

export interface MarketingQuoter {
  readonly state: MarketingQuoteState;
  /** The tick, the cart or the code changed: bring the quote up to date. */
  refresh(ticked: boolean): Promise<void>;
}

export function createMarketingQuoter(deps: MarketingQuoterDeps): MarketingQuoter {
  const timeoutMs = deps.timeoutMs ?? MARKETING_QUOTE_TIMEOUT_MS;
  let state = initialMarketingQuoteState();
  let current = 0;

  function set(next: MarketingQuoteState): void {
    state = next;
    deps.onChange(state);
  }

  async function ask(payload: CheckoutQuotePayload): Promise<CheckoutQuoteResponse> {
    const ctl = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        ctl.abort();
        reject(new Error('marketing quote timed out'));
      }, timeoutMs);
    });
    try {
      return await Promise.race([deps.quote(payload, ctl.signal), deadline]);
    } finally {
      clearTimeout(timer);
    }
  }

  return {
    get state() {
      return state;
    },
    async refresh(ticked: boolean): Promise<void> {
      const seq = ++current;
      const consent = ticked ? deps.consent() : undefined;
      const code = deps.getCode();
      const key = marketingQuoteKey(deps.getCartKey(), code);
      // A tick that earns no discount (the address already used it) changes no price.
      if (!consent || consent.discount === false || !key) {
        if (state.key !== '' || state.quote !== null || state.busy) set(initialMarketingQuoteState());
        return;
      }
      if (state.key === key && state.quote !== null && !state.busy) return;
      const payload = deps.buildPayload(code);
      if (!payload) {
        set(initialMarketingQuoteState());
        return;
      }
      set({ key: '', quote: null, busy: true });
      let res: CheckoutQuoteResponse;
      try {
        res = await ask({ ...payload, marketing_consent: consent });
      } catch {
        if (seq === current) set(initialMarketingQuoteState());
        return;
      }
      if (seq !== current) return;
      if (marketingQuoteKey(deps.getCartKey(), deps.getCode()) !== key || res.marketing_discount_applied !== true) {
        set(initialMarketingQuoteState());
        return;
      }
      set({ key, quote: toPromoQuote(code ?? '', deps.getCartKey(), res), busy: false });
    },
  };
}

/**
 * The quote the sheet prices the cart by: the tick's own, while the box is
 * ticked and its quote is for this very cart and code; otherwise the promo
 * code's (or none).
 */
export function sheetQuote(
  promoQuote: PromoQuote | null,
  marketing: MarketingQuoteState,
  ticked: boolean,
  cartKey: string,
  code: string | null,
): PromoQuote | null {
  if (ticked && marketing.quote && marketing.key !== '' && marketing.key === marketingQuoteKey(cartKey, code)) {
    return marketing.quote;
  }
  return promoQuote;
}
