/**
 * attribution.ts — the 7-day window, the first touch and the storage guards
 * (spec 43 section 5.1). Runs in node: storage is a mock.
 */

import { afterEach, describe, it, expect } from 'vitest';
import {
  ATTRIBUTION_WINDOW_MS,
  attributionKey,
  captureAttribution,
  parseStored,
  touchFromSearch,
} from './attribution.js';

function mockStorage(): Storage & { dump(): Record<string, string> } {
  const store: Record<string, string> = {};
  return {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v; },
    removeItem: (k: string) => { delete store[k]; },
    clear: () => { for (const k of Object.keys(store)) delete store[k]; },
    key: (i: number) => Object.keys(store)[i] ?? null,
    get length() { return Object.keys(store).length; },
    dump: () => ({ ...store }),
  } as Storage & { dump(): Record<string, string> };
}

const TOKEN = 'feed-abc';
const T0 = new Date('2026-10-24T10:00:00.000Z');
const DAY = 24 * 60 * 60 * 1000;

describe('touchFromSearch', () => {
  it('reads ref and every utm label', () => {
    const t = touchFromSearch('?ref=x7k2qpm&utm_source=qr&utm_medium=poster&utm_campaign=autumn&utm_content=a&utm_term=b', T0);
    expect(t).toEqual({
      ref: 'x7k2qpm',
      utm_source: 'qr',
      utm_medium: 'poster',
      utm_campaign: 'autumn',
      utm_content: 'a',
      utm_term: 'b',
      landed_at: T0.toISOString(),
    });
  });

  it('is null for an address with no touch', () => {
    expect(touchFromSearch('', T0)).toBeNull();
    expect(touchFromSearch('?checkout_token=abc', T0)).toBeNull();
    expect(touchFromSearch('?utm_medium=poster', T0)).toBeNull();
  });

  it('drops a value the server would drop and keeps the rest', () => {
    const t = touchFromSearch('?ref=ABCDEFG&utm_source=a%20b&utm_campaign=ok', T0);
    expect(t).toEqual({ ref: 'abcdefg', utm_campaign: 'ok', landed_at: T0.toISOString() });
    expect(touchFromSearch('?utm_source=' + 'x'.repeat(65), T0)).toBeNull();
  });

  it('leaves the other query keys alone', () => {
    const t = touchFromSearch('?checkout_token=abc&session=1&ref=x7k2qpm', T0);
    expect(t?.ref).toBe('x7k2qpm');
  });
});

