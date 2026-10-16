import { describe, expect, it, vi } from 'vitest';
import { sendTrack, trackBody, trackPageView, trackPartnerClick, trackURL, trackVideoPlay, utmFromSearch } from '../lib/track.ts';

const ctx = { apiBase: 'https://api.example.test/', slug: 'acme', eventSlug: 'summer fest', search: '?utm_source=instagram&utm_campaign=spring' };

describe('trackURL', () => {
  it('builds the beacon address and encodes the slugs', () => {
    expect(trackURL(ctx)).toBe('https://api.example.test/v1/public/pages/acme/summer%20fest/track');
    expect(trackURL({ apiBase: '', slug: 'a/../b', eventSlug: 'x?y' })).toBe('/v1/public/pages/a%2F..%2Fb/x%3Fy/track');
  });
});

describe('utmFromSearch', () => {
  it('reads and trims the two values', () => {
    expect(utmFromSearch('?utm_source=%20insta%20&utm_campaign=spring&other=1')).toEqual({ utm_source: 'insta', utm_campaign: 'spring' });
  });
  it('leaves out what is absent or empty', () => {
    expect(utmFromSearch('')).toEqual({});
    expect(utmFromSearch('?utm_source=&utm_campaign=%20')).toEqual({});
  });
  it('cuts a long value to 64 characters', () => {
    expect(utmFromSearch('?utm_source=' + 'a'.repeat(200)).utm_source).toHaveLength(64);
  });
});

describe('trackBody', () => {
  it('carries the kind and the utm values, nothing else', () => {
    expect(JSON.parse(trackBody('page_view', ctx.search))).toEqual({ kind: 'page_view', utm_source: 'instagram', utm_campaign: 'spring' });
    expect(JSON.parse(trackBody('video_play', ''))).toEqual({ kind: 'video_play' });
  });
});

describe('sendTrack', () => {
  it('posts JSON without credentials and keeps the request alive', () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    sendTrack(ctx, 'partner_click', fetchMock as unknown as typeof fetch);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(trackURL(ctx));
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('omit');
    expect(init.keepalive).toBe(true);
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string).kind).toBe('partner_click');
  });

  it('never throws: a failing or missing fetch is silent', async () => {
    const failing = vi.fn().mockRejectedValue(new Error('offline'));
    expect(() => sendTrack(ctx, 'page_view', failing as unknown as typeof fetch)).not.toThrow();
    const throwing = vi.fn(() => {
      throw new Error('blocked');
    });
    expect(() => sendTrack(ctx, 'page_view', throwing as unknown as typeof fetch)).not.toThrow();
    await Promise.resolve();
  });

  it('exports the three entry points', () => {
    const f = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal('fetch', f);
    trackPageView(ctx);
    trackVideoPlay(ctx);
    trackPartnerClick(ctx);
    vi.unstubAllGlobals();
    expect(f.mock.calls.map((c) => JSON.parse((c[1] as RequestInit).body as string).kind)).toEqual(['page_view', 'video_play', 'partner_click']);
  });
});
