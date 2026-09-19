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

/** Builds one <li> card for the promoter page's event grid: date/time
 * FIRST and prominent, then title, short description, venue, an optional
 * poster thumbnail, and a "tickets" link to the per-event page. The whole
 * card is one link (a single clear target beats a title link plus a
 * separate button for both mouse and keyboard/screen-reader users). */
function renderEventCard(event: HostedPageEvent, orgSlug: string, locale: PageLocale): HTMLLIElement {
  const li = document.createElement('li');
  li.className = 'asa-card-item';

  const a = document.createElement('a');
  a.className = 'asa-card';
  a.href = `/${encodeURIComponent(orgSlug)}/${encodeURIComponent(event.slug)}${currentSearch()}`;

  const posterURL = event.poster_url ?? event.image_url;
  if (posterURL) {
    const img = document.createElement('img');
    img.className = 'asa-card-image';
    img.src = posterURL;
    img.alt = '';
    img.loading = 'lazy';
    a.appendChild(img);
  }

  const body = document.createElement('div');
  body.className = 'asa-card-body';

  if (event.first_session_at) {
    const when = document.createElement('p');
    when.className = 'asa-card-datetime';
    when.textContent = formatSessionDateTime(event.first_session_at, locale, event.first_session_timezone);
    body.appendChild(when);
  }

  const title = document.createElement('h2');
  title.className = 'asa-card-title';
  title.textContent = event.title;
  body.appendChild(title);

  if (event.venue_names.length > 0) {
    const venue = document.createElement('p');
    venue.className = 'asa-card-venue';
    venue.textContent = event.venue_names.join(', ');
    body.appendChild(venue);
  }

  const description = event.short_description ?? event.description;
  if (description) {
    const desc = document.createElement('p');
    desc.className = 'asa-card-description';
    desc.textContent = description;
    body.appendChild(desc);
  }

  const cta = document.createElement('span');
  cta.className = 'asa-button asa-card-cta';
  cta.setAttribute('aria-hidden', 'true');
  cta.textContent = t(locale).ticketsCta;
  body.appendChild(cta);

  a.appendChild(body);
  li.appendChild(a);
  return li;
}

/**
 * renderPromoterPage mounts the org header (name/logo) and a responsive
 * grid of event cards, one per currently-visible event, or the localized
 * empty state when the org has none. Backs `/{org_slug}`.
 */
export function renderPromoterPage(container: HTMLElement, data: HostedPromoterPageResponse, locale: PageLocale): void {
  clear(container);
  const strings = t(locale);

  const header = document.createElement('section');
  header.className = 'asa-promoter-header';

  if (data.org.logo_url) {
    const logo = document.createElement('img');
    logo.className = 'asa-promoter-logo';
    logo.src = data.org.logo_url;
    logo.alt = data.org.name;
    header.appendChild(logo);
  }

  const h1 = document.createElement('h1');
  h1.className = 'asa-promoter-title';
  h1.textContent = data.org.name;
  header.appendChild(h1);

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

  const list = document.createElement('ul');
  list.className = 'asa-event-grid';
  list.setAttribute('aria-label', `${data.org.name} — upcoming events`);
  for (const event of data.events) {
    list.appendChild(renderEventCard(event, data.org.slug, locale));
  }
  container.appendChild(list);
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
