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
  /** The event page's slug; empty on a landing page. The API sends a
   * message about an event of a promoter to the organization's owner. */
  eventSlug?: string;
  locale: PageLocale;
  form: HostedPageContactForm | null | undefined;
  /** The organizer's public phone from the page's seller block, if any:
   * offered when a message cannot be sent. */
  phone?: string;
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
      body: JSON.stringify({ ...body, feed_token: ctx.feedToken ?? '', event_slug: ctx.eventSlug ?? '', locale: ctx.locale }),
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

/** Maps an API error code onto the sentence the buyer reads. A failure the
 * buyer cannot fix also says how else to reach the organizer: the page's
 * public phone when it has one. */
export function errorText(code: string, locale: PageLocale, phone?: string): string {
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
    default: {
      const tel = (phone ?? '').trim();
      // The phone stays left-to-right inside a Hebrew sentence.
      if (tel) return `${s.contactErrGeneric} ${s.contactErrCall} \u2066${tel}\u2069`;
      return `${s.contactErrGeneric} ${s.contactErrElsewhere}`;
    }
  }
}

/** The form field an API refusal is about, if it is about one. */
function fieldOfError(code: string): 'name' | 'email' | 'message' | 'consent' | '' {
  switch (code) {
    case 'contact.invalid_name':
      return 'name';
    case 'contact.invalid_email':
      return 'email';
    case 'contact.invalid_message':
    case 'contact.too_large':
      return 'message';
    case 'contact.consent_required':
      return 'consent';
    default:
      return '';
  }
}

let seq = 0;

/** What the buyer typed, kept per page while the tab lives: closing the form
 * (Esc, Cancel, a click beside it) never throws the text away. Cleared once
 * the message is sent. The consent is not kept: the buyer gives it again
 * each time the form opens. */
interface ContactDraft {
  name: string;
  email: string;
  message: string;
}
const drafts = new Map<string, ContactDraft>();

/** The draft kept for a page, if any (exported for tests). */
export function contactDraft(slug: string): ContactDraft | undefined {
  return drafts.get(slug);
}

/** Set on <body> while the dialog is open (style.css stops the page scrolling). */
export const SCROLL_LOCK_CLASS = 'asa-contact-open';

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), textarea:not([disabled]), iframe, [tabindex]:not([tabindex="-1"])';

