import type { PageLocale } from './locale.ts';
import { isRtlLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { HostedPageContactForm } from './api.ts';

/**
 * contact.ts — the "Write to the organizer" button and its form.
 *
 * The page never shows the organizer's e-mail (owner decision 2026-10-13):
 * a buyer writes through this form, the API stores the message and the
 * platform mails it to the organizer, with the buyer as Reply-To. The form
 * is protected by Cloudflare Turnstile when the API hands out a site key.
 *
 * Everything is built with textContent (never innerHTML). The Turnstile
 * script is loaded only when the form is opened, so a visitor who never
 * opens it loads nothing from Cloudflare.
 */

export interface ContactContext {
  apiBase: string;
  /** The first path segment of the page: the organization's or the
   * promoter's slug. */
  slug: string;
  /** The event page's feed token; empty on the promoter page. */
  feedToken?: string;
  locale: PageLocale;
  form: HostedPageContactForm | null | undefined;
}

/** Cloudflare Turnstile's global, as far as this module uses it. */
interface TurnstileApi {
  render(el: HTMLElement, opts: Record<string, unknown>): string;
  reset(id?: string): void;
  remove?(id?: string): void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

const TURNSTILE_SRC = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';
let turnstileLoad: Promise<TurnstileApi | null> | null = null;

/** Loads the Turnstile script once. Resolves null when it cannot load. */
export function loadTurnstile(): Promise<TurnstileApi | null> {
  if (window.turnstile) return Promise.resolve(window.turnstile);
  if (turnstileLoad) return turnstileLoad;
  turnstileLoad = new Promise((resolve) => {
    const s = document.createElement('script');
    s.src = TURNSTILE_SRC;
    s.async = true;
    s.defer = true;
    s.onload = () => resolve(window.turnstile ?? null);
    s.onerror = () => {
      turnstileLoad = null;
      resolve(null);
    };
    document.head.appendChild(s);
  });
  return turnstileLoad;
}

/** The form is offered only when the API says so. */
export function contactAvailable(form: HostedPageContactForm | null | undefined): boolean {
  return !!form && form.available === true;
}

/** Client-side checks, the same limits as the API. Returns the key of the
 * first problem, or '' when the form may be sent. */
export function validateContact(v: { name: string; email: string; message: string; consent: boolean }):
  '' | 'name' | 'email' | 'message' | 'consent' {
  const name = v.name.trim();
  if (!name || [...name].length > 100) return 'name';
  const email = v.email.trim();
  if (!/^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/.test(email) || email.length > 254) return 'email';
  const msg = [...v.message.trim()].length;
  if (msg < 10 || msg > 2000) return 'message';
  if (!v.consent) return 'consent';
  return '';
}

/** POST /v1/public/organizer-contact/{slug}. Answers the error code of a
 * refusal ('' on success). */
export async function sendContact(
  ctx: ContactContext,
  body: { name: string; email: string; message: string; consent: boolean; turnstile_token: string },
): Promise<string> {
  const base = ctx.apiBase.replace(/\/$/, '');
  let res: Response;
  try {
    res = await fetch(`${base}/v1/public/organizer-contact/${encodeURIComponent(ctx.slug)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ ...body, feed_token: ctx.feedToken ?? '', locale: ctx.locale }),
    });
  } catch {
    return 'network';
  }
  if (res.status === 202) return '';
  try {
    const data = (await res.json()) as { error?: { code?: string } };
    return data.error?.code ?? `http_${res.status}`;
  } catch {
    return `http_${res.status}`;
  }
}

function field(label: string, input: HTMLInputElement | HTMLTextAreaElement, id: string): HTMLElement {
  const wrap = document.createElement('div');
  wrap.className = 'asa-contact__field';
  const l = document.createElement('label');
  l.htmlFor = id;
  l.textContent = label;
  input.id = id;
  wrap.appendChild(l);
  wrap.appendChild(input);
  return wrap;
}

/** Maps an API error code onto the sentence the buyer reads. */
function errorText(code: string, locale: PageLocale): string {
  const s = t(locale);
  switch (code) {
    case 'contact.invalid_name':
    case 'name':
      return s.contactErrName;
    case 'contact.invalid_email':
    case 'email':
      return s.contactErrEmail;
    case 'contact.invalid_message':
    case 'contact.too_large':
    case 'message':
      return s.contactErrMessage;
    case 'contact.consent_required':
    case 'consent':
      return s.contactErrConsent;
    case 'contact.captcha_failed':
    case 'captcha':
      return s.contactErrCaptcha;
    case 'contact.rate_limited':
      return s.contactErrRate;
    default:
      return s.contactErrGeneric;
  }
}

let seq = 0;

/** Opens the form as a modal dialog. Returns the overlay element. */
export function openContactForm(ctx: ContactContext): HTMLElement {
  const s = t(ctx.locale);
  const n = ++seq;
  const overlay = document.createElement('div');
  overlay.className = 'asa-contact';
  const dialog = document.createElement('div');
  dialog.className = 'asa-contact__dialog';
  dialog.setAttribute('role', 'dialog');
  dialog.setAttribute('aria-modal', 'true');
  dialog.setAttribute('aria-labelledby', `asa-contact-title-${n}`);
  dialog.setAttribute('dir', isRtlLocale(ctx.locale) ? 'rtl' : 'ltr');
  overlay.appendChild(dialog);

  const title = document.createElement('h2');
  title.id = `asa-contact-title-${n}`;
  title.className = 'asa-contact__title';
  title.textContent = s.contactTitle;
  dialog.appendChild(title);

  const intro = document.createElement('p');
  intro.className = 'asa-contact__intro';
  intro.textContent = s.contactIntro;
  dialog.appendChild(intro);

  const form = document.createElement('form');
  form.className = 'asa-contact__form';
  form.noValidate = true;
  dialog.appendChild(form);

  const name = document.createElement('input');
  name.type = 'text';
  name.name = 'name';
  name.autocomplete = 'name';
  name.maxLength = 100;
  const email = document.createElement('input');
  email.type = 'email';
  email.name = 'email';
  email.autocomplete = 'email';
  email.maxLength = 254;
  email.setAttribute('dir', 'ltr');
  const message = document.createElement('textarea');
  message.name = 'message';
  message.rows = 5;
  message.maxLength = 2000;
  form.appendChild(field(s.contactName, name, `asa-contact-name-${n}`));
  form.appendChild(field(s.contactEmail, email, `asa-contact-email-${n}`));
  form.appendChild(field(s.contactMessage, message, `asa-contact-message-${n}`));

  const consentWrap = document.createElement('label');
  consentWrap.className = 'asa-contact__consent';
  const consent = document.createElement('input');
  consent.type = 'checkbox';
  consent.name = 'consent';
  consentWrap.appendChild(consent);
  consentWrap.appendChild(document.createTextNode(' ' + s.contactConsent));
  form.appendChild(consentWrap);

  const captcha = document.createElement('div');
  captcha.className = 'asa-contact__captcha';
  form.appendChild(captcha);

  const status = document.createElement('p');
  status.className = 'asa-contact__status';
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  form.appendChild(status);

  const actions = document.createElement('div');
  actions.className = 'asa-contact__actions';
  const send = document.createElement('button');
  send.type = 'submit';
  send.className = 'asa-contact__send';
  send.textContent = s.contactSend;
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.className = 'asa-contact__cancel';
  cancel.textContent = s.contactCancel;
  actions.appendChild(send);
  actions.appendChild(cancel);
  form.appendChild(actions);

  let token = '';
  let widgetId: string | undefined;
  const siteKey = (ctx.form?.turnstile_site_key ?? '').trim();
  if (siteKey) {
    void loadTurnstile().then((ts) => {
      if (!ts || !overlay.isConnected) return;
      widgetId = ts.render(captcha, {
        sitekey: siteKey,
        // The server refuses a token of any other action (hcontact.TurnstileAction).
        action: 'contact',
        language: ctx.locale,
        callback: (v: string) => {
          token = v;
        },
        'expired-callback': () => {
          token = '';
        },
      });
    });
  }

  const close = () => {
    if (widgetId && window.turnstile?.remove) window.turnstile.remove(widgetId);
    document.removeEventListener('keydown', onKey);
    overlay.remove();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') close();
  };
  document.addEventListener('keydown', onKey);
  cancel.addEventListener('click', close);
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const values = { name: name.value, email: email.value, message: message.value, consent: consent.checked };
    const problem = validateContact(values);
    if (problem) {
      status.textContent = errorText(problem, ctx.locale);
      return;
    }
    if (siteKey && !token) {
      status.textContent = errorText('captcha', ctx.locale);
      return;
    }
    send.disabled = true;
    status.textContent = s.contactSending;
    void sendContact(ctx, { ...values, turnstile_token: token }).then((code) => {
      send.disabled = false;
      if (!code) {
        form.remove();
        intro.textContent = s.contactSent;
        const done = document.createElement('button');
        done.type = 'button';
        done.className = 'asa-contact__cancel';
        done.textContent = s.contactClose;
        done.addEventListener('click', close);
        dialog.appendChild(done);
        done.focus();
        return;
      }
      status.textContent = errorText(code, ctx.locale);
      if (code === 'contact.captcha_failed' && widgetId && window.turnstile) {
        token = '';
        window.turnstile.reset(widgetId);
      }
    });
  });

  document.body.appendChild(overlay);
  name.focus();
  return overlay;
}

/** The "Write to the organizer" button, or null when the page has no form. */
export function renderContactButton(ctx: ContactContext, className = 'asa-contact-btn'): HTMLButtonElement | null {
  if (!contactAvailable(ctx.form) || !ctx.slug) return null;
  const b = document.createElement('button');
  b.type = 'button';
  b.className = className;
  b.textContent = t(ctx.locale).contactButton;
  b.addEventListener('click', () => {
    openContactForm(ctx);
  });
  return b;
}

const FOOTER_ID = 'asa-footer-contact';

/** Puts the button in the page footer (or clears it), so every page with a
 * form offers it, whether or not the seller block is shown. */
export function renderContactFooter(ctx: ContactContext): void {
  const existing = document.getElementById(FOOTER_ID);
  if (existing) existing.remove();
  const btn = renderContactButton(ctx, 'asa-contact-btn asa-contact-btn--footer');
  if (!btn) return;
  const inner = document.querySelector('.asa-footer__in');
  if (!inner) return;
  const p = document.createElement('p');
  p.id = FOOTER_ID;
  p.className = 'asa-footer__contact';
  p.appendChild(btn);
  const text = document.getElementById('asa-footer-text');
  inner.insertBefore(p, text ?? null);
}
