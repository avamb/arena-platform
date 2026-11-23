import type { PageLocale } from './locale.ts';
import { isRtlLocale } from './locale.ts';
import { t } from './i18n.ts';

/**
 * tracking.ts — the cookie banner of a page whose organizer measures ads
 * (Meta, Google). It runs only when the API says `org.tracking_enabled`:
 * otherwise nothing is shown, stored or set. Nothing is set before a choice;
 * a stored choice hides the banner, the footer link reopens it.
 *
 * The choice is passed to every <arena-tickets> element through the
 * `tracking-consent` / `tracking-consent-version` attributes.
 */

export const CONSENT_KEY = 'arena_tracking_consent';
export const CONSENT_VERSION = 'tracking-banner-2026-11-23';
export const COOKIE_MAX_AGE_SECONDS = 90 * 24 * 60 * 60;

export interface StoredConsent {
  granted: boolean;
  version: string;
  at: string;
}

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

/** The stored choice, or null when none, unreadable, or of an older banner text. */
export function readConsent(storage: StorageLike | null | undefined): StoredConsent | null {
  if (!storage) return null;
  try {
    const raw = storage.getItem(CONSENT_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<StoredConsent> | null;
    if (!v || typeof v.granted !== 'boolean' || v.version !== CONSENT_VERSION || typeof v.at !== 'string') return null;
    return { granted: v.granted, version: v.version, at: v.at };
  } catch {
    return null;
  }
}

/** Stores a choice; false when storage refuses. */
export function storeConsent(storage: StorageLike | null | undefined, granted: boolean, now: Date = new Date()): boolean {
  if (!storage) return false;
  const value: StoredConsent = { granted, version: CONSENT_VERSION, at: now.toISOString() };
  try {
    storage.setItem(CONSENT_KEY, JSON.stringify(value));
    return true;
  } catch {
    return false;
  }
}

/** Ten random decimal digits (the tail of a Meta _fbp value). */
export function randomDigits(n: number, rand: () => number = Math.random): string {
  let s = '';
  for (let i = 0; i < n; i++) s += String(Math.floor(rand() * 10) % 10);
  return s;
}

export function fbpValue(nowMs: number, rand: () => number = Math.random): string {
  return `fb.1.${nowMs}.${randomDigits(10, rand)}`;
}

export function fbcValue(nowMs: number, fbclid: string): string {
  return `fb.1.${nowMs}.${fbclid}`;
}

/** A `document.cookie` assignment string. */
export function cookieString(name: string, value: string, secure: boolean): string {
  return `${name}=${value}; max-age=${COOKIE_MAX_AGE_SECONDS}; path=/; SameSite=Lax${secure ? '; Secure' : ''}`;
}

function hasCookie(cookieJar: string, name: string): boolean {
  return cookieJar.split(';').some((p) => p.trim().startsWith(`${name}=`));
}

/** The cookie assignments to make now: `_fbp` when missing, `_fbc` when the
 * URL carries fbclid and the cookie is missing. */
export function cookiesToSet(
  cookieJar: string,
  search: string,
  secure: boolean,
  nowMs: number,
  rand: () => number = Math.random,
): string[] {
  const out: string[] = [];
  if (!hasCookie(cookieJar, '_fbp')) out.push(cookieString('_fbp', fbpValue(nowMs, rand), secure));
  const fbclid = new URLSearchParams(search).get('fbclid');
  if (fbclid && !hasCookie(cookieJar, '_fbc')) {
    out.push(cookieString('_fbc', fbcValue(nowMs, encodeURIComponent(fbclid)), secure));
  }
  return out;
}

/** Sets the first-party cookies of an accepted choice. */
export function setTrackingCookies(
  doc: Document = document,
  loc: Pick<Location, 'search' | 'protocol'> = window.location,
  nowMs: number = Date.now(),
): void {
  for (const c of cookiesToSet(doc.cookie, loc.search, loc.protocol === 'https:', nowMs)) {
    doc.cookie = c;
  }
}

/** Puts the choice on one <arena-tickets> element. */
export function applyConsentAttributes(el: Element, granted: boolean): void {
  el.setAttribute('tracking-consent', granted ? 'granted' : 'denied');
  el.setAttribute('tracking-consent-version', CONSENT_VERSION);
}

/** Puts the choice on every <arena-tickets> element under root. */
export function applyConsentToAll(root: ParentNode, granted: boolean): void {
  root.querySelectorAll('arena-tickets').forEach((el) => applyConsentAttributes(el, granted));
}

const BANNER_ID = 'asa-cookie-banner';
const LINK_ID = 'asa-footer-cookies';

export interface TrackingOptions {
  enabled: boolean | undefined;
  locale: PageLocale;
  /** The privacy policy to link; only an https address is linked. */
  privacyUrl?: string | null;
}

let observer: MutationObserver | null = null;
let current: boolean | null = null;

function safeLocalStorage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function decide(granted: boolean): void {
  storeConsent(safeLocalStorage(), granted);
  if (granted) setTrackingCookies();
  current = granted;
  applyConsentToAll(document, granted);
  removeBanner();
}

function removeBanner(): void {
  document.getElementById(BANNER_ID)?.remove();
}

/** Shows the banner (replacing an open one). */
export function showBanner(opts: TrackingOptions): void {
  removeBanner();
  const s = t(opts.locale);
  const bar = document.createElement('div');
  bar.id = BANNER_ID;
  bar.className = 'asa-cookie';
  bar.setAttribute('role', 'dialog');
  bar.setAttribute('aria-label', s.cookieSettings);
  bar.dir = isRtlLocale(opts.locale) ? 'rtl' : 'ltr';

  const text = document.createElement('p');
  text.className = 'asa-cookie__text';
  text.textContent = s.cookieText + ' ';
  const url = (opts.privacyUrl ?? '').trim();
  if (/^https:\/\//i.test(url)) {
    const a = document.createElement('a');
    a.href = url;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    a.textContent = s.cookiePrivacy;
    text.appendChild(a);
  }

  const actions = document.createElement('div');
  actions.className = 'asa-cookie__actions';
  const decline = document.createElement('button');
  decline.type = 'button';
  decline.className = 'asa-cookie__btn asa-cookie__btn--decline';
  decline.textContent = s.cookieDecline;
  decline.addEventListener('click', () => decide(false));
  const accept = document.createElement('button');
  accept.type = 'button';
  accept.className = 'asa-cookie__btn asa-cookie__btn--accept';
  accept.textContent = s.cookieAccept;
  accept.addEventListener('click', () => decide(true));
  actions.append(decline, accept);

  bar.append(text, actions);
  document.body.appendChild(bar);
}

function addFooterLink(opts: TrackingOptions): void {
  document.getElementById(LINK_ID)?.remove();
  const inner = document.querySelector('.asa-footer__in');
  if (!inner) return;
  const p = document.createElement('p');
  p.id = LINK_ID;
  p.className = 'asa-footer__cookies';
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'asa-contact-btn asa-contact-btn--footer';
  b.textContent = t(opts.locale).cookieSettings;
  b.addEventListener('click', () => showBanner(opts));
  p.appendChild(b);
  inner.insertBefore(p, document.getElementById('asa-footer-text'));
}

/**
 * Starts the banner logic of a loaded page. With tracking off it does
 * nothing at all. Safe to call again (each page render): the previous
 * banner, link and observer are replaced.
 */
export function initTracking(opts: TrackingOptions): void {
  removeBanner();
  document.getElementById(LINK_ID)?.remove();
  observer?.disconnect();
  observer = null;
  current = null;
  if (opts.enabled !== true) return;

  const stored = readConsent(safeLocalStorage());
  if (stored) {
    current = stored.granted;
    // A choice made earlier: the cookies are the choice's, set once more
    // only if they are missing.
    if (stored.granted) setTrackingCookies();
    applyConsentToAll(document, stored.granted);
  } else {
    showBanner(opts);
  }
  addFooterLink(opts);

  // An element created later (the widget mounts after the page renders).
  observer = new MutationObserver((records) => {
    if (current === null) return;
    for (const r of records) {
      r.addedNodes.forEach((n) => {
        if (!(n instanceof Element)) return;
        if (n.tagName.toLowerCase() === 'arena-tickets') applyConsentAttributes(n, current as boolean);
        n.querySelectorAll?.('arena-tickets').forEach((el) => applyConsentAttributes(el, current as boolean));
      });
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });
}
