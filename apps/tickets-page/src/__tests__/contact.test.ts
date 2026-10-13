import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  contactAvailable,
  openContactForm,
  renderContactButton,
  renderContactFooter,
  sendContact,
  validateContact,
  type ContactContext,
} from '../lib/contact.ts';
import { renderEvent } from '../lib/render.ts';
import { PAGE_LOCALES } from '../lib/locale.ts';
import { t } from '../lib/i18n.ts';
import type { HostedPageResponse } from '../lib/api.ts';

const CTX: ContactContext = {
  apiBase: 'https://api.example',
  slug: 'lampyris',
  feedToken: 'ft_1',
  locale: 'en',
  form: { available: true },
};

function fill(overlay: HTMLElement, v: { name?: string; email?: string; message?: string; consent?: boolean }): void {
  const set = (sel: string, value: string) => {
    (overlay.querySelector(sel) as HTMLInputElement | HTMLTextAreaElement).value = value;
  };
  set('input[name="name"]', v.name ?? 'Anna');
  set('input[name="email"]', v.email ?? 'anna@example.com');
  set('textarea[name="message"]', v.message ?? 'Is the venue step-free?');
  (overlay.querySelector('input[name="consent"]') as HTMLInputElement).checked = v.consent ?? true;
}

function submit(overlay: HTMLElement): void {
  overlay.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }));
}

beforeEach(() => {
  document.body.innerHTML = '<footer><div class="asa-footer__in"><p id="asa-footer-text">© Arena</p></div></footer>';
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.querySelectorAll('.asa-contact').forEach((el) => el.remove());
});

describe('contactAvailable', () => {
  it('offers the form only when the API says so', () => {
    expect(contactAvailable({ available: true })).toBe(true);
    expect(contactAvailable({ available: false })).toBe(false);
    expect(contactAvailable(null)).toBe(false);
    expect(contactAvailable(undefined)).toBe(false);
  });
});

describe('validateContact', () => {
  const ok = { name: 'Anna', email: 'anna@example.com', message: 'Hello there!', consent: true };
  it('accepts a complete form', () => {
    expect(validateContact(ok)).toBe('');
  });
  it('names the first problem, with the API limits', () => {
    expect(validateContact({ ...ok, name: '  ' })).toBe('name');
    expect(validateContact({ ...ok, name: 'x'.repeat(101) })).toBe('name');
    expect(validateContact({ ...ok, email: 'anna' })).toBe('email');
    expect(validateContact({ ...ok, email: 'a b@example.com' })).toBe('email');
    expect(validateContact({ ...ok, message: 'short' })).toBe('message');
    expect(validateContact({ ...ok, message: 'x'.repeat(2001) })).toBe('message');
    expect(validateContact({ ...ok, consent: false })).toBe('consent');
  });
});

describe('sendContact', () => {
  it('posts to the public route with the feed token and the page language', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{"status":"queued"}', { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const code = await sendContact({ ...CTX, apiBase: 'https://api.example/' }, {
      name: 'Anna', email: 'anna@example.com', message: 'Hello there!', consent: true, turnstile_token: 'tok',
    });
    expect(code).toBe('');
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://api.example/v1/public/organizer-contact/lampyris');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toMatchObject({
      name: 'Anna', consent: true, turnstile_token: 'tok', feed_token: 'ft_1', locale: 'en',
    });
  });

  it('answers the API error code of a refusal', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":{"code":"contact.rate_limited"}}', { status: 429 })));
    expect(await sendContact(CTX, { name: 'A', email: 'a@b.cz', message: '0123456789', consent: true, turnstile_token: '' }))
      .toBe('contact.rate_limited');
  });

  it('says network when the request never reached the API', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')));
    expect(await sendContact(CTX, { name: 'A', email: 'a@b.cz', message: '0123456789', consent: true, turnstile_token: '' }))
      .toBe('network');
  });
});

