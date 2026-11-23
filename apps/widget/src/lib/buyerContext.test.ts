import { describe, it, expect } from 'vitest';
import {
  buildBuyerContext,
  captureClickIds,
  clickIdsKey,
  cookieValue,
  gaClientId,
  isTrackingGranted,
  persistClickIds,
  CLICK_IDS_WINDOW_MS,
} from './buyerContext.js';

function memStore(initial: Record<string, string> = {}) {
  const m = new Map(Object.entries(initial));
  return {
    m,
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
  };
}

const base = { consentVersion: 'v1', clickIds: {}, cookies: '', pageUrl: 'https://a.example/p' };

describe('buildBuyerContext', () => {
  it('sends nothing when the attribute is absent or unknown', () => {
    expect(buildBuyerContext({ ...base, consent: undefined })).toBeNull();
    expect(buildBuyerContext({ ...base, consent: null })).toBeNull();
    expect(buildBuyerContext({ ...base, consent: '' })).toBeNull();
    expect(buildBuyerContext({ ...base, consent: 'yes' })).toBeNull();
  });

  it('denied sends only the refusal and the version', () => {
    const ctx = buildBuyerContext({
      ...base,
      consent: 'denied',
      clickIds: { fbclid: 'x' },
      cookies: '_fbp=fb.1.1.2',
    });
    expect(ctx).toEqual({ tracking_consent: false, tracking_consent_text_version: 'v1' });
  });

  it('granted sends ids, cookies and page url', () => {
    const ctx = buildBuyerContext({
      ...base,
      consent: 'granted',
      clickIds: { fbclid: 'FB1', gclid: 'G1', fbclid_at: 1700000000000 },
      cookies: '_fbp=fb.1.5.6; _fbc=fb.1.7.FB1; _ga=GA1.1.123.456',
    });
    expect(ctx).toEqual({
      tracking_consent: true,
      tracking_consent_text_version: 'v1',
      fbclid: 'FB1',
      gclid: 'G1',
      fbp: 'fb.1.5.6',
      fbc: 'fb.1.7.FB1',
      ga_client_id: '123.456',
      page_url: 'https://a.example/p',
    });
  });

  it('derives fbc from the first capture time when there is no _fbc cookie', () => {
    const ctx = buildBuyerContext({
      ...base,
      consent: 'granted',
      clickIds: { fbclid: 'FB1', fbclid_at: 1700000000000 },
    });
    expect(ctx?.fbc).toBe('fb.1.1700000000000.FB1');
  });

  it('has no fbc without cookie and without fbclid', () => {
    expect(buildBuyerContext({ ...base, consent: 'granted' })?.fbc).toBeUndefined();
  });

  it('drops values with control characters or over 256 chars', () => {
    const ctx = buildBuyerContext({
      ...base,
      consent: 'granted',
      cookies: `_fbp=a%0Ab; _fbc=${'x'.repeat(257)}`,
    });
    expect(ctx?.fbp).toBeUndefined();
    expect(ctx?.fbc).toBeUndefined();
  });
});

describe('cookies', () => {
  it('parses _ga', () => {
    expect(gaClientId('a=1; _ga=GA1.2.111.222')).toBe('111.222');
    expect(gaClientId('_ga=junk')).toBeUndefined();
    expect(gaClientId('')).toBeUndefined();
  });
  it('reads a cookie by exact name', () => {
    expect(cookieValue('x_fbp=1; _fbp=2', '_fbp')).toBe('2');
  });
});

describe('captureClickIds', () => {
  const now = 1700000000000;
  it('stores ids per feed token and returns them', () => {
    const s = memStore();
    const ids = captureClickIds('tok', '?fbclid=AA&gclid=BB', { storage: s, now });
    expect(ids).toMatchObject({ fbclid: 'AA', gclid: 'BB', fbclid_at: now });
    expect(s.m.has(clickIdsKey('tok'))).toBe(true);
  });

  it('keeps the stored ids on a page without them, and the first capture time', () => {
    const s = memStore();
    captureClickIds('tok', '?fbclid=AA', { storage: s, now });
    const again = captureClickIds('tok', '', { storage: s, now: now + 1000 });
    expect(again).toMatchObject({ fbclid: 'AA', fbclid_at: now });
    const same = captureClickIds('tok', '?fbclid=AA', { storage: s, now: now + 2000 });
    expect(same.fbclid_at).toBe(now);
  });

  it('forgets ids after 7 days', () => {
    const s = memStore();
    captureClickIds('tok', '?gclid=BB', { storage: s, now });
    expect(captureClickIds('tok', '', { storage: s, now: now + CLICK_IDS_WINDOW_MS + 1 })).toEqual({});
  });

  it('keeps ids in memory only when persist is false', () => {
    const s = memStore();
    const ids = captureClickIds('tok', '?fbclid=AA&gclid=BB', { storage: s, now, persist: false });
    expect(ids).toMatchObject({ fbclid: 'AA', gclid: 'BB', fbclid_at: now });
    expect(s.m.size).toBe(0);
  });

  it('writes nothing to storage when consent is denied or absent', () => {
    for (const consent of ['denied', '', undefined, null, 'yes']) {
      const s = memStore();
      const ids = captureClickIds('tok', '?fbclid=AA', { storage: s, now, persist: isTrackingGranted(consent) });
      expect(ids.fbclid).toBe('AA');
      expect(s.m.size).toBe(0);
    }
  });

  it('writes to storage once consent is granted', () => {
    const s = memStore();
    captureClickIds('tok', '?fbclid=AA', { storage: s, now, persist: isTrackingGranted('granted') });
    expect(s.m.has(clickIdsKey('tok'))).toBe(true);
  });

  it('persists in-memory ids later, keeping the first capture time', () => {
    const s = memStore();
    const mem = captureClickIds('tok', '?fbclid=AA', { storage: null, now, persist: false });
    persistClickIds('tok', mem, { storage: s, now: now + 5000 });
    expect(captureClickIds('tok', '', { storage: s, now: now + 6000 })).toMatchObject({ fbclid: 'AA', fbclid_at: now });
  });

  it('persistClickIds writes nothing without fbclid or gclid', () => {
    const s = memStore();
    persistClickIds('tok', {}, { storage: s, now });
    expect(s.m.size).toBe(0);
  });

  it('isTrackingGranted accepts only granted', () => {
    expect(isTrackingGranted('granted')).toBe(true);
    expect(isTrackingGranted(' Granted ')).toBe(true);
    expect(isTrackingGranted('denied')).toBe(false);
    expect(isTrackingGranted(undefined)).toBe(false);
  });

  it('survives broken or throwing storage', () => {
    const bad = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
    };
    expect(captureClickIds('tok', '?fbclid=AA', { storage: bad, now })).toMatchObject({ fbclid: 'AA' });
    expect(captureClickIds('tok', '', { storage: memStore({ [clickIdsKey('tok')]: '{nope' }), now })).toEqual({});
    expect(captureClickIds('tok', '?fbclid=AA', { storage: null, now })).toMatchObject({ fbclid: 'AA' });
  });
});
