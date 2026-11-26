/**
 * thankYou.ts - the blocks under a paid order: share, follow, promo,
 * newsletter, "you may also like". Pure helpers, free of Svelte, so they can
 * be tested without a DOM. Every load here fails silently: a missing block
 * must never turn a paid screen into an error screen.
 */

import { fetchFeedEvents, fetchFeedSite, postCheckoutMarketing } from '../api.js';
import type { FeedEventSummary } from '../types.js';

/** The thank-you message an organizer set for the feed (null when none). */
export interface ThankYouNote {
  title: string;
  text: string;
  promo_code: string | null;
  url: string | null;
}

export interface SocialLink {
  network: string;
  url: string;
}

/** GET /v1/public/feeds/{token}/site. */
export interface FeedSite {
  thank_you: ThankYouNote | null;
  socials: SocialLink[];
  /** Posters on the related cards; true when the server does not say. */
  thank_you_posters: boolean;
}

export type ShareNetwork = 'facebook' | 'linkedin' | 'reddit' | 'telegram' | 'whatsapp' | 'email' | 'x';

export interface ShareLink {
  network: ShareNetwork;
  href: string;
}

/** Per network: display name, brand colour, 24x24 fill path (evenodd). An empty path draws the generic link glyph. */
export const NET: Record<string, [string, string, string]> = {
  facebook: ['Facebook', '#1877f2', 'M14 21v-8h3l.5-3.5H14V7.5c0-1 .3-1.7 1.8-1.7h1.8V3h-2.6C12.6 3 11 4.6 11 7.5v2H7.5V13H11v8z'],
  linkedin: ['LinkedIn', '#0a66c2', 'M4 9h3v11H4zM5.5 4a1.8 1.8 0 1 1 0 3.6 1.8 1.8 0 0 1 0-3.6zM10 9h3v1.5c.6-1 1.7-1.8 3.2-1.8 3 0 3.8 2 3.8 4.6V20h-3v-5.5c0-1.3-.3-2.3-1.7-2.3S13 13.2 13 14.5V20h-3z'],
  reddit: ['Reddit', '#ff4500', 'M12 8c-4 0-7.5 2-7.5 5.5S8 19 12 19s7.5-2 7.5-5.5S16 8 12 8zm-2.7 4.5a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 0 0 0-2.4zm5.4 0a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 0 0 0-2.4zM13 8l.8-4 3.5.8z'],
  telegram: ['Telegram', '#26a5e4', 'M21 4 3 11l5 2 2 6 3-4 5 4zM9 13l9-6-7 7z'],
  whatsapp: ['WhatsApp', '#25d366', 'M12 3a9 9 0 0 0-8 13l-1 5 5-1A9 9 0 1 0 12 3zM9 8l1.5 3-1 1c.8 1.5 2 2.5 3.5 3.2l1-1 3 1.5c-.5 1.5-2 2-3.5 1.5C9 16 7 13 7.5 10z'],
  email: ['E-mail', '#6b7280', 'M3 5h18v14H3zm2 2v.5l7 5 7-5V7z'],
  x: ['X', '#000000', 'M18.2 2.3h3.3l-7.2 8.3 8.5 11.2h-6.7l-5.2-6.8-6 6.8H1.7l7.7-8.8L1.3 2.3h6.8l4.7 6.2zm-1.1 17.5h1.8L7.1 4.1H5.1z'],
  instagram: ['Instagram', '#e4405f', 'M7 3h10a4 4 0 0 1 4 4v10a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4V7a4 4 0 0 1 4-4zm0 2c-1 0-2 1-2 2v10c0 1 1 2 2 2h10c1 0 2-1 2-2V7c0-1-1-2-2-2zm5 2a5 5 0 1 1 0 10 5 5 0 0 1 0-10zm0 2a3 3 0 1 0 0 6 3 3 0 0 0 0-6z'],
  tiktok: ['TikTok', '#010101', 'M16.5 3h-3v12.5a2.5 2.5 0 1 1-2.5-2.5V10a5.5 5.5 0 1 0 5.5 5.5V9c1 .8 2.2 1.2 3.5 1.2V7.2A4 4 0 0 1 16.5 3z'],
  youtube: ['YouTube', '#ff0000', 'M3 7a3 3 0 0 1 3-2h12a3 3 0 0 1 3 2c.5 2 .5 8 0 10a3 3 0 0 1-3 2H6a3 3 0 0 1-3-2c-.5-2-.5-8 0-10zm7 2v6l5-3z'],


};

