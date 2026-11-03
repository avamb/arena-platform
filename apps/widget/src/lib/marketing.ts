/**
 * marketing.ts — the organizer's mailing tick and the unpaid-order reminder
 * line (spec 51-1), without Svelte.
 *
 * The feed session's `marketing_optin` block says whether the buyer may tick
 * "send me news and offers from <organizer>". The tick is never pre-ticked,
 * never required and never stored by the widget. Ticked, it travels as
 * `marketing_consent: {accepted: true, text_version}` in checkout/start and
 * may earn the organizer's reward code for this very order. The platform
 * answers 422 `marketing.discount_used` when this address already used it:
 * the widget then says so and the next press sends `discount: false`.
 */

import { getMarketingStrings } from './marketingI18n.js';

/**
 * The text version the widget shows. It must equal the platform's
 * `marketing.TextVersion`: change both together whenever the tick text
 * changes meaning.
 */
export const MARKETING_TEXT_VERSION = 'marketing-optin-2026-11-02';

/** The discount the tick earns. */
export interface MarketingDiscount {
  type: string;
  value: number;
  currency?: string | null;
}

/** The `marketing_optin` member of a feed session. Absent on older backends. */
export interface SessionMarketingOptIn {
  enabled: boolean;
  org_name?: string;
  text_version?: string;
  discount?: MarketingDiscount | null;
}

/** The `marketing_consent` member of the checkout/start body. */
export interface MarketingConsentPayload {
  accepted: true;
  text_version: string;
  discount?: false;
}

/** The API code of a refused reward: this address already used it. */
export const MARKETING_DISCOUNT_USED_CODE = 'marketing.discount_used';
/** The API code of a refused reward: all its uses are taken. */
export const MARKETING_DISCOUNT_UNAVAILABLE_CODE = 'marketing.discount_unavailable';

/**
 * Whether the platform refused the tick's reward. The next press then sends
 * `discount: false`, so the buyer can pay the regular price.
 */
export function isMarketingRewardRefusal(code: string | undefined): boolean {
  return code === MARKETING_DISCOUNT_USED_CODE || code === MARKETING_DISCOUNT_UNAVAILABLE_CODE;
}
/** The API code of a tick shown with an older text. */
export const MARKETING_OUTDATED_CODE = 'marketing.consent_outdated';

/** Whether the widget offers the tick at all. */
export function marketingOffered(m: SessionMarketingOptIn | null | undefined): boolean {
  return m?.enabled === true;
}

/**
 * The checkout/start member for the buyer's answer: present only for a
 * ticked box. `discountUsed` is set after the platform said the reward was
 * already used by this address: the consent is still recorded, the reward
 * is not asked for again.
 */
export function marketingPayload(
  m: SessionMarketingOptIn | null | undefined,
  ticked: boolean,
  discountUsed: boolean,
): MarketingConsentPayload | undefined {
  if (!ticked || !marketingOffered(m)) return undefined;
  // The version of the text THIS widget shows, never the one the feed names:
  // an older cached widget must be refused (consent_outdated), not recorded
  // as having shown a text it never showed.
  const version = MARKETING_TEXT_VERSION;
  return discountUsed ? { accepted: true, text_version: version, discount: false } : { accepted: true, text_version: version };
}

/** "10 %" or "5.00 EUR" for the label; "" for an unknown shape. */
export function formatMarketingDiscount(d: MarketingDiscount | null | undefined, locale: string): string {
  if (!d || !(d.value > 0)) return '';
  if (d.type === 'percent') return `${d.value} %`;
  if (d.type === 'fixed_amount' && d.currency) {
    try {
      return new Intl.NumberFormat(locale || 'en', { style: 'currency', currency: d.currency }).format(d.value / 100);
    } catch {
      return `${(d.value / 100).toFixed(2)} ${d.currency}`;
    }
  }
  return '';
}

/** The tick text: with the discount when the session earns one and it is not used up. */
export function marketingLabel(m: SessionMarketingOptIn | null | undefined, locale: string, discountUsed: boolean): string {
  const w = getMarketingStrings(locale);
  const org = (m?.org_name ?? '').trim();
  const discount = discountUsed ? '' : formatMarketingDiscount(m?.discount, locale);
  const base = org ? w.label.replace('{org}', org) : w.label_no_org;
  return discount ? `${base} ${w.discount.replace('{discount}', discount)}` : base;
}

/** The buyer text for a refusal of the tick, or null for any other code. */
export function marketingErrorMessage(code: string | undefined, locale: string): string | null {
  const w = getMarketingStrings(locale);
  if (code === MARKETING_DISCOUNT_USED_CODE) return w.discount_used;
  if (code === MARKETING_DISCOUNT_UNAVAILABLE_CODE) return w.discount_unavailable;
  if (code === MARKETING_OUTDATED_CODE) return w.outdated;
  return null;
}
