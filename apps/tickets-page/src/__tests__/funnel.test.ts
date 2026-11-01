import { describe, expect, it, vi } from 'vitest';
import { funnelURL, funnelViewBody, trackFunnelView } from '../lib/track.ts';

// The hosted page's share of the buyer funnel (spec 50, stage A7): one anonymous
// `view` beacon naming the event by its address. The widget inside the page
// sends session_open and seats_chosen itself (funnel="hosted_page").

const ctx = { apiBase: 'https://api.example/', slug: 'acme', eventSlug: 'spring-gala', search: '?utm_source=ig&utm_campaign=spring&x=1' };

describe('funnel view beacon', () => {
  it('names the page by its address and carries only the two utm values', () => {
    expect(JSON.parse(funnelViewBody(ctx))).toEqual({
      page: { slug: 'acme', event_slug: 'spring-gala' },
      step: 'view',
      surface: 'hosted_page',
      utm_source: 'ig',
      utm_campaign: 'spring',
    });
  });

  it('leaves the utm out when the address has none', () => {
    const body = JSON.parse(funnelViewBody({ ...ctx, search: '' }));
    expect(Object.keys(body).sort()).toEqual(['page', 'step', 'surface']);
  });

  it('builds the URL without a double slash', () => {
    expect(funnelURL(ctx)).toBe('https://api.example/v1/public/funnel');
    expect(funnelURL({ apiBase: '' })).toBe('/v1/public/funnel');
  });

  it('posts once, without credentials, and survives a failing request', async () => {
    const fetchMock = vi.fn(() => Promise.reject(new Error('offline')));
    expect(() => trackFunnelView(ctx, fetchMock as unknown as typeof fetch)).not.toThrow();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://api.example/v1/public/funnel');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('omit');
    expect(init.keepalive).toBe(true);
    await Promise.resolve();
  });

  it('survives a fetch that throws', () => {
    const throwing = vi.fn(() => {
      throw new Error('blocked');
    });
    expect(() => trackFunnelView(ctx, throwing as unknown as typeof fetch)).not.toThrow();
  });
});