/** Opens the form as a modal dialog. Returns the overlay element. */
export function openContactForm(ctx: ContactContext): HTMLElement {
  const s = t(ctx.locale);
  const n = ++seq;
  // Focus goes back where it was when the dialog closes.
  const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
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

  const note = document.createElement('p');
  note.className = 'asa-contact__note';
  note.appendChild(document.createTextNode(s.contactConsentNote));
  const privacyUrl = (ctx.form?.privacy_url ?? '').trim();
  if (/^https:\/\//i.test(privacyUrl)) {
    const link = document.createElement('a');
    link.href = privacyUrl;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.textContent = s.contactPrivacy;
    note.appendChild(document.createTextNode(' '));
    note.appendChild(link);
  }
  form.appendChild(note);

  const captcha = document.createElement('div');
  captcha.className = 'asa-contact__captcha';
  form.appendChild(captcha);

  const status = document.createElement('p');
  status.className = 'asa-contact__status';
  status.id = `asa-contact-status-${n}`;
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  form.appendChild(status);

  // A field the buyer must correct is marked for screen readers and points
  // at the sentence that says what to do.
  const inputs = { name, email, message, consent } as const;
  const clearInvalid = () => {
    for (const el of Object.values(inputs)) {
      el.removeAttribute('aria-invalid');
      el.removeAttribute('aria-describedby');
    }
  };
  const markInvalid = (key: keyof typeof inputs) => {
    clearInvalid();
    const el = inputs[key];
    el.setAttribute('aria-invalid', 'true');
    el.setAttribute('aria-describedby', status.id);
    el.focus();
  };

  const draft = drafts.get(ctx.slug);
  if (draft) {
    name.value = draft.name;
    email.value = draft.email;
    message.value = draft.message;
  }
  const saveDraft = () => {
    drafts.set(ctx.slug, { name: name.value, email: email.value, message: message.value });
  };
  form.addEventListener('input', () => {
    saveDraft();
    clearInvalid();
  });
  form.addEventListener('change', saveDraft);

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

  // The page under the dialog does not scroll while it is open.
  document.body.classList.add(SCROLL_LOCK_CLASS);
  const close = () => {
    if (widgetId && window.turnstile?.remove) window.turnstile.remove(widgetId);
    document.removeEventListener('keydown', onKey);
    overlay.remove();
    if (!document.querySelector('.asa-contact')) document.body.classList.remove(SCROLL_LOCK_CLASS);
    if (opener && opener.isConnected) opener.focus();
  };
  // Keyboard focus stays inside the dialog while it is open.
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      close();
      return;
    }
    if (e.key !== 'Tab') return;
    const items = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => el.isConnected);
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (e.shiftKey && (active === first || !dialog.contains(active))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (active === last || !dialog.contains(active))) {
      e.preventDefault();
      first.focus();
    }
  };
  document.addEventListener('keydown', onKey);
  cancel.addEventListener('click', close);
  // A click beside the dialog closes it; the typed text stays in the draft.
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });

  form.addEventListener('submit', (e) => {
    e.preventDefault();
    const values = { name: name.value, email: email.value, message: message.value, consent: consent.checked };
    const problem = validateContact(values);
    if (problem) {
      status.textContent = errorText(problem, ctx.locale);
      markInvalid(problem);
      return;
    }
    clearInvalid();
    if (siteKey && !token) {
      status.textContent = errorText('captcha', ctx.locale);
      return;
    }
    send.disabled = true;
    status.textContent = s.contactSending;
    void sendContact(ctx, { ...values, turnstile_token: token }).then((code) => {
      send.disabled = false;
      if (!code) {
        drafts.delete(ctx.slug);
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
      status.textContent = errorText(code, ctx.locale, ctx.phone);
      const bad = fieldOfError(code);
      if (bad) markInvalid(bad);
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
  // The seller block already offers the button: one is enough.
  if (document.querySelector('.asa-seller__write')) return;
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

/** The fragment ticket letters link: `<page>#contact` opens the form. */
export const CONTACT_HASH = '#contact';

/**
 * Opens the form when the page was reached through `#contact` (the link the
 * ticket letters print when the sales page shows no organizer e-mail). The
 * fragment is removed afterwards, so a reload or a later re-render does not
 * open it again. When the page shows no form, a short note says so instead
 * of leaving the buyer with nothing. Returns whether the form was opened.
 */
export function openContactFromHash(
  ctx: ContactContext,
  loc: Pick<Location, 'hash' | 'pathname' | 'search'> = window.location,
  hist: Pick<History, 'replaceState'> | null = window.history,
): boolean {
  if (loc.hash !== CONTACT_HASH) return false;
  try {
    hist?.replaceState(null, '', loc.pathname + loc.search);
  } catch {
    // A sandboxed frame may refuse; the form (or the note) still shows.
  }
  if (!contactAvailable(ctx.form) || !ctx.slug) {
    showContactUnavailable(ctx.locale);
    return false;
  }
  openContactForm(ctx);
  return true;
}

const NOTICE_ID = 'asa-contact-notice';

/** A short, dismissible note: the organizer cannot be written to from this
 * page right now. */
export function showContactUnavailable(locale: PageLocale): HTMLElement {
  document.getElementById(NOTICE_ID)?.remove();
  const box = document.createElement('div');
  box.id = NOTICE_ID;
  box.className = 'asa-contact-notice';
  box.setAttribute('role', 'status');
  box.setAttribute('dir', isRtlLocale(locale) ? 'rtl' : 'ltr');
  const text = document.createElement('p');
  text.textContent = t(locale).contactUnavailable;
  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'asa-contact-notice__close';
  close.textContent = t(locale).contactClose;
  close.addEventListener('click', () => box.remove());
  box.appendChild(text);
  box.appendChild(close);
  document.body.appendChild(box);
  return box;
}

/**
 * Where a `#contact` link goes when its event page no longer exists (taken
 * off sale, deleted): the organization's or promoter's own page, which offers
 * the same form. Null when the address is not a `#contact` link.
 */
export function contactFallbackURL(orgSlug: string, loc: Pick<Location, 'hash' | 'search'>): string | null {
  if (loc.hash !== CONTACT_HASH || !orgSlug) return null;
  return '/' + encodeURIComponent(orgSlug) + loc.search + CONTACT_HASH;
}
