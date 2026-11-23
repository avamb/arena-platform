import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { parsePath } from '../lib/route.ts';
import { ApiError } from '../lib/api.ts';
import type { HostedPageEvent, HostedSeriesPageResponse } from '../lib/api.ts';
import {
  formatPriceFrom,
  renderSeriesPage,
  resolveEventOrSeries,
  summaryParts,
  videoFromURL,
} from '../lib/series.ts';

function event(over: Partial<HostedPageEvent> = {}): HostedPageEvent {
  return {
    id: 'e1',
    slug: 'prague',
    title: 'Prague show',
    description: null,
    short_description: null,
    image_url: null,
    poster_url: null,
    age_rating: null,
    venue_names: ['Hall'],
    first_session_at: '2026-10-16T12:00:00Z',
    last_session_at: '2026-10-16T14:00:00Z',
    first_session_timezone: 'Europe/Prague',
    ...over,
  };
}

function seriesData(over: Partial<HostedSeriesPageResponse['series']> = {}): HostedSeriesPageResponse {
  return {
    org: { slug: 'lamp', name: 'Lampyris', logo_url: null },
    default_locale: 'en',
    series: {
      slug: 'big-tour',
      name: 'Big Tour',
      description_html: '<p>About <strong>tour</strong></p>',
      partners_text_html: '<p>With friends</p>',
      poster_url: 'https://cdn.test/poster.jpg',
      hero_url: 'https://cdn.test/hero.jpg',
      hero_video_url: null,
      summary: {
        date_from: '2026-10-16T12:00:00Z',
        date_to: '2026-10-20T12:00:00Z',
        cities_count: 3,
        cities_on_sale_count: 2,
        price_from: [
          { currency: 'EUR', amount: 39 },
          { currency: 'CZK', amount: 990 },
        ],
      },
      ...over,
    },
    events: [event(), event({ id: 'e2', slug: 'brno', title: 'Brno show' })],
  };
}

function jsonResponse(status: number, body: unknown = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

beforeEach(() => {
  document.body.innerHTML = '';
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date('2026-09-01T12:00:00Z'));
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('routing: /<org>/<slug> serves events and tours alike', () => {
  it('a promoter or org prefix parses as the same two-segment route', () => {
    expect(parsePath('/lamp/big-tour')).toEqual({ kind: 'event', orgSlug: 'lamp', eventSlug: 'big-tour' });
  });
});

describe('resolveEventOrSeries', () => {
  it('returns the event when the event page answers', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { event: { id: 'x' } }));
    vi.stubGlobal('fetch', fetchMock);
    const r = await resolveEventOrSeries('', 'lamp', 'prague');
    expect(r.kind).toBe('event');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('asks for the series after an event 404', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(404))
      .mockResolvedValueOnce(jsonResponse(200, seriesData()));
    vi.stubGlobal('fetch', fetchMock);
    const r = await resolveEventOrSeries('https://api.test', 'lamp', 'big-tour');
    expect(r.kind).toBe('series');
    expect(fetchMock.mock.calls[0][0]).toBe('https://api.test/v1/public/pages/lamp/big-tour');
    expect(fetchMock.mock.calls[1][0]).toBe('https://api.test/v1/public/pages/lamp/series/big-tour');
  });

  it('event 404 and series 404 stay a 404 (the not-found screen)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(404)));
    await expect(resolveEventOrSeries('', 'lamp', 'nope')).rejects.toMatchObject({ status: 404 });
  });

  it('a server error of the event request does not try the series', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(500));
    vi.stubGlobal('fetch', fetchMock);
    await expect(resolveEventOrSeries('', 'lamp', 'x')).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe('summary and price formatting', () => {
  it('formats one price per currency for the locale', () => {
    expect(formatPriceFrom(39, 'EUR', 'ru')).toMatch(/^от 39\s€$/);
    expect(formatPriceFrom(990, 'CZK', 'cs')).toMatch(/^od 990\sKč$/);
    expect(formatPriceFrom(12.5, 'EUR', 'en')).toBe('from €12.50');
  });

  it('builds dates, cities, on-sale and prices in order', () => {
    const parts = summaryParts(seriesData().series.summary, 'en');
    expect(parts[0]).toContain('2026');
    expect(parts.slice(1, 3)).toEqual(['3 cities', 'on sale in 2 cities']);
    expect(parts).toHaveLength(5);
    expect(parts[3]).toContain('39');
    expect(parts[4]).toContain('990');
  });

  it('uses the Russian plural forms and leaves empty parts out', () => {
    expect(
      summaryParts({ date_from: null, date_to: null, cities_count: 2, cities_on_sale_count: 0, price_from: [] }, 'ru'),
    ).toEqual(['2 города']);
    expect(
      summaryParts({ date_from: null, date_to: null, cities_count: 5, cities_on_sale_count: 1, price_from: [] }, 'ru'),
    ).toEqual(['5 городов', 'в продаже в 1 городе']);
  });

  it('survives an unknown currency code', () => {
    expect(formatPriceFrom(5, 'ZZZZ', 'en')).toContain('5');
  });
});

