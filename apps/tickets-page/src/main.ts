import { parsePath } from './lib/route.ts';
import { resolveLocale } from './lib/locale.ts';
import { applyDocumentChrome, renderError, renderEvent, renderLoading, renderNotFound } from './lib/render.ts';
import { ApiError, fetchHostedPage } from './lib/api.ts';

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
  loadWidgetScript();
  const mainEl = document.getElementById('asa-main');
  if (!mainEl) return;

  const locale = resolveLocale(window.location.search, navigator.languages ?? [navigator.language]);
  applyDocumentChrome(locale, null);

  const route = parsePath(window.location.pathname);
  if (!route) {
    renderNotFound(mainEl, locale);
    return;
  }

  const resumingCheckout = new URLSearchParams(window.location.search).has('checkout_token');

  const load = async (): Promise<void> => {
    renderLoading(mainEl, locale);
    try {
      const data = await fetchHostedPage(API_BASE, route.orgSlug, route.eventSlug);
      applyDocumentChrome(locale, data.event);
      renderEvent(mainEl, data, locale, { apiBase: API_BASE, resumingCheckout });
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        renderNotFound(mainEl, locale);
        return;
      }
      renderError(mainEl, locale, () => {
        void load();
      });
    }
  };

  await load();
}

void main();
