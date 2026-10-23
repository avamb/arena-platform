import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderMaterials } from '../lib/materials.ts';
import { trackPartnerClick, trackVideoPlay } from '../lib/track.ts';
import type { HostedPageMaterials } from '../lib/api.ts';

// The hooks main.ts passes to renderEvent: a video start and a partner-link
// click each send one beacon of their kind for THIS event (task 2-3).

const MATERIALS: HostedPageMaterials = {
  gallery: [],
  videos: [
    {
      provider: 'youtube',
      video_id: 'abc123',
      embed_url: 'https://www.youtube-nocookie.com/embed/abc123',
      url: 'https://www.youtube.com/watch?v=abc123',
    },
  ],
  partner_links: [{ url: 'https://tickets.other.example/e/1', seller_name: 'Other Tickets' }],
  map_point: null,
  report_photos: [],
};

const ctx = { apiBase: 'https://api.example', slug: 'org', eventSlug: 'show', search: '?utm_source=ig&utm_campaign=spring' };

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('materials hooks send the page statistics beacon', () => {
  it('a video start sends video_play, a partner click sends partner_click, both with the utm pair', () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })));
    vi.stubGlobal('fetch', fetchMock);
    const root = renderMaterials(MATERIALS, 'en', [], {
      onVideoPlay: () => trackVideoPlay(ctx),
      onPartnerClick: () => trackPartnerClick(ctx),
    })!;

    expect(fetchMock).not.toHaveBeenCalled();

    root.querySelector<HTMLButtonElement>('.asa-video__play')!.click();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const a = root.querySelector<HTMLAnchorElement>('.asa-partner__link')!;
    a.addEventListener('click', (e) => e.preventDefault());
    a.click();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    const calls = fetchMock.mock.calls as unknown as Array<[string, RequestInit]>;
    for (const [url] of calls) {
      expect(url).toBe('https://api.example/v1/public/pages/org/show/track');
    }
    expect(JSON.parse(String(calls[0][1].body))).toEqual({ kind: 'video_play', utm_source: 'ig', utm_campaign: 'spring' });
    expect(JSON.parse(String(calls[1][1].body))).toEqual({ kind: 'partner_click', utm_source: 'ig', utm_campaign: 'spring' });
    expect(calls[0][1].credentials).toBe('omit');
  });

  it('a failing beacon never breaks playback', () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))));
    const root = renderMaterials(MATERIALS, 'en', [], { onVideoPlay: () => trackVideoPlay(ctx) })!;
    expect(() => root.querySelector<HTMLButtonElement>('.asa-video__play')!.click()).not.toThrow();
    expect(root.querySelector('iframe')).not.toBeNull();
  });
});
