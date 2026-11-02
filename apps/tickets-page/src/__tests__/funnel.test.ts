import { describe, expect, it, vi } from 'vitest';
import { funnelURL, funnelViewBody, observeOnce, trackFunnelView } from '../lib/track.ts';

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

  it('files a promoter-page card under its own surface', () => {
    expect(JSON.parse(funnelViewBody(ctx, 'tickets_page')).surface).toBe('tickets_page');
    expect(JSON.parse(funnelViewBody(ctx)).surface).toBe('hosted_page');
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
    expect(() => trackFunnelView(ctx, 'hosted_page', fetchMock as unknown as typeof fetch)).not.toThrow();
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
    expect(() => trackFunnelView(ctx, 'hosted_page', throwing as unknown as typeof fetch)).not.toThrow();
  });
});

describe('observeOnce', () => {
  class FakeObserver {
    static last: FakeObserver;
    disconnected = false;
    constructor(private cb: (e: Array<{ isIntersecting: boolean }>) => void) {
      FakeObserver.last = this;
    }
    observe() {}
    disconnect() {
      this.disconnected = true;
    }
    fire(isIntersecting: boolean) {
      this.cb([{ isIntersecting }]);
    }
  }
  const asObserver = FakeObserver as unknown as typeof IntersectionObserver;

  it('calls back once, only when the element is in view', () => {
    const fn = vi.fn();
    observeOnce(document.createElement('div'), fn, asObserver);
    expect(fn).not.toHaveBeenCalled();
    FakeObserver.last.fire(false);
    expect(fn).not.toHaveBeenCalled();
    FakeObserver.last.fire(true);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(FakeObserver.last.disconnected).toBe(true);
  });

  it('counts the card at once where there is no IntersectionObserver', () => {
    const fn = vi.fn();
    observeOnce(document.createElement('div'), fn, undefined);
    // `undefined` falls back to the global; jsdom has none, so it fires.
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
