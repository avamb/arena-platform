import type { PageLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { PluralForms } from './i18n.ts';
import { renderArtHtml } from './richtext.ts';
import { renderSingleVideo } from './materials.ts';
import type { MaterialsHooks } from './materials.ts';
import { intlLocale, renderCatalogCard } from './render.ts';
import { ApiError, fetchHostedPage, fetchSeriesPage } from './api.ts';
import type { HostedPageResponse, HostedPageVideo, HostedSeriesPageResponse, HostedSeriesSummary } from './api.ts';

/**
 * series.ts — the tour page (event series): /{org}/{series_slug}.
 *
 * The address is the same as an event page's, so the page asks for the event
 * first and, only on a 404, for the series (resolveEventOrSeries).
 */

export type EventOrSeries =
  | { kind: 'event'; data: HostedPageResponse }
  | { kind: 'series'; data: HostedSeriesPageResponse };

/** Event first; on 404 the series of the same slug. A 404 of both is the
 * series' ApiError(404), so the caller shows the usual not-found screen. Any
 * other failure of the event request is thrown as it is (no guessing). */
export async function resolveEventOrSeries(
  apiBase: string,
  orgSlug: string,
  slug: string,
  signal?: AbortSignal,
): Promise<EventOrSeries> {
  try {
    return { kind: 'event', data: await fetchHostedPage(apiBase, orgSlug, slug, signal) };
  } catch (err) {
    if (!(err instanceof ApiError) || err.status !== 404) throw err;
  }
  return { kind: 'series', data: await fetchSeriesPage(apiBase, orgSlug, slug, signal) };
}

function plural(forms: PluralForms, n: number, locale: PageLocale): string {
  let category: Intl.LDMLPluralRule = 'other';
  try {
    category = new Intl.PluralRules(locale).select(n);
  } catch {
    /* keep other */
  }
  return (forms[category] ?? forms.other).replace('{n}', String(n));
}

/** "from 39 € " — one price in its currency, formatted for the locale; a
 * whole amount has no decimals. An unknown currency code is printed as is. */
export function formatPriceFrom(amount: number, currency: string, locale: PageLocale): string {
  const tag = intlLocale(locale);
  const whole = Number.isInteger(amount);
  let money: string;
  try {
    money = new Intl.NumberFormat(tag, {
      style: 'currency',
      currency,
      minimumFractionDigits: whole ? 0 : 2,
      maximumFractionDigits: whole ? 0 : 2,
    }).format(amount);
  } catch {
    money = `${amount} ${currency}`;
  }
  return t(locale).seriesFrom.replace('{price}', money);
}

/** The localized range of the tour's dates ("16–18 October 2026"), or "". */
export function formatSeriesDates(from: string | null, to: string | null, locale: PageLocale): string {
  const a = from ? new Date(from) : null;
  const b = to ? new Date(to) : a;
  if (!a || Number.isNaN(a.getTime()) || !b || Number.isNaN(b.getTime())) return '';
  try {
    const fmt = new Intl.DateTimeFormat(intlLocale(locale), { day: 'numeric', month: 'long', year: 'numeric' });
    return fmt.formatRange(a, b);
  } catch {
    return '';
  }
}

/** The parts of the summary line, in order: dates, cities, cities on sale,
 * one "from" price per currency. Empty parts are left out. */
export function summaryParts(summary: HostedSeriesSummary | null | undefined, locale: PageLocale): string[] {
  if (!summary) return [];
  const strings = t(locale);
  const parts: string[] = [];
  const dates = formatSeriesDates(summary.date_from, summary.date_to, locale);
  if (dates) parts.push(dates);
  if (summary.cities_count > 0) parts.push(plural(strings.seriesCities, summary.cities_count, locale));
  if (summary.cities_on_sale_count > 0) parts.push(plural(strings.seriesOnSale, summary.cities_on_sale_count, locale));
  for (const price of summary.price_from ?? []) {
    if (typeof price.amount === 'number' && Number.isFinite(price.amount) && price.currency) {
      parts.push(formatPriceFrom(price.amount, price.currency, locale));
    }
  }
  return parts;
}

/** YouTube or Vimeo address -> the video the materials renderer knows, with
 * the privacy embed address (youtube-nocookie / player.vimeo.com); null for
 * anything else. */
export function videoFromURL(raw: string | null | undefined): HostedPageVideo | null {
  if (typeof raw !== 'string') return null;
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return null;
  }
  if (u.protocol !== 'https:') return null;
  const host = u.hostname.replace(/^www\./, '').replace(/^m\./, '');
  const idOK = (s: string | null | undefined): s is string => typeof s === 'string' && /^[A-Za-z0-9_-]{6,20}$/.test(s);
  let yt: string | null = null;
  if (host === 'youtu.be') {
    yt = u.pathname.split('/')[1] ?? null;
  } else if (host === 'youtube.com' || host === 'youtube-nocookie.com') {
    const m = /^\/(?:embed|shorts|live)\/([^/]+)/.exec(u.pathname);
    yt = m ? m[1] : u.searchParams.get('v');
  }
  if (idOK(yt)) {
    return { provider: 'youtube', video_id: yt, embed_url: `https://www.youtube-nocookie.com/embed/${yt}`, url: u.toString() };
  }
  if (host === 'vimeo.com' || host === 'player.vimeo.com') {
    const m = /^\/(?:video\/)?(\d{5,12})(?:\/|$)/.exec(u.pathname);
    if (m) {
      return { provider: 'vimeo', video_id: m[1], embed_url: `https://player.vimeo.com/video/${m[1]}`, url: u.toString() };
    }
  }
  return null;
}

