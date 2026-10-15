/**
 * consent.ts — the checkout consent checkbox (P2), without Svelte.
 *
 * The feed session's `consent` block says whether the buyer must tick "I
 * accept the sale terms (seller: <legal name>) and the privacy policy" before
 * paying. While it is required and not ticked, the pay button
 * waits; a ticked box travels as `consent: {accepted: true, locale}` in
 * checkout/start, and the platform records it with the order. The tick is
 * never remembered: no storage, a new cart sheet starts unticked.
 */

import { getConsentStrings } from './consentI18n.js';

/** The `consent` member of a feed session. Absent on older backends. */
export interface SessionConsent {
  required: boolean;
  seller_legal_name?: string;
  terms_url?: string;
  privacy_url?: string;
}

/** The `consent` member of the checkout/start body. */
export interface CheckoutConsentPayload {
  accepted: true;
  locale?: string;
}

/** One piece of the checkbox text. */
export interface ConsentSegment {
  kind: 'text' | 'terms' | 'privacy' | 'seller';
  text: string;
}

/** The API code of a checkout refused for a missing consent. */
export const CONSENT_REQUIRED_CODE = 'checkout.consent_required';

/** Whether the session asks for the consent. An older backend sends nothing: not required. */
export function consentRequired(c: SessionConsent | null | undefined): boolean {
  return c?.required === true;
}

/**
 * The consent block the buyer form uses. `forced` is set after the platform
 * refused a checkout with checkout.consent_required although the feed session
 * did not ask for it: the box is then shown (required) with whatever links
 * the feed session carried, instead of leaving the buyer at a dead end.
 */
export function forceConsent(c: SessionConsent | null | undefined, forced: boolean): SessionConsent | null {
  if (!forced || consentRequired(c)) return c ?? null;
  return { ...(c ?? {}), required: true };
}

/** Whether the pay button waits for the box. */
export function consentPayBlocked(required: boolean, accepted: boolean): boolean {
  return required && !accepted;
}

/**
 * The checkout/start member for the buyer's answer: present only for a ticked
 * box, never for an unticked one (the platform then refuses a required
 * consent with 422, which is the honest answer).
 */
export function consentPayload(accepted: boolean, locale: string | null | undefined): CheckoutConsentPayload | undefined {
  if (!accepted) return undefined;
  const loc = (locale ?? '').trim();
  return loc ? { accepted: true, locale: loc } : { accepted: true };
}

/** An href the widget may put on a link: http(s) only, else null. */
export function safeConsentUrl(raw: string | null | undefined): string | null {
  const s = (raw ?? '').trim();
  if (!s) return null;
  try {
    const u = new URL(s);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : null;
  } catch {
    return null;
  }
}

/**
 * The checkbox text in the buyer's language, cut into plain text, the two
 * links and the seller's name. Without a name the bracketed "(seller: …)"
 * part and the space before it are dropped.
 */
export function consentSegments(locale: string, seller: string | null | undefined): ConsentSegment[] {
  const name = (seller ?? '').trim();
  let label = getConsentStrings(locale).label;
  // Without a name the bracketed "(seller: …)" goes away as a whole.
  if (!name) label = label.replace(/\s?\([^()]*\{seller\}[^()]*\)/, '');
  const out: ConsentSegment[] = [];
  const re = /\[terms\]([\s\S]*?)\[\/terms\]|\[privacy\]([\s\S]*?)\[\/privacy\]|\{seller\}/g;
  let last = 0;
  for (let m = re.exec(label); m !== null; m = re.exec(label)) {
    if (m.index > last) out.push({ kind: 'text', text: label.slice(last, m.index) });
    if (m[1] !== undefined) out.push({ kind: 'terms', text: m[1] });
    else if (m[2] !== undefined) out.push({ kind: 'privacy', text: m[2] });
    else out.push({ kind: 'seller', text: name });
    last = m.index + m[0].length;
  }
  if (last < label.length) out.push({ kind: 'text', text: label.slice(last) });
  return out;
}

/** The buyer text for a refusal for a missing consent, or null for any other code. */
export function consentErrorMessage(code: string | undefined, locale: string): string | null {
  return code === CONSENT_REQUIRED_CODE ? getConsentStrings(locale).error_required : null;
}