describe('captureAttribution', () => {
  it('stores a touch from the address under the feed token for 7 days', () => {
    const storage = mockStorage();
    const a = captureAttribution(TOKEN, '?ref=x7k2qpm&utm_source=qr', { storage, now: T0 });
    expect(a).toEqual({ ref: 'x7k2qpm', utm_source: 'qr', landed_at: T0.toISOString() });
    const raw = JSON.parse(storage.dump()[attributionKey(TOKEN)]!);
    expect(raw.expires_at).toBe(new Date(T0.getTime() + ATTRIBUTION_WINDOW_MS).toISOString());
    expect(raw.ref).toBe('x7k2qpm');
  });

  it('does not send expires_at or empty fields to the server', () => {
    const a = captureAttribution(TOKEN, '?utm_source=qr', { storage: mockStorage(), now: T0 });
    expect(a).not.toHaveProperty('expires_at');
    expect(a).not.toHaveProperty('ref');
    expect(a).not.toHaveProperty('first');
  });

  it('returns the stored touch when the address names nothing', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=x7k2qpm', { storage, now: T0 });
    const later = new Date(T0.getTime() + 3 * DAY);
    const a = captureAttribution(TOKEN, '', { storage, now: later });
    expect(a).toEqual({ ref: 'x7k2qpm', landed_at: T0.toISOString() });
  });

  it('forgets a touch past its expiry', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=x7k2qpm', { storage, now: T0 });
    const later = new Date(T0.getTime() + ATTRIBUTION_WINDOW_MS + 1000);
    expect(captureAttribution(TOKEN, '', { storage, now: later })).toBeNull();
    expect(storage.dump()[attributionKey(TOKEN)]).toBeUndefined();
  });

  it('still keeps a touch one second before the expiry', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=x7k2qpm', { storage, now: T0 });
    const later = new Date(T0.getTime() + ATTRIBUTION_WINDOW_MS - 1000);
    expect(captureAttribution(TOKEN, '', { storage, now: later })?.ref).toBe('x7k2qpm');
  });

  it('moves a touch from another source into first', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa&utm_source=qr&utm_campaign=spring', { storage, now: T0 });
    const t1 = new Date(T0.getTime() + DAY);
    const a = captureAttribution(TOKEN, '?utm_source=facebook', { storage, now: t1 });
    expect(a).toEqual({
      utm_source: 'facebook',
      landed_at: t1.toISOString(),
      first: { ref: 'aaaaaaa', utm_source: 'qr', utm_campaign: 'spring', landed_at: T0.toISOString() },
    });
  });

  it('keeps the very first touch through a third one', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa', { storage, now: T0 });
    captureAttribution(TOKEN, '?ref=bbbbbbb', { storage, now: new Date(T0.getTime() + DAY) });
    const t2 = new Date(T0.getTime() + 2 * DAY);
    const a = captureAttribution(TOKEN, '?ref=ccccccc', { storage, now: t2 });
    expect(a?.ref).toBe('ccccccc');
    expect(a?.first).toEqual({ ref: 'aaaaaaa', landed_at: T0.toISOString() });
  });

  it('does not make a first touch of the same source, and keeps an earlier first', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa', { storage, now: T0 });
    captureAttribution(TOKEN, '?ref=bbbbbbb', { storage, now: new Date(T0.getTime() + DAY) });
    const t2 = new Date(T0.getTime() + 2 * DAY);
    const a = captureAttribution(TOKEN, '?ref=bbbbbbb', { storage, now: t2 });
    expect(a?.landed_at).toBe(t2.toISOString());
    expect(a?.first?.ref).toBe('aaaaaaa');
    const fresh = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa', { storage: fresh, now: T0 });
    expect(captureAttribution(TOKEN, '?ref=aaaaaaa', { storage: fresh, now: t2 })).not.toHaveProperty('first');
  });

  it('moves the touch into first when only utm_campaign changes', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?utm_source=facebook&utm_campaign=spring', { storage, now: T0 });
    const t1 = new Date(T0.getTime() + DAY);
    const a = captureAttribution(TOKEN, '?utm_source=facebook&utm_campaign=autumn', { storage, now: t1 });
    expect(a).toEqual({
      utm_source: 'facebook',
      utm_campaign: 'autumn',
      landed_at: t1.toISOString(),
      first: { utm_source: 'facebook', utm_campaign: 'spring', landed_at: T0.toISOString() },
    });
  });

  it('forgets an expired stored touch before it can become first', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa', { storage, now: T0 });
    const later = new Date(T0.getTime() + 8 * DAY);
    expect(captureAttribution(TOKEN, '?ref=bbbbbbb', { storage, now: later })).not.toHaveProperty('first');
  });

  it('scopes the record by feed token', () => {
    const storage = mockStorage();
    captureAttribution('site-a', '?ref=aaaaaaa', { storage, now: T0 });
    expect(captureAttribution('site-b', '', { storage, now: T0 })).toBeNull();
    expect(captureAttribution('site-a', '', { storage, now: T0 })?.ref).toBe('aaaaaaa');
  });

  it('ignores a medium-only address and keeps the stored touch', () => {
    const storage = mockStorage();
    captureAttribution(TOKEN, '?ref=aaaaaaa', { storage, now: T0 });
    expect(captureAttribution(TOKEN, '?utm_medium=poster', { storage, now: T0 })?.ref).toBe('aaaaaaa');
  });

  it('ignores broken stored data', () => {
    const storage = mockStorage();
    storage.setItem(attributionKey(TOKEN), '{not json');
    expect(captureAttribution(TOKEN, '', { storage, now: T0 })).toBeNull();
    storage.setItem(attributionKey(TOKEN), JSON.stringify({ ref: 'aaaaaaa' }));
    expect(captureAttribution(TOKEN, '', { storage, now: T0 })).toBeNull();
  });

  it('has nothing to do without a feed token but still reads the address', () => {
    const storage = mockStorage();
    const a = captureAttribution('', '?ref=aaaaaaa', { storage, now: T0 });
    expect(a?.ref).toBe('aaaaaaa');
    expect(Object.keys(storage.dump())).toHaveLength(0);
  });

  it('returns the touch of the address when storage throws', () => {
    const broken = {
      getItem: () => { throw new Error('denied'); },
      setItem: () => { throw new Error('denied'); },
      removeItem: () => { throw new Error('denied'); },
    } as unknown as Storage;
    const a = captureAttribution(TOKEN, '?ref=x7k2qpm&utm_source=qr', { storage: broken, now: T0 });
    expect(a).toEqual({ ref: 'x7k2qpm', utm_source: 'qr', landed_at: T0.toISOString() });
    expect(captureAttribution(TOKEN, '', { storage: broken, now: T0 })).toBeNull();
  });

  it('works with no storage at all', () => {
    const a = captureAttribution(TOKEN, '?utm_source=qr', { storage: null, now: T0 });
    expect(a?.utm_source).toBe('qr');
  });
});

describe('parseStored', () => {
  const stored = (extra: Record<string, unknown>): string =>
    JSON.stringify({
      landed_at: T0.toISOString(),
      expires_at: new Date(T0.getTime() + DAY).toISOString(),
      ...extra,
    });

  it('is null for an empty value', () => {
    expect(parseStored(null, T0)).toBeNull();
    expect(parseStored('', T0)).toBeNull();
  });

  it('checks the stored values by the same rules as the address', () => {
    expect(parseStored(stored({ ref: 'bad value!' }), T0)).toBeNull();
    const p = parseStored(stored({ ref: 'ABCDEFG', utm_source: 'qr', utm_medium: 'x'.repeat(65) }), T0);
    expect(p?.ref).toBe('abcdefg');
    expect(p?.utm_source).toBe('qr');
    expect(p).not.toHaveProperty('utm_medium');
    const f = parseStored(stored({ ref: 'aaaaaaa', first: { ref: '<b>', utm_source: 'qr' } }), T0);
    expect(f?.first).toEqual({ utm_source: 'qr' });
  });
});

describe('default storage', () => {
  const desc = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');

  afterEach(() => {
    if (desc) Object.defineProperty(globalThis, 'localStorage', desc);
    else delete (globalThis as Record<string, unknown>)['localStorage'];
  });

  it('survives a localStorage that throws on access', () => {
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('SecurityError');
      },
    });
    const a = captureAttribution(TOKEN, '?ref=x7k2qpm', { now: T0 });
    expect(a).toEqual({ ref: 'x7k2qpm', landed_at: T0.toISOString() });
    expect(captureAttribution(TOKEN, '', { now: T0 })).toBeNull();
  });

  it('uses the global localStorage when there is one', () => {
    const storage = mockStorage();
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: storage });
    captureAttribution(TOKEN, '?ref=x7k2qpm', { now: T0 });
    expect(storage.dump()[attributionKey(TOKEN)]).toBeDefined();
    expect(captureAttribution(TOKEN, '', { now: T0 })?.ref).toBe('x7k2qpm');
  });
});
