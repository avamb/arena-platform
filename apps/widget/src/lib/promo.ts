/**
 * promo.ts — pure helpers for the promo code in the cart sheet.
 *
 * The sheet never computes a discount. It asks the platform
 * (`POST checkout/quote`) and shows what comes back, so the total in the sheet
 * is the total on the payment page. These helpers only decide WHICH numbers to
 * show and WHICH words to print.
 */

import { formatPrice } from './checkout.js';
import type { CheckoutQuoteResponse, PublicGAItem } from './checkout.js';
import type { PromoStrings } from './promoI18n.js';

/** A quote the platform gave for one exact cart and one exact code. */
export interface PromoQuote {
  /** The code as the buyer typed it (trimmed). */
  code: string;
  /** Identity of the cart the quote was made for (see `cartQuoteKey`). */
  cartKey: string;
  subtotal: number;
  discount: number;
  fee: number;
  total: number;
  currency: string;
}

/**
 * Identity of a cart for quoting: session, seats and GA quantities, in a fixed
 * order. A quote is shown only while the cart still has the key it was made
 * for, so removing a line can never leave yesterday's discount on screen.
 */
export function cartQuoteKey(sessionId: string, seats: readonly string[], gaItems: readonly PublicGAItem[]): string {
  const s = [...seats].sort().join(',');
  const g = [...gaItems]
    .map((i) => `${i.tier_id}:${i.quantity}`)
    .sort()
    .join(',');
  return `${sessionId}|${s}|${g}`;
}

/** Turn a quote response into the sheet's view of it. */
export function toPromoQuote(code: string, cartKey: string, res: CheckoutQuoteResponse): PromoQuote {
  return {
    code: code.trim(),
    cartKey,
    subtotal: res.pricing.subtotal,
    discount: res.pricing.discount,
    fee: res.pricing.platform_fee,
    total: res.pricing.total,
    currency: res.pricing.currency,
  };
}

/** What the sheet prints for the money rows. */
export interface SheetAmounts {
  /** Price of the tickets before any discount. */
  subtotal: number;
  /** Promo discount, 0 when none applies. */
  discount: number;
  /** Service fee. */
  fee: number;
  /** What the buyer pays. */
  total: number;
  currency: string;
  /** True when the numbers come from a promo quote of THIS cart. */
  discounted: boolean;
}

/**
 * The numbers for the sheet. With a quote made for this very cart they are the
 * platform's, verbatim. Otherwise they are the local list prices and the
 * basis-point fee (`cartServiceFee`), which is the same floor the platform uses
 * when no code is applied.
 */
export function sheetAmounts(
  local: { subtotal: number; fee: number; currency: string },
  quote: PromoQuote | null,
  cartKey: string,
): SheetAmounts {
  if (quote && quote.cartKey === cartKey) {
    return {
      subtotal: quote.subtotal,
      discount: quote.discount,
      fee: quote.fee,
      total: quote.total,
      currency: quote.currency || local.currency,
      discounted: true,
    };
  }
  return {
    subtotal: local.subtotal,
    discount: 0,
    fee: local.fee,
    total: local.subtotal + local.fee,
    currency: local.currency,
    discounted: false,
  };
}

// ─── Reading direction of the amounts ────────────────────────────────────────

const LRI = '⁦'; // left-to-right isolate
const PDI = '⁩'; // pop directional isolate
const MINUS = '−';

/**
 * An amount that keeps its own left-to-right order inside a right-to-left
 * sheet. Without the isolate the bidi algorithm moves a leading "−" or
 * currency sign to the far end of the number in Hebrew ("€5.00−").
 */
export function isolateLtr(text: string): string {
  return `${LRI}${text}${PDI}`;
}

/** "−€5.00", isolated so the minus stays in front of the amount in any direction. */
export function formatDiscount(amountMinor: number, currency: string): string {
  return isolateLtr(`${MINUS}${formatPrice(amountMinor, currency)}`);
}

/** A plain amount, isolated the same way. */
export function formatAmount(amountMinor: number, currency: string): string {
  return isolateLtr(formatPrice(amountMinor, currency));
}

// ─── Words ───────────────────────────────────────────────────────────────────

/**
 * The words for a promo problem. `code` is the error code of the platform
 * (`promo.*`); anything else is a failure of the check itself.
 *
 * The platform answers a code that does not fit with ONE neutral code on
 * purpose, so there is no "expired" or "wrong event" here and none must be
 * added: the buyer is told only what they can act on.
 */
export function promoErrorMessage(code: string | undefined, p: PromoStrings): string {
  switch (code) {
    case 'promo.minimum_not_reached':
      return p.error_minimum;
    case 'promo.exhausted':
      return p.error_exhausted;
    case 'promo.per_customer_limit':
      return p.error_per_customer;
    case 'promo.rate_limited':
    // The shared public-feed limiter answers a burst with its own code.
    case 'feed.rate_limited':
      return p.error_rate_limited;
    case 'promo.not_applicable':
    // checkout/start still answers with the platform's finer codes; to a buyer
    // they all mean "this code does not work for this order".
    case 'promo.not_found':
    case 'promo.not_active':
    case 'promo.not_yet_valid':
    case 'promo.expired':
    case 'promo.session_not_applicable':
    case 'promo.tier_not_applicable':
    case 'promo.currency_mismatch':
      return p.error_not_applicable;
    case 'promo.invalid_order_amount':
      return p.error_minimum;
    default:
      return p.error_generic;
  }
}

/** True when an API error code is a promo verdict (not a network or seat problem). */
export function isPromoErrorCode(code: string | undefined): boolean {
  return typeof code === 'string' && code.startsWith('promo.');
}