export interface RenderSeriesOptions extends MaterialsHooks {
  /** Slug the page was opened under; links to events use the org's own slug. */
  orgSlug?: string;
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

/** Plain text of a safe-HTML fragment, for <meta name="description">. */
export function seriesDescriptionPlain(html: string | null | undefined): string {
  if (!html) return '';
  const parsed = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html');
  return (parsed.body.textContent ?? '').replace(/\s+/g, ' ').trim();
}

/**
 * Mounts the tour page: hero (video or picture), name, poster, the summary
 * line, the description, the cards of its events (each links to
 * /{org}/{event_slug}) and the partners text. Texts of the organizer go
 * through the same safe-HTML renderer as an event description.
 */
export function renderSeriesPage(
  container: HTMLElement,
  data: HostedSeriesPageResponse,
  locale: PageLocale,
  options: RenderSeriesOptions = {},
): void {
  while (container.firstChild) container.removeChild(container.firstChild);
  const strings = t(locale);
  const s = data.series;
  const orgSlug = data.org?.slug ?? options.orgSlug ?? '';

  const hero = el('section', 'asa-hero asa-series-hero');
  hero.setAttribute('aria-label', s.name);

  const video = videoFromURL(s.hero_video_url);
  const videoEl = video ? renderSingleVideo(video, locale, options) : null;
  if (videoEl) {
    hero.appendChild(videoEl);
  } else if (s.hero_url) {
    const img = el('img', 'asa-hero-image asa-series-hero__image');
    img.src = s.hero_url;
    img.alt = s.name;
    hero.appendChild(img);
  }

  const body = el('div', 'asa-hero-body');
  body.appendChild(el('h1', 'asa-hero-title', s.name));

  const parts = summaryParts(s.summary, locale);
  if (parts.length > 0) {
    body.appendChild(el('p', 'asa-hero-meta asa-series-summary', parts.join(' · ')));
  }

  if (s.poster_url) {
    const img = el('img', 'asa-series-poster');
    img.src = s.poster_url;
    img.alt = s.name;
    body.appendChild(img);
  }

  if (s.description_html) {
    const box = el('div', 'asa-hero-description asa-series-description');
    box.appendChild(renderArtHtml(document, s.description_html));
    body.appendChild(box);
  }
  hero.appendChild(body);
  container.appendChild(hero);

  const events = data.events ?? [];
  if (events.length > 0) {
    const section = el('section', 'asa-dates asa-series-events');
    section.appendChild(el('h2', 'asa-dates__head', strings.seriesEvents));
    const grid = el('ul', 'asa-card-grid');
    grid.setAttribute('aria-label', `${s.name} — ${strings.seriesEvents}`);
    const now = Date.now();
    for (const event of events) grid.appendChild(renderCatalogCard(event, orgSlug, locale, now));
    section.appendChild(grid);
    container.appendChild(section);
  }

  if (s.partners_text_html) {
    const section = el('section', 'asa-series-partners');
    section.appendChild(el('h2', 'asa-materials__title', strings.seriesPartners));
    const box = el('div', 'asa-series-partners__text');
    box.appendChild(renderArtHtml(document, s.partners_text_html));
    section.appendChild(box);
    container.appendChild(section);
  }
}
