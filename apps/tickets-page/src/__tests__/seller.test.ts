import { beforeEach, describe, expect, it } from 'vitest';
import { renderEvent } from '../lib/render.ts';
import { addressLines, renderSellerBlock, renderSellerFooter, taxLabel } from '../lib/seller.ts';
import { PAGE_LOCALES } from '../lib/locale.ts';
import { t } from '../lib/i18n.ts';
import type { HostedPageResponse, HostedPageSeller } from '../lib/api.ts';

const FULL: HostedPageSeller = {
  legal_name: 'Lampyris Events s.r.o.',
  registration_number: '12345678',
  tax_id: 'CZ12345678',
  tax_scheme: 'eu_vat',
  address: { line1: 'Hlavní 1', line2: 'Budova B', city: 'Praha', postal_code: '110 00', country: 'CZ' },
  public_contact: { email: 'info@lampyris.example', phone: '+420 777 123 456' },
};

function event(extra: Partial<HostedPageResponse> = {}): HostedPageResponse {
  return {
    org: { slug: 'lampyris', name: 'Lampyris', logo_url: null },
    event: {
      id: 'e1',
      slug: 'show',
      title: 'Show',
      description: null,
      short_description: null,
      image_url: null,
      poster_url: null,
      age_rating: null,
      venue_names: [],
      first_session_at: null,
      last_session_at: null,
      first_session_timezone: null,
    },
    feed_token: 'tok',
    default_locale: 'en',
    ...extra,
  };
}

function footerDom(): void {
  document.body.innerHTML = '<main id="m"></main><footer class="asa-footer"><div class="asa-footer__in"><p id="asa-footer-seller" hidden></p><p id="asa-footer-text">c</p></div></footer>';
}

beforeEach(() => {
  document.body.innerHTML = '';
});

describe('renderSellerBlock', () => {
  it('prints every part that is present', () => {
    const el = renderSellerBlock(FULL, null, 'en');
    expect(el).not.toBeNull();
    const text = el!.textContent ?? '';
    expect(text).toContain('Seller: Lampyris Events s.r.o.');
    expect(text).toContain('Registration no.: 12345678');
    expect(text).toContain('VAT ID: CZ12345678');
    expect(text).toContain('Hlavní 1');
    expect(text).toContain('Budova B');
    expect(text).toContain('110 00 Praha');
    expect(text).toContain('Czechia');
    expect(el!.querySelector('a[href="mailto:info@lampyris.example"]')?.textContent).toBe('info@lampyris.example');
    expect(el!.querySelector('a[href="tel:+420777123456"]')?.textContent).toBe('+420 777 123 456');
    expect(text).not.toContain('Organizer');
  });

  it('keeps the organizer and the seller as two distinct labelled lines', () => {
    const el = renderSellerBlock(FULL, { name: 'Promoter Name' }, 'en')!;
    const seller = el.querySelector('.asa-seller__line--seller')?.textContent;
    const organizer = el.querySelector('.asa-seller__line--organizer')?.textContent;
    expect(seller).toBe('Seller: Lampyris Events s.r.o.');
    expect(organizer).toBe('Organizer: Promoter Name');
  });

  it('prints neither value nor label for missing parts', () => {
    const el = renderSellerBlock({ legal_name: 'Only Name Ltd' }, null, 'en')!;
    const text = el.textContent ?? '';
    expect(text).toBe('Seller: Only Name Ltd');
    expect(el.querySelector('a')).toBeNull();
    expect(el.querySelector('address')).toBeNull();
    expect(text).not.toContain('Tax');
    expect(text).not.toContain('Registration');
  });

  it('treats blank strings as missing and renders no empty labels', () => {
    const el = renderSellerBlock(
      { legal_name: ' X ', tax_id: '  ', registration_number: '', address: { line1: ' ', city: '' }, public_contact: { email: '', phone: ' ' } },
      { name: '  ' },
      'en',
    )!;
    expect(el.textContent).toBe('Seller: X');
  });

  it('labels the tax number by scheme and falls back to Tax ID', () => {
    expect(taxLabel('eu_vat', 'en')).toBe('VAT ID');
    expect(taxLabel('us_ein', 'en')).toBe('EIN');
    expect(taxLabel('other', 'en')).toBe('Tax ID');
    expect(taxLabel(undefined, 'en')).toBe('Tax ID');
    expect(taxLabel('something_new', 'cs')).toBe('DIČ');
    const el = renderSellerBlock({ legal_name: 'N', tax_id: '99', tax_scheme: 'other' }, null, 'en')!;
    expect(el.textContent).toContain('Tax ID: 99');
  });

  it('returns nothing without a seller or without a name', () => {
    expect(renderSellerBlock(null, null, 'en')).toBeNull();
    expect(renderSellerBlock(undefined, { name: 'P' }, 'en')).toBeNull();
    expect(renderSellerBlock({ legal_name: '  ' }, null, 'en')).toBeNull();
  });

  it('escapes markup in every field', () => {
    const evil = '<img src=x onerror=alert(1)>';
    const el = renderSellerBlock(
      {
        legal_name: evil,
        tax_id: evil,
        registration_number: evil,
        address: { line1: evil, city: evil },
        public_contact: { email: `a@b.c${'"'}onmouseover=x`, phone: evil },
      },
      { name: evil },
      'en',
    )!;
    expect(el.querySelector('img')).toBeNull();
    expect(el.innerHTML).not.toContain('<img');
    expect(el.textContent).toContain(evil);
    // an e-mail that could break out of the attribute is not linked at all
    expect(el.querySelector('a[href^="mailto:"]')).toBeNull();
  });

  it('makes the phone a link only when it has dialable characters', () => {
    const el = renderSellerBlock({ legal_name: 'N', public_contact: { phone: 'ask at the desk' } }, null, 'en')!;
    expect(el.querySelector('a[href^="tel:"]')).toBeNull();
    expect(el.textContent).toContain('ask at the desk');
  });

  it('has strings for the seller block in every page locale', () => {
    for (const loc of PAGE_LOCALES) {
      const s = t(loc);
      for (const key of ['sellerLabel', 'organizerLabel', 'registrationNo', 'taxId', 'vatId', 'einId'] as const) {
        expect(s[key].trim().length, `${loc}.${key}`).toBeGreaterThan(0);
      }
      expect(s.sellerLabel).not.toBe(s.organizerLabel);
    }
    expect(renderSellerBlock(FULL, { name: 'P' }, 'es')!.textContent).toContain('Vendedor: Lampyris Events s.r.o.');
    expect(renderSellerBlock(FULL, { name: 'P' }, 'es')!.textContent).toContain('Organizador: P');
    expect(renderSellerBlock(FULL, { name: 'P' }, 'cs')!.textContent).toContain('Prodávající: ');
    expect(renderSellerBlock(FULL, { name: 'P' }, 'ru')!.textContent).toContain('Продавец: ');
  });

  it('renders right-to-left safely for he: hebrew labels, ltr contacts, auto-direction values, logical CSS', () => {
    const el = renderSellerBlock(FULL, { name: 'מפיק' }, 'he')!;
    expect(el.textContent).toContain('מוכר: Lampyris Events s.r.o.');
    expect(el.textContent).toContain('מארגן: מפיק');
    for (const a of Array.from(el.querySelectorAll('a'))) {
      expect(a.getAttribute('dir')).toBe('ltr');
    }
    for (const v of Array.from(el.querySelectorAll('.asa-seller__value'))) {
      expect(v.getAttribute('dir')).toBe('auto');
    }
    expect(el.querySelector('address span')?.getAttribute('dir')).toBe('auto');
  });
});

