/**
 * captcha.ts - Cloudflare Turnstile on the checkout form. Only used while the shop's
 * public answer carries `captcha_site_key`; without a key the form works as before.
 * The script is loaded on demand from challenges.cloudflare.com (allowed by the CSP in
 * security-headers.conf.template) and the widget is rendered explicitly into #checkout-captcha.
 * A Turnstile token is single-use, so every submit consumes it and the widget is reset.
 */

export const TURNSTILE_SRC = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';

interface TurnstileApi {
  render(el: HTMLElement, opts: Record<string, unknown>): string;
  reset(id?: string): void;
  remove(id?: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

let loading: Promise<TurnstileApi | null> | null = null;
let widgetId: string | undefined;
let token = '';

/** Loads the Turnstile script once. Resolves null when it cannot be loaded (blocked, offline). */
export function loadTurnstile(): Promise<TurnstileApi | null> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (!loading) {
    loading = new Promise((resolve) => {
      const s = document.createElement('script');
      s.src = TURNSTILE_SRC;
      s.async = true;
      s.onload = () => resolve(window.turnstile ?? null);
      s.onerror = () => {
        loading = null;
        resolve(null);
      };
      document.head.appendChild(s);
    });
  }
  return loading;
}

/** The token the widget handed out, or one it put into the form; '' while the visitor has not passed. */
export function captchaToken(form?: HTMLFormElement | null): string {
  if (token) return token;
  const hidden = form?.elements.namedItem('cf-turnstile-response') as HTMLInputElement | null;
  return hidden?.value ?? '';
}

/** Draws the widget into `host` (replacing an earlier one). Returns false when the script is unavailable. */
export async function mountCaptcha(host: HTMLElement, siteKey: string): Promise<boolean> {
  // A token the visitor already earned survives a redraw of the form (a validation error): it is good for five minutes.
  removeWidget();
  const api = await loadTurnstile();
  if (!api || !host.isConnected) return false;
  widgetId = api.render(host, {
    sitekey: siteKey,
    language: 'ru',
    callback: (t: string) => {
      token = t;
    },
    'expired-callback': () => {
      token = '';
    },
    'error-callback': () => {
      token = '';
    },
  });
  return true;
}

/** The token was used (or refused): ask the visitor for a fresh one. */
export function resetCaptcha(): void {
  token = '';
  try {
    if (widgetId !== undefined) window.turnstile?.reset(widgetId);
  } catch {
    /* the widget is gone with its page */
  }
}

function removeWidget(): void {
  try {
    if (widgetId !== undefined) window.turnstile?.remove(widgetId);
  } catch {
    /* already removed */
  }
  widgetId = undefined;
}

/** The form is gone: forget the widget and its token. */
export function unmountCaptcha(): void {
  removeWidget();
  token = '';
}
