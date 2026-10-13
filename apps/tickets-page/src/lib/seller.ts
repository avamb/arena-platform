import type { PageLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { HostedPagePromoter, HostedPageSeller } from './api.ts';

/**
 * seller.ts — the legal block of the hosted page: WHO SELLS the ticket and,
 * separately, who ORGANIZES the event.
 *
 * The two roles are different people in law (the seller is the buyer's
 * contract partner), so they are always printed as two labelled lines —
 * "Seller: ..." and "Organizer: ..." — never merged into one name.
 *
 * Everything is written with textContent (never innerHTML) and a part that
 * is missing or blank prints neither its value nor its label. A response
 * without `seller` renders nothing at all.
 */

/** A trimmed non-empty string, or '' for anything else. */
function clean(value: string | null | undefined): string {
  return typeof value === 'string' ? value.trim() : '';
}

/** Label printed next to the tax number. The scheme comes from the
 * organization's legal settings (`eu_vat`, `gb_vat`, `il_vat`, `us_ein`,
 * `other`); anything unknown is a plain "Tax ID". */
export function taxLabel(scheme: string | null | undefined, locale: PageLocale): string {
  const strings = t(locale);
  switch (clean(scheme).toLowerCase()) {
    case 'eu_vat':
    case 'gb_vat':
    case 'il_vat':
    case 'vat':
      return strings.vatId;
    case 'us_ein':
    case 'ein':
      return strings.einId;
    default:
      return strings.taxId;
  }
}

/** "CZ" -> "Czechia" in the page language; any other value (or a runtime
 * without Intl.DisplayNames) is printed as it came. */
function countryName(value: string, locale: PageLocale): string {
  if (!/^[A-Za-z]{2}$/.test(value)) return value;
  try {
    const names = new Intl.DisplayNames([locale], { type: 'region' });
    return names.of(value.toUpperCase()) ?? value;
  } catch {
    return value;
  }
}

/** The postal address as printable lines: street, second street line,
 * "postcode city", country. Empty parts are dropped. */
export function addressLines(seller: HostedPageSeller, locale: PageLocale): string[] {
  const a = seller.address ?? {};
  const cityLine = [clean(a.postal_code), clean(a.city)].filter(Boolean).join(' ');
  const country = clean(a.country) || clean(seller.country);
  return [clean(a.line1), clean(a.line2), cityLine, country ? countryName(country, locale) : ''].filter(Boolean);
}

/** `tel:` target: digits and a leading plus only. '' when nothing is left. */
function telTarget(phone: string): string {
  const digits = phone.replace(/[^\d+]/g, '');
  return digits.replace(/(?!^)\+/g, '');
}

/** The phone as a tel: link, when present. The e-mail is never printed:
 * since the contact form (2026-10-13) buyers write through it, and the API
 * no longer sends the address. The value is left-to-right text even on a
 * right-to-left page. */
function contactLinks(seller: HostedPageSeller): HTMLElement[] {
  const out: HTMLElement[] = [];
  const phone = clean(seller.public_contact?.phone);
  if (phone) {
    const target = telTarget(phone);
    if (target) {
      const a = document.createElement('a');
      a.className = 'asa-seller__link';
      a.href = `tel:${target}`;
      a.setAttribute('dir', 'ltr');
      a.textContent = phone;
      out.push(a);
    } else {
      const span = document.createElement('span');
      span.setAttribute('dir', 'ltr');
      span.textContent = phone;
      out.push(span);
    }
  }
  return out;
}

/** "<label>: <value>" as a label span plus a value span; the value's own
 * direction is detected from its text so a Latin name on a Hebrew page (and
 * the reverse) does not scramble the punctuation around it. */
function labelled(tag: 'p' | 'div', className: string, label: string, value: string): HTMLElement {
  const el = document.createElement(tag);
  el.className = className;
  const l = document.createElement('span');
  l.className = 'asa-seller__label';
  l.textContent = `${label}:`;
  el.appendChild(l);
  el.appendChild(document.createTextNode(' '));
  const v = document.createElement('span');
  v.className = 'asa-seller__value';
  v.setAttribute('dir', 'auto');
  v.textContent = value;
  el.appendChild(v);
  return el;
}

/** The full seller block of the event page. Returns null when there is no
 * seller (or it has no name to print). */
export function renderSellerBlock(
  seller: HostedPageSeller | null | undefined,
  promoter: HostedPagePromoter | null | undefined,
  locale: PageLocale,
): HTMLElement | null {
  const legalName = clean(seller?.legal_name);
  if (!seller || !legalName) return null;
  const strings = t(locale);

  const section = document.createElement('section');
  section.className = 'asa-seller';
  section.setAttribute('aria-label', strings.sellerLabel);

  section.appendChild(labelled('p', 'asa-seller__line asa-seller__line--seller', strings.sellerLabel, legalName));

  const promoterName = clean(promoter?.name);
  if (promoterName) {
    section.appendChild(labelled('p', 'asa-seller__line asa-seller__line--organizer', strings.organizerLabel, promoterName));
  }

  const registration = clean(seller.registration_number);
  const taxId = clean(seller.tax_id);
  if (registration || taxId) {
    const list = document.createElement('div');
    list.className = 'asa-seller__ids';
    if (registration) list.appendChild(labelled('div', 'asa-seller__id', strings.registrationNo, registration));
    if (taxId) list.appendChild(labelled('div', 'asa-seller__id', taxLabel(seller.tax_scheme, locale), taxId));
    section.appendChild(list);
  }

  const lines = addressLines(seller, locale);
  if (lines.length > 0) {
    const address = document.createElement('address');
    address.className = 'asa-seller__address';
    lines.forEach((line, i) => {
      if (i > 0) address.appendChild(document.createElement('br'));
      const span = document.createElement('span');
      span.setAttribute('dir', 'auto');
      span.textContent = line;
      address.appendChild(span);
    });
    section.appendChild(address);
  }

  const links = contactLinks(seller);
  if (links.length > 0) {
    const p = document.createElement('p');
    p.className = 'asa-seller__contact';
    links.forEach((link, i) => {
      if (i > 0) p.appendChild(document.createTextNode(' · '));
      p.appendChild(link);
    });
    section.appendChild(p);
  }

  return section;
}

const FOOTER_ID = 'asa-footer-seller';

/** The compact seller line of the page footer: name, numbers, address on one
 * line, contact links. Clears the line when there is no seller, so a re-render
 * (retry, other page) never leaves a stale one behind. */
export function renderSellerFooter(seller: HostedPageSeller | null | undefined, locale: PageLocale): void {
  let host = document.getElementById(FOOTER_ID);
  if (!host) {
    const inner = document.querySelector('.asa-footer__in') ?? document.getElementById('asa-footer-text')?.parentElement;
    if (!inner) return;
    host = document.createElement('p');
    host.id = FOOTER_ID;
    inner.insertBefore(host, inner.firstChild);
  }
  while (host.firstChild) host.removeChild(host.firstChild);
  host.className = 'asa-footer__seller';

  const legalName = clean(seller?.legal_name);
  if (!seller || !legalName) {
    host.hidden = true;
    return;
  }
  host.hidden = false;
  const strings = t(locale);

  const parts: Array<string | HTMLElement> = [];
  const head = document.createElement('span');
  head.className = 'asa-seller__line--seller';
  const label = document.createElement('span');
  label.className = 'asa-seller__label';
  label.textContent = `${strings.sellerLabel}:`;
  const name = document.createElement('span');
  name.setAttribute('dir', 'auto');
  name.textContent = legalName;
  head.appendChild(label);
  head.appendChild(document.createTextNode(' '));
  head.appendChild(name);
  parts.push(head);

  const registration = clean(seller.registration_number);
  if (registration) parts.push(`${strings.registrationNo} ${registration}`);
  const taxId = clean(seller.tax_id);
  if (taxId) parts.push(`${taxLabel(seller.tax_scheme, locale)} ${taxId}`);
  const lines = addressLines(seller, locale);
  if (lines.length > 0) parts.push(lines.join(', '));
  parts.push(...contactLinks(seller));

  parts.forEach((part, i) => {
    if (i > 0) host!.appendChild(document.createTextNode(' · '));
    if (typeof part === 'string') {
      const span = document.createElement('span');
      span.setAttribute('dir', 'auto');
      span.textContent = part;
      host!.appendChild(span);
    } else {
      host!.appendChild(part);
    }
  });
}