describe('addressLines', () => {
  it('falls back to the seller country and drops empty parts', () => {
    expect(addressLines({ legal_name: 'N', country: 'XX-Land', address: { city: 'Brno' } }, 'en')).toEqual(['Brno', 'XX-Land']);
    expect(addressLines({ legal_name: 'N' }, 'en')).toEqual([]);
  });
});

describe('renderEvent seller block', () => {
  const opts = { apiBase: '', resumingCheckout: false };

  it('adds the block under the purchase card when the response carries a seller', () => {
    const c = document.createElement('main');
    document.body.appendChild(c);
    renderEvent(c, event({ seller: FULL, promoter: { name: 'Promoter Name' } }), 'en', opts);
    const block = c.querySelector('.asa-seller');
    expect(block).not.toBeNull();
    expect(block!.previousElementSibling?.id).toBe('asa-widget');
    expect(block!.textContent).toContain('Organizer: Promoter Name');
  });

  it('renders exactly what it rendered before when there is no seller', () => {
    const a = document.createElement('main');
    const b = document.createElement('main');
    document.body.append(a, b);
    renderEvent(a, event(), 'en', opts);
    renderEvent(b, event({ seller: null, promoter: { name: 'Ignored without a seller' } }), 'en', opts);
    expect(b.querySelector('.asa-seller')).toBeNull();
    expect(b.innerHTML).toBe(a.innerHTML);
  });
});

describe('renderSellerFooter', () => {
  it('prints a compact seller line in the footer', () => {
    footerDom();
    renderSellerFooter(FULL, 'en');
    const host = document.getElementById('asa-footer-seller')!;
    expect(host.hidden).toBe(false);
    const text = host.textContent ?? '';
    expect(text).toContain('Seller: Lampyris Events s.r.o.');
    expect(text).toContain('Registration no. 12345678');
    expect(text).toContain('VAT ID CZ12345678');
    expect(text).toContain('Hlavní 1, Budova B, 110 00 Praha, Czechia');
    expect(host.querySelector('br')).toBeNull();
    expect(host.querySelector('a[href="mailto:info@lampyris.example"]')).not.toBeNull();
    expect(host.querySelector('a[href^="tel:"]')).not.toBeNull();
  });

  it('stays empty and hidden without a seller, and clears a previous one', () => {
    footerDom();
    renderSellerFooter(FULL, 'en');
    renderSellerFooter(undefined, 'en');
    const host = document.getElementById('asa-footer-seller')!;
    expect(host.hidden).toBe(true);
    expect(host.textContent).toBe('');
  });

  it('creates its own line when the footer lacks the placeholder', () => {
    document.body.innerHTML = '<footer><div class="asa-footer__in"><p id="asa-footer-text">c</p></div></footer>';
    renderSellerFooter({ legal_name: 'N' }, 'en');
    expect(document.getElementById('asa-footer-seller')?.textContent).toBe('Seller: N');
  });

  it('escapes markup and localizes for he', () => {
    footerDom();
    renderSellerFooter({ legal_name: '<b>x</b>', public_contact: { email: 'a@b.cz' } }, 'he');
    const host = document.getElementById('asa-footer-seller')!;
    expect(host.querySelector('b')).toBeNull();
    expect(host.textContent).toContain('מוכר: <b>x</b>');
    expect(host.querySelector('a')?.getAttribute('dir')).toBe('ltr');
  });
});