/** The generic link glyph. */
export const LINK_PATH = 'M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1 1.4 1.4 1-1a2 2 0 0 1 2.9 2.9l-3 3a2 2 0 0 1-2.9 0zM14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1-1.4-1.4-1 1a2 2 0 0 1-2.9-2.9l3-3a2 2 0 0 1 2.9 0z';

/** Networks the follow block accepts (share-only ones are not among them). */
const FOLLOW = 'facebook instagram telegram tiktok youtube x whatsapp linkedin';

/** Share links for one page address and one event name. Plain links, no scripts. */
export function buildShareLinks(url: string, name: string): ShareLink[] {
  const u = encodeURIComponent(url);
  const n = encodeURIComponent(name);
  const both = encodeURIComponent(name ? `${name} ${url}` : url);
  return [
    { network: 'facebook', href: `https://www.facebook.com/sharer/sharer.php?u=${u}` },
    { network: 'linkedin', href: `https://www.linkedin.com/sharing/share-offsite/?url=${u}` },
    { network: 'reddit', href: `https://www.reddit.com/submit?url=${u}&title=${n}` },
    { network: 'telegram', href: `https://t.me/share/url?url=${u}&text=${n}` },
    { network: 'whatsapp', href: `https://wa.me/?text=${both}` },
    { network: 'email', href: `mailto:?subject=${n}&body=${both}` },
    { network: 'x', href: `https://twitter.com/intent/tweet?url=${u}&text=${n}` },
  ];
}

/** The address to share: the event's page_url, else the current page without query or hash. */
export function shareTarget(
  pageUrl: string | null | undefined,
  loc?: { origin: string; pathname: string } | null,
): string {
  const p = (pageUrl ?? '').trim();
  if (p) return p;
  return loc ? `${loc.origin}${loc.pathname}` : '';
}

