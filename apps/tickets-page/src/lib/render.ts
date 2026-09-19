import type { SupportedLocale } from './locale.ts';
import { isRtlLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { HostedPageResponse } from './api.ts';

/** Clears a container's children (avoids innerHTML = '' churn semantics
 * differences and keeps this file free of innerHTML entirely). */
function clear(el: HTMLElement): void {
  while (el.firstChild) el.removeChild(el.firstChild);
}

export function renderLoading(container: HTMLElement, locale: SupportedLocale): void {
  clear(container);
  const p = document.createElement('p');
  p.className = 'asa-loading';
  p.setAttribute('role', 'status');
  p.textContent = t(locale).loading;
  container.appendChild(p);
}

export function renderNotFound(container: HTMLElement, locale: SupportedLocale): void {
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

export function renderError(container: HTMLElement, locale: SupportedLocale, onRetry: () => void): void {
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

function formatSessionDate(iso: string, locale: SupportedLocale): string {
  try {
    return new Intl.DateTimeFormat(locale, {
      dateStyle: 'long',
      timeStyle: 'short',
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
 * renderEvent mounts the hero (image/title/date/venue/description) and the
 * <arena-tickets> widget for a resolved hosted page. Returns the widget host
 * element so the caller can scroll it into view when resuming a checkout.
 */
export function renderEvent(
  container: HTMLElement,
  data: HostedPageResponse,
  locale: SupportedLocale,
  options: RenderEventOptions,
): HTMLElement {
  clear(container);

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
    metaParts.push(formatSessionDate(data.event.first_session_at, locale));
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
  widget.setAttribute('locale', locale);
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

/** Sets document-level chrome: <html lang/dir>, <title>, meta description,
 * and the footer copyright line. Called once locale is known (even before
 * the event resolves) and again once event data is available. */
export function applyDocumentChrome(locale: SupportedLocale, event?: HostedPageResponse['event'] | null): void {
  document.documentElement.lang = locale;
  document.documentElement.dir = isRtlLocale(locale) ? 'rtl' : 'ltr';

  if (event) {
    document.title = `${event.title} — Arena Sold Out`;
    const desc = event.short_description ?? event.description;
    if (desc) {
      let metaEl = document.querySelector('meta[name="description"]');
      if (!metaEl) {
        metaEl = document.createElement('meta');
        metaEl.setAttribute('name', 'description');
        document.head.appendChild(metaEl);
      }
      metaEl.setAttribute('content', desc);
    }
  }

  const footerText = document.getElementById('asa-footer-text');
  if (footerText) {
    footerText.textContent = `© ${new Date().getFullYear()} ${t(locale).footerRights}`;
  }
}