describe('video address', () => {
  it('maps YouTube and Vimeo to privacy embeds', () => {
    expect(videoFromURL('https://www.youtube.com/watch?v=dQw4w9WgXcQ')?.embed_url).toBe(
      'https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ',
    );
    expect(videoFromURL('https://youtu.be/dQw4w9WgXcQ')?.provider).toBe('youtube');
    expect(videoFromURL('https://vimeo.com/123456789')?.embed_url).toBe('https://player.vimeo.com/video/123456789');
  });

  it('refuses other hosts and plain http', () => {
    expect(videoFromURL('https://evil.example/watch?v=dQw4w9WgXcQ')).toBeNull();
    expect(videoFromURL('http://youtu.be/dQw4w9WgXcQ')).toBeNull();
    expect(videoFromURL(null)).toBeNull();
  });
});

describe('renderSeriesPage', () => {
  it('renders name, hero image, poster, summary, description, event cards and partners', () => {
    const main = document.createElement('main');
    document.body.appendChild(main);
    renderSeriesPage(main, seriesData(), 'en');
    expect(main.querySelector('h1')?.textContent).toBe('Big Tour');
    expect(main.querySelector('.asa-series-hero__image')?.getAttribute('src')).toBe('https://cdn.test/hero.jpg');
    expect(main.querySelector('.asa-series-poster')?.getAttribute('src')).toBe('https://cdn.test/poster.jpg');
    expect(main.querySelector('.asa-series-summary')?.textContent).toContain('3 cities');
    expect(main.querySelector('.asa-series-description strong')?.textContent).toBe('tour');
    const links = Array.from(main.querySelectorAll('.asa-card-grid a.asa-card')).map((a) => a.getAttribute('href'));
    expect(links).toEqual(['/lamp/prague', '/lamp/brno']);
    expect(main.querySelector('.asa-series-partners')?.textContent).toContain('With friends');
    expect(main.querySelector('.asa-series-partners h2')?.textContent).toBe('Partners');
  });

  it('puts a press-to-play embed in the hero and no iframe before the press', () => {
    const main = document.createElement('main');
    document.body.appendChild(main);
    renderSeriesPage(main, seriesData({ hero_video_url: 'https://youtu.be/dQw4w9WgXcQ' }), 'ru');
    expect(main.querySelector('.asa-series-hero__image')).toBeNull();
    expect(main.querySelector('iframe')).toBeNull();
    (main.querySelector('.asa-video__play') as HTMLButtonElement).click();
    expect(main.querySelector('iframe')?.getAttribute('src')).toContain('youtube-nocookie.com/embed/dQw4w9WgXcQ');
    expect(main.querySelector('h2.asa-dates__head')?.textContent).toBe('Даты и города');
  });

  it('does not render script from the organizer text', () => {
    const main = document.createElement('main');
    document.body.appendChild(main);
    renderSeriesPage(main, seriesData({ description_html: '<p>ok</p><script>window.x=1</script>' }), 'en');
    expect(main.querySelector('script')).toBeNull();
  });
});
