// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest';
import { createFunnel, funnelBody, funnelURL, parseFunnelHost } from './funnel.js';

const EVENT = '0190a1b2-c3d4-7e5f-8a6b-7c8d9e0f1a2b';
const SESSION = '0190a1b2-c3d4-7e5f-8a6b-7c8d9e0f1a2c';

function newFunnel(viewCountedByHost = false, touch = { utm_source: 'ig', utm_campaign: 'spring' }) {
  const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  const funnel = createFunnel({
    apiBase: 'https://api.example.test/',
    feedToken: 'tok_1',
    viewCountedByHost,
    touch: () => touch,
    fetchImpl: fetchImpl as unknown as typeof fetch,
  });
  return { funnel, fetchImpl };
}

describe('parseFunnelHost', () => {
  it('is true only when an Arena page around the widget counts the view', () => {
    expect(parseFunnelHost('hosted_page')).toBe(true);
    expect(parseFunnelHost(' Tickets_Page ')).toBe(true);
    expect(parseFunnelHost('widget')).toBe(false);
    expect(parseFunnelHost('')).toBe(false);
    expect(parseFunnelHost(null)).toBe(false);
    expect(parseFunnelHost('whatever')).toBe(false);
  });
});

describe('funnelURL / funnelBody', () => {
  it('builds the beacon URL without a double slash', () => {
    expect(funnelURL('https://api.example.test/')).toBe('https://api.example.test/v1/public/funnel');
    expect(funnelURL('')).toBe('/v1/public/funnel');
  });

  it('carries the event, step, surface, session and utm, and nothing about the visitor', () => {
    const body = JSON.parse(
      funnelBody({ feedToken: 'tok_1' }, 'session_open', EVENT, SESSION, { utm_source: 'ig', utm_campaign: 'spring' }),
    );
    expect(body).toEqual({
      feed_token: 'tok_1',
      event_id: EVENT,
      step: 'session_open',
      surface: 'widget',
      session_id: SESSION,
      utm_source: 'ig',
      utm_campaign: 'spring',
    });
  });

  it('leaves out an absent session and empty utm values', () => {
    const body = JSON.parse(funnelBody({ feedToken: 't' }, 'view', EVENT, undefined, { utm_source: '' }));
    expect(Object.keys(body).sort()).toEqual(['event_id', 'feed_token', 'step', 'surface']);
  });
});

describe('createFunnel', () => {
  it('posts the beacon without credentials, with keepalive', () => {
    const { funnel, fetchImpl } = newFunnel();
    expect(funnel.track('view', EVENT)).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://api.example.test/v1/public/funnel');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('omit');
    expect(init.keepalive).toBe(true);
  });

  it('counts a step once per event and session for the life of the widget', () => {
    const { funnel, fetchImpl } = newFunnel();
    expect(funnel.track('session_open', EVENT, SESSION)).toBe(true);
    expect(funnel.track('session_open', EVENT, SESSION)).toBe(false);
    expect(funnel.track('session_open', EVENT, '0190a1b2-c3d4-7e5f-8a6b-7c8d9e0f1a2d')).toBe(true);
    expect(funnel.track('seats_chosen', EVENT, SESSION)).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(3);
  });

  it('does not count the view when the page around the widget counts it', () => {
    const { funnel, fetchImpl } = newFunnel(true);
    expect(funnel.track('view', EVENT)).toBe(false);
    expect(funnel.track('session_open', EVENT, SESSION)).toBe(true);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it('sends nothing for the synthetic event of a session-only embed or without a token', () => {
    const { funnel, fetchImpl } = newFunnel();
    expect(funnel.track('view', '')).toBe(false);
    expect(funnel.track('view', 'not-a-uuid')).toBe(false);
    const noToken = createFunnel({
      apiBase: '', feedToken: '', viewCountedByHost: false, touch: () => null,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(noToken.track('view', EVENT)).toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it('never throws when the request fails or fetch is missing', async () => {
    const failing = vi.fn().mockRejectedValue(new Error('offline'));
    const a = createFunnel({ apiBase: '', feedToken: 't', viewCountedByHost: false, touch: () => null, fetchImpl: failing as unknown as typeof fetch });
    expect(() => a.track('view', EVENT)).not.toThrow();
    const throwing = vi.fn(() => {
      throw new Error('blocked');
    });
    const b = createFunnel({ apiBase: '', feedToken: 't', viewCountedByHost: false, touch: () => null, fetchImpl: throwing as unknown as typeof fetch });
    expect(() => b.track('view', EVENT)).not.toThrow();
    await Promise.resolve();
  });

  it('leaves no trace in the browser storage', () => {
    const { funnel } = newFunnel();
    funnel.track('view', EVENT);
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
    expect(document.cookie).toBe('');
  });
});
