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

/** sessionStorage key under which the widget keeps the checkout it is in
 * the middle of, scoped to the event it was mounted for.
 *
 * Duplicated from `apps/widget/src/lib/store.ts` (`CHECKOUT_TOKEN_KEY` +
 * `checkoutTokenKey`) on purpose: the widget ships as its own bundle and
 * this page cannot import from it. Keep the two in step — the coupling is
 * what lets the page find which row a buyer was paying for when Stripe
 * sends them back here, since the return URL carries no event id. */
const WIDGET_CHECKOUT_TOKEN_PREFIX = 'arena_checkout_token:';

/** The event on this page the buyer has a checkout open for, or null.
 *
 * Stripe returns a buyer to the page they left, which is the whole date
 * list — not the row they were buying from. Without this the buyer lands
 * on a list of closed rows and nothing tells them the payment went
 * through, which is exactly what happened to the first real purchase. */
export function eventIDWithOpenCheckout(
  events: HostedPageEvent[],
  storage: Storage | null,
): string | null {
  if (!storage) return null;
  for (const event of events) {
    try {
      if (storage.getItem(WIDGET_CHECKOUT_TOKEN_PREFIX + event.id)) return event.id;
    } catch {
      // Private mode, blocked site data: no resume, just the plain list.
      return null;
    }
  }
  return null;
}

/** Takes `checkout_token` out of the page URL, returning what it held.
 *
 * Stripe returns a buyer to `…/{org}?checkout_token=…`, and the widget
 * reads that parameter itself. On a single-event page that is right. On
 * the date list it is not: the token names a checkout, not an event, so
 * EVERY row that opened afterwards mounted a widget that found it and
 * showed that order — a buyer who had just paid saw "payment successful"
 * on every other master class and could not buy a second one. Reloading
 * did not help, because the token was still in the address.
 *
 * So the list consumes it once, before any widget mounts, and the widget
 * resumes from the copy it stored under the event it belongs to. History
 * is replaced rather than pushed: the buyer must not be able to go "back"
 * into the stale URL. */
