/**
 * referral.ts - "Refer a friend" on the storefront (spec 52 section 3.5).
 *
 * A friend arrives on a link like `/?promo=ANNA-7K2Q&utm_source=referral`. The
 * code is remembered for the tab (sessionStorage, per shop) so it survives the
 * walk from the landing page to the checkout, and is offered in the checkout's
 * promo field. The buyer who has just paid gets a block with their own code.
 * Nothing here decides a discount: the server applies it and answers.
 */

/** A code as the platform makes them (letters, digits, dot, dash, underscore), at most 64 characters. */
const PROMO_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/** The code the API will accept for a typed or linked value, or '' when it cannot be one. */
export function cleanPromo(raw: string | null | undefined): string {
  const v = (raw ?? '').trim();
  return PROMO_RE.test(v) ? v : '';
}

const key = (org: string): string => 'arena_shop_promo:' + org;

/** Remembers the `promo` of the address for this tab; a link without a valid one changes nothing. */
export function capturePromo(org: string, search: string): string {
  const code = cleanPromo(new URLSearchParams(search).get('promo'));
  if (code === '') return storedPromo(org);
  try {
    sessionStorage.setItem(key(org), code);
  } catch {
    /* a closed storage: the code is then used on this page load only */
    memory.set(org, code);
  }
  return code;
}

const memory = new Map<string, string>();

/** The remembered code of this tab, '' when there is none. */
export function storedPromo(org: string): string {
  try {
    return cleanPromo(sessionStorage.getItem(key(org))) || memory.get(org) || '';
  } catch {
    return memory.get(org) || '';
  }
}

/** Forgets the remembered code (after the order it was used for). */
export function forgetPromo(org: string): void {
  memory.delete(org);
  try {
    sessionStorage.removeItem(key(org));
  } catch {
    /* ignore */
  }
}

/** What GET /v1/public/shop/checkout/{token}/referral answers. */
export interface ReferralShare {
  code: string;
  friend_discount_pct: number;
  cashback_pct: number;
  /** The storefront link to hand out; null when the shop's address is not known. */
  share_url: string | null;
}

/** A programme percent from the wire: a whole number from 1 to 100, else 0 (no preview). */
export function cleanPct(raw: unknown): number {
  return typeof raw === 'number' && Number.isInteger(raw) && raw >= 1 && raw <= 100 ? raw : 0;
}

/**
 * What a friend's code would take off the goods, worked out the way the server does it: the
 * programme's percent of the goods total (delivery never), rounded down in minor units. A
 * preview only - the payment page shows the final amount, and the server decides.
 */
export function referralDiscountMinor(subtotalMinor: number, pct: number, code: string): number {
  if (cleanPromo(code) === '' || !(subtotalMinor > 0) || !(pct > 0)) return 0;
  return Math.floor((subtotalMinor * Math.min(pct, 100)) / 100);
}
