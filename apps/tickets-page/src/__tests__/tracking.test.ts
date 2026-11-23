import { beforeEach, describe, expect, it } from 'vitest';
import {
  CONSENT_KEY,
  CONSENT_VERSION,
  applyConsentAttributes,
  applyConsentToAll,
  cookieString,
  cookiesToSet,
  fbpValue,
  initTracking,
  readConsent,
  storeConsent,
} from '../lib/tracking.ts';

function memStore(): Storage {
  const m = new Map<string, string>();
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  } as unknown as Storage;
}

describe('consent storage', () => {
  it('round-trips a choice', () => {
    const s = memStore();
    expect(readConsent(s)).toBeNull();
    expect(storeConsent(s, true, new Date('2026-11-23T10:00:00Z'))).toBe(true);
    expect(readConsent(s)).toEqual({ granted: true, version: CONSENT_VERSION, at: '2026-11-23T10:00:00.000Z' });
  });

  it('ignores an older version and garbage', () => {
    const s = memStore();
    s.setItem(CONSENT_KEY, JSON.stringify({ granted: true, version: 'old', at: 'x' }));
    expect(readConsent(s)).toBeNull();
    s.setItem(CONSENT_KEY, '{bad');
    expect(readConsent(s)).toBeNull();
    expect(readConsent(null)).toBeNull();
  });
});

describe('cookies', () => {
  it('builds _fbp with 10 digits', () => {
    expect(fbpValue(1700000000000, () => 0.5)).toBe('fb.1.1700000000000.5555555555');
  });

  it('formats the cookie string', () => {
    expect(cookieString('_fbp', 'v', true)).toBe('_fbp=v; max-age=7776000; path=/; SameSite=Lax; Secure');
    expect(cookieString('_fbp', 'v', false)).not.toContain('Secure');
  });

  it('sets only missing cookies and _fbc only with fbclid', () => {
    expect(cookiesToSet('', '', false, 5, () => 0)).toEqual([cookieString('_fbp', 'fb.1.5.0000000000', false)]);
    expect(cookiesToSet('_fbp=x', '?fbclid=AbC', true, 5)).toEqual([cookieString('_fbc', 'fb.1.5.AbC', true)]);
    expect(cookiesToSet('_fbp=x; _fbc=y', '?fbclid=AbC', true, 5)).toEqual([]);
  });
});

describe('attributes', () => {
  beforeEach(() => {
    document.body.innerHTML = '<arena-tickets id="a"></arena-tickets><div><arena-tickets id="b"></arena-tickets></div>';
  });

  it('sets both attributes', () => {
    const el = document.getElementById('a')!;
    applyConsentAttributes(el, true);
    expect(el.getAttribute('tracking-consent')).toBe('granted');
    expect(el.getAttribute('tracking-consent-version')).toBe(CONSENT_VERSION);
    applyConsentAttributes(el, false);
    expect(el.getAttribute('tracking-consent')).toBe('denied');
  });

  it('applies to every element', () => {
    applyConsentToAll(document, false);
    expect(document.querySelectorAll('arena-tickets[tracking-consent="denied"]').length).toBe(2);
  });
});

describe('initTracking', () => {
  beforeEach(() => {
    localStorage.clear();
    document.body.innerHTML = '<arena-tickets></arena-tickets><footer><div class="asa-footer__in"><p id="asa-footer-text">c</p></div></footer>';
  });

  it('does nothing when tracking is off', () => {
    initTracking({ enabled: false, locale: 'en' });
    expect(document.getElementById('asa-cookie-banner')).toBeNull();
    expect(document.getElementById('asa-footer-cookies')).toBeNull();
    expect(document.querySelector('arena-tickets')!.hasAttribute('tracking-consent')).toBe(false);
  });

  it('shows the banner without a choice, sets nothing, and honours Decline', () => {
    initTracking({ enabled: true, locale: 'he', privacyUrl: 'https://x.example/p' });
    const banner = document.getElementById('asa-cookie-banner')!;
    expect(banner.dir).toBe('rtl');
    expect(banner.querySelector('a')?.getAttribute('href')).toBe('https://x.example/p');
    expect(document.querySelector('arena-tickets')!.hasAttribute('tracking-consent')).toBe(false);
    (banner.querySelector('.asa-cookie__btn--decline') as HTMLButtonElement).click();
    expect(document.getElementById('asa-cookie-banner')).toBeNull();
    expect(document.querySelector('arena-tickets')!.getAttribute('tracking-consent')).toBe('denied');
    expect(readConsent(localStorage)?.granted).toBe(false);
  });

  it('hides the banner for a stored choice and the footer link reopens it', () => {
    storeConsent(localStorage, false);
    initTracking({ enabled: true, locale: 'en' });
    expect(document.getElementById('asa-cookie-banner')).toBeNull();
    expect(document.querySelector('arena-tickets')!.getAttribute('tracking-consent')).toBe('denied');
    (document.querySelector('#asa-footer-cookies button') as HTMLButtonElement).click();
    expect(document.getElementById('asa-cookie-banner')).not.toBeNull();
  });

  it('declining after accepting expires _fbp and _fbc', () => {
    document.cookie = '_fbp=fb.1.1.1111111111; path=/';
    document.cookie = '_fbc=fb.1.1.AbC; path=/';
    expect(document.cookie).toContain('_fbp=');
    expect(document.cookie).toContain('_fbc=');

    initTracking({ enabled: true, locale: 'en' });
    (document.querySelector('.asa-cookie__btn--accept') as HTMLButtonElement).click();
    expect(readConsent(localStorage)?.granted).toBe(true);

    (document.querySelector('#asa-footer-cookies button') as HTMLButtonElement).click();
    (document.querySelector('.asa-cookie__btn--decline') as HTMLButtonElement).click();
    expect(readConsent(localStorage)?.granted).toBe(false);
    expect(document.cookie).not.toContain('_fbp=');
    expect(document.cookie).not.toContain('_fbc=');
  });

  it('marks an element created later', async () => {
    storeConsent(localStorage, true);
    initTracking({ enabled: true, locale: 'en' });
    const late = document.createElement('arena-tickets');
    document.body.appendChild(late);
    await new Promise((r) => setTimeout(r, 0));
    expect(late.getAttribute('tracking-consent')).toBe('granted');
  });
});