export function consumeCheckoutTokenFromURL(loc: Location, history: History): string | null {
  try {
    const url = new URL(loc.href);
    const token = url.searchParams.get('checkout_token');
    if (!token) return null;
    url.searchParams.delete('checkout_token');
    history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`);
    return token;
  } catch {
    return null;
  }
}

/** Everything a date row needs to open its own ticket picker in place. */
export interface PromoterPageOptions {
  apiBase: string;
  /** Resolves one event's hosted page — the promoter response carries no
   * feed token, and the widget cannot be mounted without one. Injected so
   * the renderer stays free of network code (and testable). */
  resolveEvent: (eventSlug: string) => Promise<HostedPageResponse>;
  /** Id of the event whose row should open by itself and scroll into
   * view — a buyer coming back from the payment page. */
  openEventID?: string | null;
}

/** One row of the date list: a date block, then what is on that date, then
 * the action — and, once pressed, the ticket picker itself unfolded
 * directly underneath.
 *
 * The row used to be a link to a per-event page, which is where the buyer
 * chose a quantity. That page is gone from the flow: a promoter's date
 * list IS the shop, and making someone load a second page to say "two,
 * please" loses buyers for nothing. Direct links to /{org}/{event} still
 * work and still render the full page — the list simply no longer routes
 * through it.
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
  options: PromoterPageOptions,
): HTMLLIElement {
  const li = document.createElement('li');
  li.className = 'asa-date-item';

  const past = isPast(event, now);
  // A past date keeps its link to the event page — there is nothing to
  // pick, and the page is where a buyer checks what they attended.
  const a = document.createElement(past ? 'a' : 'button') as HTMLElement;
  a.className = past ? 'asa-date-row asa-date-row--past' : 'asa-date-row';
  if (past) {
    (a as HTMLAnchorElement).href = `/${encodeURIComponent(orgSlug)}/${encodeURIComponent(event.slug)}${currentSearch()}`;
  } else {
    (a as HTMLButtonElement).type = 'button';
    a.setAttribute('aria-expanded', 'false');
  }

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
  if (past) return li;

  const panel = document.createElement('div');
  panel.className = 'asa-date-panel';
  panel.id = `asa-panel-${event.id}`;
  panel.hidden = true;
  a.setAttribute('aria-controls', panel.id);
  li.appendChild(panel);

  let mounted = false;
  a.addEventListener('click', () => {
    const open = a.getAttribute('aria-expanded') === 'true';
    if (open) {
      a.setAttribute('aria-expanded', 'false');
      panel.hidden = true;
      cta.textContent = t(locale).ticketsCta;
      return;
    }
    // One picker at a time: two open carts on one page means two live
    // seat holds and a buyer wondering which total is theirs.
    collapseOpenRows(li);
    a.setAttribute('aria-expanded', 'true');
    panel.hidden = false;
    cta.textContent = t(locale).hideCta;
    if (!mounted) {
      mounted = true;
      void mountTicketPicker(panel, event, locale, options);
    }
  });

  return li;
}

/** Closes every other open row in the same list. */
function collapseOpenRows(except: HTMLLIElement): void {
  const list = except.parentElement;
  if (!list) return;
  for (const item of Array.from(list.children)) {
    if (item === except) continue;
    const toggle = item.querySelector('.asa-date-row[aria-expanded="true"]');
    if (toggle instanceof HTMLElement) toggle.click();
  }
}

/** Fills an open row's panel: the event's own words, then the widget.
 * The feed token comes from the per-event endpoint, which is why this is
 * fetched on first open rather than rendered upfront — mounting six
 * widgets a buyer may never touch would cost six requests and six carts. */
async function mountTicketPicker(
  panel: HTMLElement,
  event: HostedPageEvent,
  locale: PageLocale,
  options: PromoterPageOptions,
): Promise<void> {
  const strings = t(locale);
  clear(panel);

  const status = document.createElement('p');
  status.className = 'asa-loading';
  status.setAttribute('role', 'status');
  status.textContent = strings.loading;
  panel.appendChild(status);

  let data: HostedPageResponse;
  try {
    data = await options.resolveEvent(event.slug);
  } catch {
    clear(panel);
    const failed = document.createElement('p');
    failed.className = 'asa-date-panel__error';
    failed.textContent = strings.errorBody;
    panel.appendChild(failed);

    const retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'asa-button';
    retry.textContent = strings.errorRetry;
    retry.addEventListener('click', () => {
      void mountTicketPicker(panel, event, locale, options);
    });
    panel.appendChild(retry);
    return;
  }

  clear(panel);

  // What the event is about is the one thing the row header does NOT say,
  // so it stays. The artwork and the date do not: the season's poster is
  // at the top of the page and the date is in the row itself, and showing
  // either again inside the row is what made the old event page feel like
  // a pointless extra step.
  const description = data.event.short_description ?? data.event.description;
  if (description) {
    const p = document.createElement('p');
    p.className = 'asa-date-panel__description';
    p.textContent = description;
    panel.appendChild(p);
  }

  const widget = document.createElement('arena-tickets');
  widget.setAttribute('feed-token', data.feed_token);
  widget.setAttribute('event-id', data.event.id);
  widget.setAttribute('cover', 'hidden');
  widget.setAttribute('sessions', 'hidden');
  widget.setAttribute('locale', toWidgetLocale(locale));
  if (options.apiBase) {
    widget.setAttribute('api-base', options.apiBase);
  }
  panel.appendChild(widget);
}

/**
 * renderPromoterPage mounts the page in the shape of a tour: the season's
 * one poster at the top, whole and uncropped, and beneath it the list of
 * dates a buyer picks from — or the localized empty state when the org has
 * none. Backs `/{org_slug}`.
 */
export function renderPromoterPage(
  container: HTMLElement,
  data: HostedPromoterPageResponse,
  locale: PageLocale,
  options: PromoterPageOptions,
): void {
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
    list.appendChild(renderDateRow(event, data.org.slug, locale, now, options));
  }
  section.appendChild(list);
  container.appendChild(section);

  // A buyer returning from the payment page: open their row for them and
  // put it on screen, so the order's outcome is the first thing they see
  // rather than a list that behaves as if nothing happened.
  if (options.openEventID) {
    const row = list.querySelector(`.asa-date-row[aria-controls="asa-panel-${cssEscape(options.openEventID)}"]`);
    if (row instanceof HTMLElement) {
      row.click();
      requestAnimationFrame(() => {
        row.scrollIntoView({ behavior: 'smooth', block: 'center' });
      });
    }
  }
}

/** Escapes a value for use inside an attribute selector. Event ids are
 * UUIDs today, but a selector built from data must never be able to break
 * out of it. */
function cssEscape(value: string): string {
  const api = (window as unknown as { CSS?: { escape?: (s: string) => string } }).CSS;
  return api?.escape ? api.escape(value) : value.replace(/["\\]/g, '\\$&');
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
