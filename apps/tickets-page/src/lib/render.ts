import type { PageLocale } from './locale.ts';
import { isRtlLocale, toWidgetLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { HostedPageEvent, HostedPageResponse, HostedPromoterPageResponse } from './api.ts';

/** Clears a container's children (avoids innerHTML = '' churn semantics
 * differences and keeps this file free of innerHTML entirely). */
function clear(el: HTMLElement): void {
  while (el.firstChild) el.removeChild(el.firstChild);
}

/** Preserves every query parameter except none are stripped — used when
 * linking between the promoter page and its event pages so `?lang=` (and
 * any future param, e.g. a resumed checkout_token) survives the hop. */
function currentSearch(): string {
  return window.location.search;
}

export function renderLoading(container: HTMLElement, locale: PageLocale): void {
  clear(container);
  const p = document.createElement('p');
  p.className = 'asa-loading';
  p.setAttribute('role', 'status');
  p.textContent = t(locale).loading;
  container.appendChild(p);
}

export function renderNotFound(container: HTMLElement, locale: PageLocale): void {
  clear(container);
  const strings = t(locale);

  const section = document.createElement('section');
  section.className = 'asa-state asa-state--not-found';

  const h1 = document.createElement('h1');
  h1.textContent = strings.notFoundTitle;
  section.appendChild(h1);

  const p = document.createElement('p');
  p.textContent = strings.notFoundBody;
  section.appendChild(p);

  const a = document.createElement('a');
  a.className = 'asa-button';
  a.href = '/';
  a.textContent = strings.notFoundHome;
  section.appendChild(a);

  container.appendChild(section);
}

export function renderError(container: HTMLElement, locale: PageLocale, onRetry: () => void): void {
  clear(container);
  const strings = t(locale);

  const section = document.createElement('section');
  section.className = 'asa-state asa-state--error';

  const h1 = document.createElement('h1');
  h1.textContent = strings.errorTitle;
  section.appendChild(h1);

  const p = document.createElement('p');
  p.textContent = strings.errorBody;
  section.appendChild(p);

  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'asa-button';
  button.textContent = strings.errorRetry;
  button.addEventListener('click', onRetry);
  section.appendChild(button);

  container.appendChild(section);
}

/** Formats an event/session start as "weekday, day month year, HH:MM" in
 * the given locale. When `timeZone` is known (the event's own venue
 * timezone, from `first_session_timezone`) the time is shown in THAT zone
 * rather than the viewer's — a buyer in Madrid checking a Prague master
 * class should see Prague local time, not their own. Falls back to the
 * viewer's local zone when unknown. */
function formatSessionDateTime(iso: string, locale: PageLocale, timeZone?: string | null): string {
  try {
    return new Intl.DateTimeFormat(locale, {
      weekday: 'long',
      day: 'numeric',
      month: 'long',
      year: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
      timeZone: timeZone ?? undefined,
    }).format(new Date(iso));
  } catch {
    return iso;
  }
}

export interface RenderEventOptions {
  apiBase: string;
  resumingCheckout: boolean;
}

/**
 * renderEvent mounts the hero (image/title/date/venue/description), a back
 * link to the org's promoter page, and the <arena-tickets> widget for a
 * resolved hosted page. Returns the widget host element so the caller can
 * scroll it into view when resuming a checkout.
 */
export function renderEvent(
  container: HTMLElement,
  data: HostedPageResponse,
  locale: PageLocale,
  options: RenderEventOptions,
): HTMLElement {
  clear(container);

  const back = document.createElement('a');
  back.className = 'asa-back-link';
  back.href = `/${encodeURIComponent(data.org.slug)}${currentSearch()}`;
  back.textContent = `← ${t(locale).backToPromoter}`;
  container.appendChild(back);

  const hero = document.createElement('section');
  hero.className = 'asa-hero';
  hero.setAttribute('aria-label', data.event.title);

  const imageURL = data.event.poster_url ?? data.event.image_url;
  if (imageURL) {
    const img = document.createElement('img');
    img.className = 'asa-hero-image';
    img.src = imageURL;
    img.alt = data.event.title;
    hero.appendChild(img);
  }

  const heroBody = document.createElement('div');
  heroBody.className = 'asa-hero-body';

  const h1 = document.createElement('h1');
  h1.className = 'asa-hero-title';
  h1.textContent = data.event.title;
  heroBody.appendChild(h1);

  const meta = document.createElement('p');
  meta.className = 'asa-hero-meta';
  const metaParts: string[] = [];
  if (data.event.first_session_at) {
    metaParts.push(formatSessionDateTime(data.event.first_session_at, locale, data.event.first_session_timezone));
  }
  if (data.event.venue_names.length > 0) {
    metaParts.push(data.event.venue_names.join(', '));
  }
  if (data.event.age_rating) {
    metaParts.push(data.event.age_rating);
  }
  if (metaParts.length > 0) {
    meta.textContent = metaParts.join(' · ');
    heroBody.appendChild(meta);
  }

  const description = data.event.short_description ?? data.event.description;
  if (description) {
    const p = document.createElement('p');
    p.className = 'asa-hero-description';
    p.textContent = description;
    heroBody.appendChild(p);
  }

  hero.appendChild(heroBody);
  container.appendChild(hero);

  const widgetSection = document.createElement('section');
  widgetSection.className = 'asa-widget-wrap';
  widgetSection.setAttribute('aria-label', 'Ticket purchase');
  widgetSection.id = 'asa-widget';

  const widget = document.createElement('arena-tickets');
  widget.setAttribute('feed-token', data.feed_token);
  widget.setAttribute('event-id', data.event.id);
  widget.setAttribute('locale', toWidgetLocale(locale));
  if (options.apiBase) {
    widget.setAttribute('api-base', options.apiBase);
  }
  widgetSection.appendChild(widget);
  container.appendChild(widgetSection);

  if (options.resumingCheckout) {
    // Deferred to a microtask/frame so the element has been laid out.
    requestAnimationFrame(() => {
      widgetSection.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  }

  return widgetSection;
}

/** The artwork that stands for the WHOLE promoter page, or null when the
 * events do not agree on one.
 *
 * This is what turns a flat list of events into the owner's "tour" shape:
 * a promoter runs a season off one announcement image (the first live
 * client's six master classes all carry the same one), so that image
 * belongs at the top of the page, once, at full size — not repeated as a
 * thumbnail on every row, which made six different classes look like the
 * same thing six times. The moment two events carry DIFFERENT artwork the
 * page is not a tour any more and no poster is promoted: showing one
 * event's poster over another event's row would be a lie. Events with no
 * image of their own do not veto the shared one — a season where only the
 * first date was given the artwork is still one season. */
function sharedPosterURL(events: HostedPageEvent[]): string | null {
  let shared: string | null = null;
  for (const event of events) {
    const url = event.poster_url ?? event.image_url;
    if (!url) continue;
    if (shared === null) shared = url;
    else if (shared !== url) return null;
  }
  return shared;
}

/** Earliest start and latest end across the page's events, as a localized
 * range ("16–18 October 2026"), or "" when no event carries a date.
 * `formatRange` collapses the shared parts itself; the catch covers both
 * an unparseable date and an engine without `formatRange`. */
function formatSpanOfDates(events: HostedPageEvent[], locale: PageLocale): string {
  const starts: number[] = [];
  const ends: number[] = [];
  for (const event of events) {
    const start = event.first_session_at ? Date.parse(event.first_session_at) : NaN;
    if (!Number.isNaN(start)) starts.push(start);
    const endISO = event.last_session_at ?? event.first_session_at;
    const end = endISO ? Date.parse(endISO) : NaN;
    if (!Number.isNaN(end)) ends.push(end);
  }
  if (starts.length === 0) return '';
  const from = new Date(Math.min(...starts));
  const to = new Date(Math.max(...ends, ...starts));
  try {
    const fmt = new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'long', year: 'numeric' });
    return fmt.formatRange(from, to);
  } catch {
    return '';
  }
}

/** True once the event's last session is over. Such a date stays on the
 * page (the public feed has no cutoff) but must not offer tickets. */
function isPast(event: HostedPageEvent, now: number): boolean {
  const endISO = event.last_session_at ?? event.first_session_at;
  if (!endISO) return false;
  const end = Date.parse(endISO);
  return !Number.isNaN(end) && end < now;
}

interface DateParts {
  /** Day number, or "16–18" when the event spans several days. */
  day: string;
  month: string;
  /** Weekday and clock time — empty for a multi-day event, which has no
   * single start a buyer could rely on. */
  time: string;
}

function dateParts(event: HostedPageEvent, locale: PageLocale): DateParts | null {
  if (!event.first_session_at) return null;
  const tz = event.first_session_timezone ?? undefined;
  const start = new Date(event.first_session_at);
  const endISO = event.last_session_at ?? event.first_session_at;
  const end = new Date(endISO);
  try {
    const dayFmt = new Intl.DateTimeFormat(locale, { day: 'numeric', timeZone: tz });
    const monthFmt = new Intl.DateTimeFormat(locale, { month: 'short', timeZone: tz });
    const dayKeyFmt = new Intl.DateTimeFormat('en-CA', { dateStyle: 'short', timeZone: tz });
    const multiDay = dayKeyFmt.format(start) !== dayKeyFmt.format(end);

    const day = multiDay ? `${dayFmt.format(start)}–${dayFmt.format(end)}` : dayFmt.format(start);
    const month = monthFmt.format(start).replace(/\.$/, '');
    const time = multiDay
      ? ''
      : new Intl.DateTimeFormat(locale, {
          weekday: 'short',
          hour: '2-digit',
          minute: '2-digit',
          timeZone: tz,
        }).format(start);
    return { day, month, time };
  } catch {
    return null;
  }
}

/** One row of the date list: a date block, then what is on that date, then
 * the action. The whole row is a single link — one clear target beats a
 * title link plus a separate button for mouse, keyboard and screen-reader
 * users alike — with the tickets pill drawn inside it and hidden from the
 * accessibility tree, since the row's own text already names the target.
 *
 * Unlike the tour page this is modelled on, the row leads with the DATE
 * and then names the event: on that site every date of a tour is the same
 * show in a different city, so the city distinguished them; here the six
 * master classes have different titles and different teachers, so the
 * title has to be first-class. */
function renderDateRow(
  event: HostedPageEvent,
  orgSlug: string,
  locale: PageLocale,
  now: number,
): HTMLLIElement {
  const li = document.createElement('li');
  li.className = 'asa-date-item';

  const past = isPast(event, now);
  const a = document.createElement('a');
  a.className = past ? 'asa-date-row asa-date-row--past' : 'asa-date-row';
  a.href = `/${encodeURIComponent(orgSlug)}/${encodeURIComponent(event.slug)}${currentSearch()}`;

  const parts = dateParts(event, locale);
  if (parts) {
    const when = document.createElement('span');
    when.className = 'asa-date-row__when';

    const day = document.createElement('span');
    day.className = 'asa-date-row__day';
    day.textContent = parts.day;
    when.appendChild(day);

    const month = document.createElement('span');
    month.className = 'asa-date-row__month';
    month.textContent = parts.month;
    when.appendChild(month);

    if (parts.time) {
      const time = document.createElement('span');
      time.className = 'asa-date-row__time';
      time.textContent = parts.time;
      when.appendChild(time);
    }

    a.appendChild(when);
  }

  const what = document.createElement('span');
  what.className = 'asa-date-row__what';

  const title = document.createElement('span');
  title.className = 'asa-date-row__title';
  title.textContent = event.title;
  what.appendChild(title);

  if (event.venue_names.length > 0) {
    const venue = document.createElement('span');
    venue.className = 'asa-date-row__venue';
    venue.textContent = event.venue_names.join(', ');
    what.appendChild(venue);
  }

  a.appendChild(what);

  const cta = document.createElement('span');
  cta.className = past ? 'asa-button asa-date-row__cta asa-button--ghost' : 'asa-button asa-date-row__cta';
  cta.setAttribute('aria-hidden', 'true');
  cta.textContent = past ? t(locale).eventPast : t(locale).ticketsCta;
  a.appendChild(cta);

  li.appendChild(a);
  return li;
}

/**
 * renderPromoterPage mounts the page in the shape of a tour: the season's
 * one poster at the top, whole and uncropped, and beneath it the list of
 * dates a buyer picks from — or the localized empty state when the org has
 * none. Backs `/{org_slug}`.
 */
export function renderPromoterPage(container: HTMLElement, data: HostedPromoterPageResponse, locale: PageLocale): void {
  clear(container);
  const strings = t(locale);
  const poster = sharedPosterURL(data.events);

  const header = document.createElement('section');
  header.className = poster ? 'asa-promoter-header asa-promoter-header--poster' : 'asa-promoter-header';

  if (poster) {
    const img = document.createElement('img');
    img.className = 'asa-promoter-poster';
    img.src = poster;
    img.alt = data.org.name;
    header.appendChild(img);
  }

  const headerText = document.createElement('div');
  headerText.className = 'asa-promoter-header__text';

  if (data.org.logo_url) {
    const logo = document.createElement('img');
    logo.className = 'asa-promoter-logo';
    logo.src = data.org.logo_url;
    logo.alt = data.org.name;
    headerText.appendChild(logo);
  }

  const h1 = document.createElement('h1');
  h1.className = 'asa-promoter-title';
  h1.textContent = data.org.name;
  headerText.appendChild(h1);

  const span = formatSpanOfDates(data.events, locale);
  if (span) {
    const range = document.createElement('p');
    range.className = 'asa-promoter-range';
    range.textContent = span;
    headerText.appendChild(range);
  }

  // The tour page this is modelled on also carries a "choose a date ↓"
  // button here, because its hero is a full-bleed 88vh image and the list
  // is genuinely far below. Ours is a bounded poster with the labelled
  // list immediately under it, so the button would only repeat the
  // heading a finger-width away. Left out on purpose.
  header.appendChild(headerText);
  container.appendChild(header);

  if (data.events.length === 0) {
    const empty = document.createElement('section');
    empty.className = 'asa-state asa-state--empty';

    const h2 = document.createElement('h2');
    h2.textContent = strings.promoterEmptyTitle;
    empty.appendChild(h2);

    const p = document.createElement('p');
    p.textContent = strings.promoterEmptyBody;
    empty.appendChild(p);

    container.appendChild(empty);
    return;
  }

  const section = document.createElement('section');
  section.className = 'asa-dates';
  section.id = 'asa-dates';

  if (data.events.length > 1) {
    const h2 = document.createElement('h2');
    h2.className = 'asa-dates__head';
    h2.textContent = strings.promoterPickDate;
    section.appendChild(h2);
  }

  const now = Date.now();
  const list = document.createElement('ul');
  list.className = 'asa-date-list';
  list.setAttribute('aria-label', `${data.org.name} — ${strings.promoterPickDate}`);
  for (const event of data.events) {
    list.appendChild(renderDateRow(event, data.org.slug, locale, now));
  }
  section.appendChild(list);
  container.appendChild(section);
}

/** Sets document-level chrome: <html lang/dir>, <title>, meta description,
 * and the footer copyright line. Called once locale is known (even before
 * the event resolves) and again once event or org data is available. */
export function applyDocumentChrome(
  locale: PageLocale,
  content?: { title: string; description?: string | null } | null,
): void {
  document.documentElement.lang = locale;
  document.documentElement.dir = isRtlLocale(locale) ? 'rtl' : 'ltr';

  if (content) {
    document.title = `${content.title} — Arena Sold Out`;
    if (content.description) {
      let metaEl = document.querySelector('meta[name="description"]');
      if (!metaEl) {
        metaEl = document.createElement('meta');
        metaEl.setAttribute('name', 'description');
        document.head.appendChild(metaEl);
      }
      metaEl.setAttribute('content', content.description);
    }
  }

  const footerText = document.getElementById('asa-footer-text');
  if (footerText) {
    footerText.textContent = `© ${new Date().getFullYear()} ${t(locale).footerRights}`;
  }
}
