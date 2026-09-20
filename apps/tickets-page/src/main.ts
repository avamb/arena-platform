import { parsePath } from './lib/route.ts';
import { resolveLocale } from './lib/locale.ts';
import { applyDocumentChrome, eventIDWithOpenCheckout, renderError, renderEvent, renderLoading, renderNotFound, renderPromoterPage } from './lib/render.ts';
import { ApiError, fetchHostedPage, fetchPromoterPage } from './lib/api.ts';

/** Resolved at build time by Vite from the VITE_API_BASE_URL build arg
 * (see Dockerfile / .env). Empty string falls back to same-origin relative
 * requests, matching the widget's own apiBase convention. */
const API_BASE: string = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.trim() ?? '';

/**
 * loadWidgetScript injects the <arena-tickets> custom-element bundle as a
 * plain runtime <script> tag rather than a static index.html reference:
 * the file is served from THIS SAME nginx container at
 * /widget/v1/arena-tickets.js (copied in from the widget build stage — see
 * Dockerfile), which is not part of this app's own Vite module graph and
 * does not exist on disk at build time. Idempotent — safe to call once.
 */
function loadWidgetScript(): void {
  if (document.querySelector('script[data-arena-widget]')) return;
  const script = document.createElement('script');
  script.type = 'module';
  script.src = '/widget/v1/arena-tickets.js';
  script.dataset.arenaWidget = 'true';
  document.head.appendChild(script);
}

async function main(): Promise<void> {
  const mainEl = document.getElementById('asa-main');
  if (!mainEl) return;

  const locale = resolveLocale(
    window.location.search,
    navigator.languages ?? [navigator.language],
    readRememberedLang(),
  );
  rememberExplicitLang(window.location.search);
  applyDocumentChrome(locale, null);

  const route = parsePath(window.location.pathname);
  if (!route) {
    renderNotFound(mainEl, locale);
    return;
  }

  if (route.kind === 'promoter') {
    // The date rows open the ticket picker in place, so the widget bundle
    // is needed here too — not only on a per-event page.
    loadWidgetScript();
    const loadPromoter = async (): Promise<void> => {
      renderLoading(mainEl, locale);
      try {
        const data = await fetchPromoterPage(API_BASE, route.orgSlug);
        applyDocumentChrome(locale, { title: data.org.name });
        renderPromoterPage(mainEl, data, locale, {
          apiBase: API_BASE,
          resolveEvent: (eventSlug) => fetchHostedPage(API_BASE, route.orgSlug, eventSlug),
          // Stripe returns the buyer to this list, not to the row they
          // bought from, and the return URL carries no event id — so the
          // row is found by the checkout the widget left behind.
          openEventID: eventIDWithOpenCheckout(data.events, safeSessionStorage()),
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          renderNotFound(mainEl, locale);
          return;
        }
        renderError(mainEl, locale, () => {
          void loadPromoter();
        });
      }
    };
    await loadPromoter();
    return;
  }

  // route.kind === 'event' from here on.
  loadWidgetScript();
  const resumingCheckout = new URLSearchParams(window.location.search).has('checkout_token');

  const loadEvent = async (): Promise<void> => {
    renderLoading(mainEl, locale);
    try {
      const data = await fetchHostedPage(API_BASE, route.orgSlug, route.eventSlug);
      applyDocumentChrome(locale, { title: data.event.title, description: data.event.short_description ?? data.event.description });
      renderEvent(mainEl, data, locale, { apiBase: API_BASE, resumingCheckout });
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        renderNotFound(mainEl, locale);
        return;
      }
      renderError(mainEl, locale, () => {
        void loadEvent();
      });
    }
  };

  await loadEvent();
}

void main();

/** sessionStorage, or null where it throws (private mode, blocked site
 * data). Reading it must never be able to break the page. */
function safeSessionStorage(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

const LANG_STORAGE_KEY = 'arena.tickets.lang';

/** Storage can throw (private mode, blocked site data) — never let that break the page. */
function readRememberedLang(): string | null {
  try {
    return window.localStorage.getItem(LANG_STORAGE_KEY);
  } catch {
    return null;
  }
}

/** Remembers only an EXPLICIT `?lang=` choice, never the browser default. */
function rememberExplicitLang(search: string): void {
  const lang = new URLSearchParams(search).get('lang');
  if (!lang) return;
  try {
    window.localStorage.setItem(LANG_STORAGE_KEY, lang);
  } catch {
    /* ignore */
  }
}