/** Only https social links of known networks, in the server's order. */
export function cleanSocials(list: unknown): SocialLink[] {
  if (!Array.isArray(list)) return [];
  const out: SocialLink[] = [];
  for (const item of list) {
    const network = typeof item?.network === 'string' ? item.network : '';
    const url = typeof item?.url === 'string' ? item.url.trim() : '';
    if (!FOLLOW.split(' ').includes(network) || !/^https:\/\//i.test(url)) continue;
    out.push({ network, url });
  }
  return out;
}

/** A note is drawn only when it has something to say. */
export function noteVisible(note: ThankYouNote | null | undefined): note is ThankYouNote {
  if (!note) return false;
  return Boolean((note.title ?? '').trim() || (note.text ?? '').trim() || note.promo_code || note.url);
}

/** A link of the note is only safe when it is https. */
export function safeHttpUrl(u: string | null | undefined): string | null {
  const v = (u ?? '').trim();
  return /^https:\/\//i.test(v) ? v : null;
}

/** Load /site. Any failure (404 from an older server too) is null. */
export async function loadFeedSite(feedToken: string, apiBase: string): Promise<FeedSite | null> {
  if (!feedToken) return null;
  try {
    const raw = await fetchFeedSite(feedToken, apiBase);
    return {
      thank_you: noteVisible(raw?.thank_you) ? raw.thank_you : null,
      socials: cleanSocials(raw?.socials),
      thank_you_posters: raw?.thank_you_posters !== false,
    };
  } catch {
    return null;
  }
}

/** One card of "You may also like". */
export interface RelatedEvent {
  id: string;
  name: string;
  pageUrl: string;
  date: string;
  /** Venue zone for the date (the venue holding that session), else the first known zone, else null. */
  timezone: string | null;
  /** Poster (or event image) address, null when the event has none. */
  posterUrl: string | null;
}

/** The zone of the venue whose sessions contain the chosen date, else the first venue with a zone. */
function relatedTimezone(e: FeedEventSummary, date: string): string | null {
  const venues = e.venues ?? [];
  const at = Date.parse(date);
  const own = venues.find(
    (v) => v?.timezone && (v.session_starts ?? []).some((s) => Date.parse(s) === at),
  );
  if (own?.timezone) return own.timezone;
  return venues.find((v) => v?.timezone)?.timezone ?? null;
}

/**
 * Up to `limit` future events other than the current one that have a page
 * address, nearest first. No poster is needed: an event without one keeps its
 * card, only without a picture. A date in the past or missing drops the event.
 */
export function pickRelatedEvents(
  events: FeedEventSummary[] | null | undefined,
  currentId: string | null | undefined,
  now: Date = new Date(),
  limit = 3,
): RelatedEvent[] {
  const t = now.getTime();
  const rows: Array<RelatedEvent & { at: number }> = [];
  for (const e of events ?? []) {
    if (!e || e.id === currentId) continue;
    const pageUrl = (e.page_url ?? '').trim();
    if (!pageUrl || !(e.name ?? '').trim()) continue;
    const first = e.first_session_at ? Date.parse(e.first_session_at) : NaN;
    const last = e.last_session_at ? Date.parse(e.last_session_at) : NaN;
    // The nearest date still ahead: the first start, or the last one for a run that has begun.
    let at = NaN;
    let date = '';
    if (!Number.isNaN(first) && first >= t) {
      at = first;
      date = e.first_session_at as string;
    } else if (!Number.isNaN(last) && last >= t) {
      at = last;
      date = e.last_session_at as string;
    }
    if (Number.isNaN(at)) continue;
    const posterUrl = (e.poster_url ?? '').trim() || (e.image_url ?? '').trim() || null;
    rows.push({ id: e.id, name: e.name, pageUrl, date, timezone: relatedTimezone(e, date), posterUrl, at });
  }
  rows.sort((a, b) => a.at - b.at);
  return rows.slice(0, limit).map(({ at: _at, ...r }) => r);
}

/** Load the related events. Any failure is an empty list. */
export async function loadRelatedEvents(
  feedToken: string,
  apiBase: string,
  currentId: string | null | undefined,
  now: Date = new Date(),
): Promise<RelatedEvent[]> {
  if (!feedToken) return [];
  try {
    const res = await fetchFeedEvents(feedToken, apiBase);
    return pickRelatedEvents(res?.events, currentId, now);
  } catch {
    return [];
  }
}

/** What the order status offers: the organizer's name and the consent text version (null: no offer). */
export interface MarketingOffer {
  org_name: string;
  text_version: string;
}

/** The consent line with the organizer's name in it (the text the version stands for). */
export function consentText(template: string, orgName: string): string {
  return template.replace('{org}', orgName);
}

/** The body of the newsletter subscription: the version the offer named. */
export function marketingBody(textVersion: string, pageUrl: string): { text_version: string; page_url?: string } {
  const body: { text_version: string; page_url?: string } = { text_version: textVersion };
  const p = (pageUrl ?? '').trim();
  if (p) body.page_url = p;
  return body;
}

export type SubscribeResult = 'pending' | 'already_confirmed' | 'not_paid' | 'not_offered' | 'rate_limited' | 'error';

/** Subscribe the order's e-mail. Never throws. */
export async function subscribeToNews(
  checkoutToken: string,
  apiBase: string,
  pageUrl: string,
  textVersion: string,
): Promise<SubscribeResult> {
  try {
    const res = await postCheckoutMarketing(checkoutToken, marketingBody(textVersion, pageUrl), apiBase);
    return res?.status === 'already_confirmed' ? 'already_confirmed' : 'pending';
  } catch (err) {
    const status = (err as { status?: number } | null)?.status;
    const code = (err as { code?: string } | null)?.code;
    if (code === 'marketing.not_offered') return 'not_offered';
    if (status === 409 || code === 'checkout.not_paid') return 'not_paid';
    if (status === 429) return 'rate_limited';
    return 'error';
  }
}
