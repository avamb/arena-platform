/**
 * referral.ts — "Refer a friend" on the buyer's side (spec 52 sections 3.3, 7.2).
 *
 * Two small jobs, both free of Svelte so they can be tested without a DOM:
 *
 *  1. The friend's side. A shared link carries `?promo=<code>`. The widget
 *     reads the code from the address (or from the `promo` attribute the
 *     hosted page hands over), keeps it for the life of the tab and applies it
 *     to the first cart through the ordinary promo controller. The platform
 *     stays the judge: a code that does not fit is taken off with its reason.
 *
 *  2. The referrer's side. On a paid order the widget asks the platform for
 *     the order's personal code. Anything but a clear answer (404, network,
 *     server) means the block is simply not drawn; the paid screen never
 *     shows an error for something the buyer did not ask for.
 *
 * Nothing here is persisted beyond the tab: the code lives in sessionStorage
 * (every call in try/catch, like attribution.ts), keyed by feed token.
 */

import type { ReferralStrings } from './referralI18n.js';
import { interpolate } from './checkout.js';

// ─── ?promo= ────────────────────────────────────────────────────────────────

const PROMO_RE = /^[A-Z0-9-]{3,64}$/;
const KEY_PREFIX = 'arena_promo:';

/**
 * A promo code from the address or an attribute: trimmed, upper-cased, and only
 * letters, digits and dashes (3-64). Anything else is null — a hostile or
 * garbled value is dropped, never repaired.
 */
export function parsePromoParam(raw: string | null | undefined): string | null {
  const v = (raw ?? '').trim().toUpperCase();
  return PROMO_RE.test(v) ? v : null;
}

/** The code named by `?promo=` in a query string, or null. */
export function promoFromSearch(search: string): string | null {
  try {
    return parsePromoParam(new URLSearchParams(search).get('promo'));
  } catch {
    return null;
  }
}

/** The sessionStorage key for one feed token. */
export function promoStorageKey(feedToken: string): string {
  return `${KEY_PREFIX}${feedToken.trim()}`;
}

function defaultStorage(): Storage | undefined {
  try {
    return typeof sessionStorage === 'undefined' ? undefined : sessionStorage;
  } catch {
    return undefined;
  }
}

/**
 * The code this tab is carrying for the feed: the `promo` attribute first,
 * then the address, then what an earlier page of this tab kept. A code found in
 * the first two is remembered. Returns null when there is none.
 */
export function resolveWantedPromo(
  feedToken: string,
  attribute: string,
  search: string,
  storage?: Storage | null,
): string | null {
  const store = storage === undefined ? defaultStorage() : (storage ?? undefined);
  const key = feedToken.trim() ? promoStorageKey(feedToken) : '';
  const fresh = parsePromoParam(attribute) ?? promoFromSearch(search);
  if (fresh) {
    if (store && key) {
      try {
        store.setItem(key, fresh);
      } catch {
        // Storage refused the write: the code of this page still applies.
      }
    }
    return fresh;
  }
  if (store && key) {
    try {
      return parsePromoParam(store.getItem(key));
    } catch {
      return null;
    }
  }
  return null;
}

/** Forget the kept code (the buyer took it off for good). */
export function forgetWantedPromo(feedToken: string, storage?: Storage | null): void {
  const store = storage === undefined ? defaultStorage() : (storage ?? undefined);
  if (!store || !feedToken.trim()) return;
  try {
    store.removeItem(promoStorageKey(feedToken));
  } catch {
    // Nothing to do.
  }
}

// ─── The referrer's block ───────────────────────────────────────────────────

/** `GET /v1/public/checkout/{token}/referral` */
export interface ReferralCode {
  code: string;
  friend_discount_pct: number;
  cashback_pct: number;
  share_url: string | null;
}

/** What the paid screen does with the answer. */
export type ReferralBlock = { show: false } | { show: true; data: ReferralCode };

function isReferralCode(v: unknown): v is ReferralCode {
  if (!v || typeof v !== 'object') return false;
  const r = v as Record<string, unknown>;
  return (
    typeof r['code'] === 'string' &&
    r['code'] !== '' &&
    typeof r['friend_discount_pct'] === 'number' &&
    typeof r['cashback_pct'] === 'number' &&
    (r['share_url'] === null || r['share_url'] === undefined || typeof r['share_url'] === 'string')
  );
}

/**
 * Ask for the order's code and decide whether to draw the block. A refusal
 * (404 `referral.not_available`), any failure of the call and a malformed
 * answer all mean "do not show", never an error on screen.
 */
export async function loadReferralBlock(fetcher: () => Promise<unknown>): Promise<ReferralBlock> {
  try {
    const res = await fetcher();
    if (!isReferralCode(res)) return { show: false };
    return { show: true, data: { ...res, share_url: res.share_url ?? null } };
  } catch {
    return { show: false };
  }
}

/** The explaining sentence under the heading. */
export function referralText(s: ReferralStrings, r: ReferralCode): string {
  return interpolate(s.text, { discount: String(r.friend_discount_pct), cashback: String(r.cashback_pct) });
}

/** What the share sheet is given. */
export function shareData(s: ReferralStrings, r: ReferralCode): { title: string; text: string; url?: string } {
  const out: { title: string; text: string; url?: string } = {
    title: s.title,
    text: interpolate(s.share_text, { discount: String(r.friend_discount_pct), code: r.code }),
  };
  if (r.share_url) out.url = r.share_url;
  return out;
}

/** The value the copy button puts on the clipboard: the link, else the code. */
export function copyValue(r: ReferralCode): string {
  return r.share_url ?? r.code;
}

/** The "Share" button exists only where the browser can share. */
export function canNativeShare(nav: { share?: unknown } | undefined | null): boolean {
  return !!nav && typeof nav.share === 'function';
}

/** Copy to the clipboard; false when the browser refuses (insecure page, no permission). */
export async function copyToClipboard(
  text: string,
  nav: { clipboard?: { writeText?: (t: string) => Promise<void> } } | undefined | null,
): Promise<boolean> {
  try {
    if (!nav?.clipboard || typeof nav.clipboard.writeText !== 'function') return false;
    await nav.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}
