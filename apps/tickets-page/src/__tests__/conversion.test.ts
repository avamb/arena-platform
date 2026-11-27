import { beforeEach, describe, expect, it, vi } from 'vitest';
import { buyerContext, conversionBody, conversionURL, initConversion, newEventId, setConversionFetch, trackTicketsClick } from '../lib/conversion.ts';
import { CONSENT_KEY, CONSENT_VERSION, initTracking } from '../lib/tracking.ts';

function mount(): void {
  document.body.innerHTML = '<div class="asa-footer__in"><p id="asa-footer-text"></p></div>';
}

function stored(granted: boolean): void {
  localStorage.setItem(CONSENT_KEY, JSON.stringify({ granted, version: CONSENT_VERSION, at: '2026-11-27T10:00:00.000Z' }));
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  localStorage.clear();
  mount();
  initTracking({ enabled: false, locale: 'en' }); // resets the module's remembered choice
  fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })));
  setConversionFetch(fetchMock as unknown as typeof fetch);
});

const base = { apiBase: 'https://api.test/', slug: 'acme', eventSlug: 'fest' };

describe('urls and body', () => {
  it('builds event and series urls', () => {
    expect(conversionURL(base)).toBe('https://api.test/v1/public/pages/acme/fest/conversion');
    expect(conversionURL({ apiBase: '', slug: 'a', seriesSlug: 'tour 1' })).toBe('/v1/public/pages/a/series/tour%201/conversion');
  });
  it('event ids are valid and fresh', () => {
    const a = newEventId();
    expect(a).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(newEventId()).not.toBe(a);
  });
  it('buyer context reads cookies, fbclid and page url', () => {
    const bc = buyerContext('x=1; _fbp=fb.1.1.2; _fbc=fb.1.1.abc', '?fbclid=abc', { origin: 'https://t.test', pathname: '/acme/fest' });
    expect(bc).toEqual({
      tracking_consent: true,
      tracking_consent_text_version: CONSENT_VERSION,
      fbp: 'fb.1.1.2',
      fbc: 'fb.1.1.abc',
      fbclid: 'abc',
      page_url: 'https://t.test/acme/fest',
    });
    expect(buyerContext('', '', { origin: 'https://t.test', pathname: '/p' })).not.toHaveProperty('fbp');
    const body = JSON.parse(conversionBody('view_content', 'id1', bc));
    expect(body.kind).toBe('view_content');
    expect(body.event_id).toBe('id1');
  });
});

describe('sending', () => {
  it('sends one ViewContent with the right shape when consent is stored', () => {
    stored(true);
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('https://api.test/v1/public/pages/acme/fest/conversion');
    expect(init.method).toBe('POST');
    expect(init.keepalive).toBe(true);
    expect(init.credentials).toBe('omit');
    const body = JSON.parse(init.body as string);
    expect(body.kind).toBe('view_content');
    expect(body.event_id).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
    expect(body.buyer_context.tracking_consent).toBe(true);
    expect(body.buyer_context.tracking_consent_text_version).toBe(CONSENT_VERSION);
    expect(body.buyer_context.page_url).toMatch(/^https?:\/\/[^?#]+$/);
  });

  it('sends nothing without a choice, when denied, or with tracking off', () => {
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    trackTicketsClick();
    expect(fetchMock).not.toHaveBeenCalled();

    stored(false);
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    trackTicketsClick();
    expect(fetchMock).not.toHaveBeenCalled();

    stored(true);
    initConversion({ ...base, enabled: false });
    initTracking({ enabled: false, locale: 'en' });
    trackTicketsClick();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('sends ViewContent once, when the banner is accepted after load', () => {
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    expect(fetchMock).not.toHaveBeenCalled();
    (document.querySelector('.asa-cookie__btn--accept') as HTMLButtonElement).click();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(JSON.parse((fetchMock.mock.calls[0][1] as RequestInit).body as string).kind).toBe('view_content');
  });

  it('a decline later sends nothing', () => {
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    (document.querySelector('.asa-cookie__btn--decline') as HTMLButtonElement).click();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('a tour page posts to the series route', () => {
    stored(true);
    initConversion({ apiBase: '', slug: 'acme', seriesSlug: 'tour', enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    expect(fetchMock.mock.calls[0][0]).toBe('/v1/public/pages/acme/series/tour/conversion');
  });

  it('sends TicketsClick on a marked element and on an anchor to the widget, with a new event id', () => {
    stored(true);
    initConversion({ ...base, enabled: true });
    initTracking({ enabled: true, locale: 'en' });
    fetchMock.mockClear();
    document.body.insertAdjacentHTML('beforeend', '<a id="c" data-asa-tickets-click="1" href="/x"><span id="in">Buy</span></a><a id="w" href="#asa-widget">Tickets</a><a id="o" href="/o">other</a>');
    (document.getElementById('in') as HTMLElement).click();
    (document.getElementById('w') as HTMLElement).click();
    (document.getElementById('o') as HTMLElement).click();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const bodies = fetchMock.mock.calls.map((c) => JSON.parse((c[1] as RequestInit).body as string));
    expect(bodies.map((b) => b.kind)).toEqual(['tickets_click', 'tickets_click']);
    expect(bodies[0].event_id).not.toBe(bodies[1].event_id);
  });

  it('a failing request is silent', () => {
    stored(true);
    setConversionFetch((() => Promise.reject(new Error('down'))) as unknown as typeof fetch);
    initTracking({ enabled: true, locale: 'en' });
    expect(() => initConversion({ ...base, enabled: true })).not.toThrow();
    initTracking({ enabled: true, locale: 'en' });
  });
});