describe('openContactForm', () => {
  it('refuses an incomplete form without calling the API', () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const overlay = openContactForm(CTX);
    fill(overlay, { consent: false });
    submit(overlay);
    expect(overlay.querySelector('.asa-contact__status')!.textContent).toBe(t('en').contactErrConsent);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('sends a complete form and thanks the buyer', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{"status":"queued"}', { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    const overlay = openContactForm(CTX);
    fill(overlay, {});
    submit(overlay);
    await vi.waitFor(() => expect(overlay.querySelector('form')).toBeNull());
    expect(overlay.textContent).toContain(t('en').contactSent);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('shows a friendly sentence for a refusal and keeps the form', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":{"code":"contact.rate_limited"}}', { status: 429 })));
    const overlay = openContactForm(CTX);
    fill(overlay, {});
    submit(overlay);
    await vi.waitFor(() =>
      expect(overlay.querySelector('.asa-contact__status')!.textContent).toBe(t('en').contactErrRate));
    expect(overlay.querySelector('form')).not.toBeNull();
  });

  it('waits for the captcha when the page has a site key', () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    const overlay = openContactForm({ ...CTX, form: { available: true, turnstile_site_key: '0xKEY' } });
    fill(overlay, {});
    submit(overlay);
    expect(overlay.querySelector('.asa-contact__status')!.textContent).toBe(t('en').contactErrCaptcha);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('tells about the Telegram copy and links the privacy policy', () => {
    const overlay = openContactForm({ ...CTX, form: { available: true, privacy_url: 'https://arenasoldout.com/legal/privacy' } });
    const note = overlay.querySelector('.asa-contact__note') as HTMLElement;
    expect(note.textContent).toContain('Telegram');
    expect(note.textContent).toContain('90');
    const link = note.querySelector('a') as HTMLAnchorElement;
    expect(link.href).toBe('https://arenasoldout.com/legal/privacy');
    expect(link.rel).toContain('noopener');
    overlay.remove();
  });

  it('links no privacy policy that is not an https address', () => {
    for (const privacy_url of [undefined, '', 'javascript:alert(1)', 'http://arenasoldout.com/legal/privacy']) {
      const overlay = openContactForm({ ...CTX, form: { available: true, privacy_url } });
      expect(overlay.querySelector('.asa-contact__note a')).toBeNull();
      expect(overlay.querySelector('.asa-contact__note')?.textContent).toContain('Telegram');
      overlay.remove();
    }
  });

  it('closes on Escape and on Cancel', () => {
    openContactForm(CTX);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(document.querySelector('.asa-contact')).toBeNull();
    const overlay = openContactForm(CTX);
    (overlay.querySelector('.asa-contact__cancel') as HTMLButtonElement).click();
    expect(document.querySelector('.asa-contact')).toBeNull();
  });

  it('is right-to-left on the Hebrew page', () => {
    const overlay = openContactForm({ ...CTX, locale: 'he' });
    expect(overlay.querySelector('[role="dialog"]')!.getAttribute('dir')).toBe('rtl');
  });
});

describe('renderContactButton / renderContactFooter', () => {
  it('renders nothing without a form', () => {
    expect(renderContactButton({ ...CTX, form: null })).toBeNull();
    renderContactFooter({ ...CTX, form: undefined });
    expect(document.getElementById('asa-footer-contact')).toBeNull();
  });

  it('puts one button in the footer and replaces it on a re-render', () => {
    renderContactFooter(CTX);
    renderContactFooter(CTX);
    const buttons = document.querySelectorAll('#asa-footer-contact button');
    expect(buttons).toHaveLength(1);
    expect(buttons[0].textContent).toBe(t('en').contactButton);
    renderContactFooter({ ...CTX, form: null });
    expect(document.getElementById('asa-footer-contact')).toBeNull();
  });

  it('has its words in every page language', () => {
    for (const loc of PAGE_LOCALES) {
      const s = t(loc);
      for (const key of ['contactButton', 'contactConsent', 'contactConsentNote', 'contactPrivacy', 'contactSent', 'contactErrGeneric'] as const) {
        expect(s[key].trim(), `${loc}.${key}`).not.toBe('');
      }
    }
  });
});

describe('event page', () => {
  function event(extra: Partial<HostedPageResponse>): HostedPageResponse {
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
      feed_token: 'ft_1',
      default_locale: 'en',
      ...extra,
    };
  }

  it('offers the form under the seller block and never prints the e-mail', () => {
    const main = document.createElement('main');
    renderEvent(main, event({
      seller: { legal_name: 'Lampyris s.r.o.', public_contact: { email: 'secret@lampyris.example' } },
      contact_form: { available: true },
    }), 'en', { apiBase: 'https://api.example', resumingCheckout: false });
    const block = main.querySelector('.asa-seller')!;
    expect(block.querySelector('.asa-seller__write button')?.textContent).toBe(t('en').contactButton);
    expect(main.innerHTML).not.toContain('secret@lampyris.example');
  });
});
