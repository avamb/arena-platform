import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { EMBED_RULES, httpsURL, mapLinks, renderMaterials, safeEmbedURL } from '../lib/materials.ts';
import { renderEvent } from '../lib/render.ts';
import { PAGE_LOCALES } from '../lib/locale.ts';
import { t } from '../lib/i18n.ts';
import type { HostedPageMaterials, HostedPageResponse, HostedPageVideo } from '../lib/api.ts';

const EMPTY: HostedPageMaterials = { gallery: [], videos: [], partner_links: [], map_point: null, report_photos: [] };

function photo(n: number, caption: string | null = null) {
  return { media_id: `m${n}`, url: `https://api.example/v1/media-files/m${n}?sig=x`, caption, width: 800, height: 600 };
}

const YT: HostedPageVideo = {
  provider: 'youtube',
  video_id: 'abc123',
  embed_url: 'https://www.youtube-nocookie.com/embed/abc123',
  url: 'https://www.youtube.com/watch?v=abc123',
};

function page(materials?: HostedPageMaterials | null): HostedPageResponse {
  return {
    org: { slug: 'org', name: 'Org', logo_url: null },
    event: {
      id: 'e1',
      slug: 'show',
      title: 'Show',
      description: null,
      short_description: null,
      image_url: null,
      poster_url: null,
      age_rating: null,
      venue_names: ['Palác Akropolis'],
      first_session_at: null,
      last_session_at: null,
      first_session_timezone: null,
    },
    feed_token: 'tok',
    default_locale: 'en',
    ...(materials === undefined ? {} : { materials }),
  };
}

beforeEach(() => {
  document.body.innerHTML = '';
  document.documentElement.dir = 'ltr';
});

describe('renderMaterials — which sections appear', () => {
  it('renders nothing for a missing or empty block', () => {
    expect(renderMaterials(undefined, 'en', [])).toBeNull();
    expect(renderMaterials(null, 'en', [])).toBeNull();
    expect(renderMaterials(EMPTY, 'en', [])).toBeNull();
  });

  it('renders only the sections that have data', () => {
    const root = renderMaterials({ ...EMPTY, gallery: [photo(1)] }, 'en', [])!;
    expect(root.querySelectorAll('section')).toHaveLength(1);
    expect(root.querySelector('.asa-materials__gallery h2')?.textContent).toBe('Photos');
    expect(root.querySelector('.asa-materials__videos')).toBeNull();
    expect(root.querySelector('.asa-materials__map')).toBeNull();
    expect(root.querySelector('.asa-materials__partners')).toBeNull();
    expect(root.querySelector('.asa-materials__report')).toBeNull();
  });

  it('renders all five sections in order', () => {
    const root = renderMaterials(
      {
        gallery: [photo(1)],
        videos: [YT],
        partner_links: [{ url: 'https://other.example/e', seller_name: 'Other Tickets' }],
        map_point: { latitude: 50.0755, longitude: 14.4378, label: null },
        report_photos: [photo(2)],
      },
      'en',
      [],
    )!;
    const classes = [...root.querySelectorAll('section')].map((s) => s.className.split(' ')[1]);
    expect(classes).toEqual(['asa-materials__gallery', 'asa-materials__videos', 'asa-materials__map', 'asa-materials__partners', 'asa-materials__report']);
  });

  it('labels every section in every page locale', () => {
    for (const loc of PAGE_LOCALES) {
      const s = t(loc);
      for (const key of ['materialsGallery', 'materialsVideos', 'materialsMap', 'materialsPartners', 'materialsReport', 'videoPlay', 'photoOpen', 'lightboxClose'] as const) {
        expect(s[key], `${loc}.${key}`).toBeTruthy();
      }
    }
    expect(t('ru').materialsReport).toBe('Фотоотчёт');
  });
});

describe('gallery and photo report', () => {
  it('lazy-loads thumbnails and uses the caption as alt text', () => {
    const root = renderMaterials({ ...EMPTY, gallery: [photo(1, 'The hall'), photo(2)] }, 'en', [])!;
    const imgs = root.querySelectorAll<HTMLImageElement>('img');
    expect(imgs).toHaveLength(2);
    expect(imgs[0]!.loading).toBe('lazy');
    expect(imgs[0]!.alt).toBe('The hall');
    expect(imgs[1]!.alt).toBe('Photos 2');
  });

  it('opens a lightbox, steps through photos, closes on Escape and returns the focus', () => {
    const root = renderMaterials({ ...EMPTY, gallery: [photo(1, 'One'), photo(2, 'Two')] }, 'en', [])!;
    document.body.appendChild(root);
    const first = root.querySelector<HTMLButtonElement>('.asa-photos__btn')!;
    first.focus();
    first.click();
    const box = document.querySelector('.asa-lightbox')!;
    expect(box.getAttribute('role')).toBe('dialog');
    expect(box.querySelector('img')?.getAttribute('alt')).toBe('One');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    expect(box.querySelector('img')?.getAttribute('alt')).toBe('Two');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    expect(box.querySelector('img')?.getAttribute('alt')).toBe('One');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(document.querySelector('.asa-lightbox')).toBeNull();
    expect(document.body.classList.contains('asa-contact-open')).toBe(false);
    expect(document.activeElement).toBe(first);
  });

  it('swaps the arrow keys on the mirrored page', () => {
    document.documentElement.dir = 'rtl';
    const root = renderMaterials({ ...EMPTY, gallery: [photo(1, 'One'), photo(2, 'Two')] }, 'he', [])!;
    document.body.appendChild(root);
    root.querySelector<HTMLButtonElement>('.asa-photos__btn')!.click();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
    expect(document.querySelector('.asa-lightbox img')?.getAttribute('alt')).toBe('Two');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  });

  it('closes on the close button and hides the arrows for a single photo', () => {
    const root = renderMaterials({ ...EMPTY, report_photos: [photo(1)] }, 'en', [])!;
    document.body.appendChild(root);
    expect(root.querySelector('.asa-materials__report h2')?.textContent).toBe('Photo report');
    root.querySelector<HTMLButtonElement>('.asa-photos__btn')!.click();
    expect(document.querySelector<HTMLElement>('.asa-lightbox__prev')!.hidden).toBe(true);
    document.querySelector<HTMLButtonElement>('.asa-lightbox__close')!.click();
    expect(document.querySelector('.asa-lightbox')).toBeNull();
  });
});

describe('videos — nothing third-party before the click', () => {
  it('shows a play button, no iframe, and a link to the original', () => {
    const root = renderMaterials({ ...EMPTY, videos: [YT] }, 'en', [])!;
    expect(root.querySelector('iframe')).toBeNull();
    expect(root.querySelector('.asa-video__play')?.textContent).toContain('Play video');
    const link = root.querySelector<HTMLAnchorElement>('.asa-video__link')!;
    expect(link.href).toBe('https://www.youtube.com/watch?v=abc123');
    expect(link.rel).toBe('noopener noreferrer');
  });

  it('creates the iframe only after the click, on the nocookie host, and calls the hook', () => {
    const onVideoPlay = vi.fn();
    const root = renderMaterials({ ...EMPTY, videos: [YT] }, 'en', [], { onVideoPlay })!;
    root.querySelector<HTMLButtonElement>('.asa-video__play')!.click();
    const frame = root.querySelector<HTMLIFrameElement>('iframe')!;
    expect(new URL(frame.src).hostname).toBe('www.youtube-nocookie.com');
    expect(frame.getAttribute('referrerpolicy')).toBe('strict-origin-when-cross-origin');
    expect(frame.getAttribute('sandbox')).toContain('allow-scripts');
    expect(root.querySelector('.asa-video__play')).toBeNull();
    expect(onVideoPlay).toHaveBeenCalledWith('youtube');
  });

  it('a throwing hook does not break playback', () => {
    const root = renderMaterials({ ...EMPTY, videos: [YT] }, 'en', [], {
      onVideoPlay: () => {
        throw new Error('boom');
      },
    })!;
    expect(() => root.querySelector<HTMLButtonElement>('.asa-video__play')!.click()).not.toThrow();
    expect(root.querySelector('iframe')).not.toBeNull();
  });

  it('adds dnt=1 to Vimeo and accepts Rutube', () => {
    const vimeo = safeEmbedURL({ provider: 'vimeo', video_id: '1', embed_url: 'https://player.vimeo.com/video/1', url: 'https://vimeo.com/1' })!;
    expect(new URL(vimeo).searchParams.get('dnt')).toBe('1');
    const rutube = safeEmbedURL({ provider: 'rutube', video_id: 'x', embed_url: 'https://rutube.ru/play/embed/x', url: 'https://rutube.ru/video/x/' });
    expect(rutube).toBe('https://rutube.ru/play/embed/x');
  });

  it('refuses an embed address outside the allow-list and falls back to a link', () => {
    const evil: HostedPageVideo = { ...YT, embed_url: 'https://evil.example/embed/abc123' };
    expect(safeEmbedURL(evil)).toBeNull();
    expect(safeEmbedURL({ ...YT, embed_url: 'http://www.youtube-nocookie.com/embed/abc' })).toBeNull();
    expect(safeEmbedURL({ ...YT, embed_url: 'https://www.youtube-nocookie.com/watch?v=1' })).toBeNull();
    expect(safeEmbedURL({ ...YT, embed_url: 'javascript:alert(1)' })).toBeNull();
    const root = renderMaterials({ ...EMPTY, videos: [evil] }, 'en', [])!;
    expect(root.querySelector('.asa-video__play')).toBeNull();
    expect(root.querySelector('.asa-video__link')?.textContent).toContain('www.youtube.com');
  });

  it('an https provider is a plain link, and an unsafe link is dropped', () => {
    const plain: HostedPageVideo = { provider: 'https', video_id: null, embed_url: null, url: 'https://videos.example/clip' };
    const root = renderMaterials({ ...EMPTY, videos: [plain] }, 'en', [])!;
    expect(root.querySelector('iframe')).toBeNull();
    expect(root.querySelector<HTMLAnchorElement>('.asa-video__link')?.href).toBe('https://videos.example/clip');
    expect(renderMaterials({ ...EMPTY, videos: [{ ...plain, url: 'javascript:alert(1)' }] }, 'en', [])).toBeNull();
  });
});

describe('map — links and text, never an embed', () => {
  it('prints the coordinates and two links', () => {
    const root = renderMaterials({ ...EMPTY, map_point: { latitude: 50.0755, longitude: 14.4378, label: 'Main entrance' } }, 'en', ['Hall'])!;
    const map = root.querySelector('.asa-materials__map')!;
    expect(map.querySelector('h2')?.textContent).toBe('How to get there');
    expect(map.textContent).toContain('Main entrance');
    expect(map.textContent).toContain('50.07550, 14.43780');
    const hrefs = [...map.querySelectorAll<HTMLAnchorElement>('a')].map((a) => a.href);
    expect(hrefs).toEqual([
      'https://www.openstreetmap.org/?mlat=50.0755&mlon=14.4378#map=17/50.0755/14.4378',
      'https://www.google.com/maps/search/?api=1&query=50.0755,14.4378',
    ]);
    expect(map.querySelector('iframe, script')).toBeNull();
  });

  it('falls back to the venue names and rejects impossible coordinates', () => {
    const root = renderMaterials({ ...EMPTY, map_point: { latitude: 1, longitude: 2, label: null } }, 'en', ['Palác Akropolis'])!;
    expect(root.querySelector('.asa-map__place')?.textContent).toBe('Palác Akropolis');
    expect(mapLinks({ latitude: 91, longitude: 0, label: null })).toBeNull();
    expect(mapLinks({ latitude: 0, longitude: 181, label: null })).toBeNull();
    expect(mapLinks({ latitude: Number.NaN, longitude: 0, label: null })).toBeNull();
    expect(renderMaterials({ ...EMPTY, map_point: { latitude: 91, longitude: 0, label: null } }, 'en', [])).toBeNull();
  });
});

describe('partners', () => {
  it('shows the seller, opens in a new tab with noopener nofollow and calls the hook with the host', () => {
    const onPartnerClick = vi.fn();
    const root = renderMaterials(
      { ...EMPTY, partner_links: [{ url: 'https://tickets.other.example/e/1', seller_name: 'Other Tickets' }] },
      'en',
      [],
      { onPartnerClick },
    )!;
    expect(root.querySelector('.asa-partner__seller')?.textContent).toBe('Seller: Other Tickets');
    const a = root.querySelector<HTMLAnchorElement>('.asa-partner__link')!;
    expect(a.target).toBe('_blank');
    expect(a.rel).toBe('noopener nofollow');
    a.addEventListener('click', (e) => e.preventDefault());
    a.click();
    expect(onPartnerClick).toHaveBeenCalledWith('tickets.other.example');
  });

  it('localizes the seller label and drops a non-https link', () => {
    const root = renderMaterials(
      { ...EMPTY, partner_links: [{ url: 'https://a.example/x', seller_name: '' }, { url: 'http://b.example/x', seller_name: 'B' }] },
      'ru',
      [],
    )!;
    const rows = root.querySelectorAll('.asa-partner');
    expect(rows).toHaveLength(1);
    expect(rows[0]!.querySelector('.asa-partner__seller')?.textContent).toBe('Продавец: a.example');
  });
});

describe('renderEvent integration', () => {
  it('adds the block after the purchase card and leaves the existing page untouched', () => {
    const container = document.createElement('main');
    document.body.appendChild(container);
    const without = document.createElement('main');
    document.body.appendChild(without);
    renderEvent(without, page(), 'en', { apiBase: '', resumingCheckout: false });
    renderEvent(container, page({ ...EMPTY, gallery: [photo(1)] }), 'en', { apiBase: '', resumingCheckout: false });
    expect(without.querySelector('.asa-materials')).toBeNull();
    expect(container.children.length).toBe(without.children.length + 1);
    const idx = (cls: string) => [...container.children].findIndex((c) => c.classList.contains(cls));
    expect(idx('asa-materials')).toBe(idx('asa-widget-wrap') + 1);
    // Everything the page had before is still there.
    expect(container.querySelector('arena-tickets')).not.toBeNull();
    expect(container.querySelector('.asa-hero-title')?.textContent).toBe('Show');
  });

  it('passes the analytics hooks through RenderEventOptions', () => {
    const container = document.createElement('main');
    document.body.appendChild(container);
    const onVideoPlay = vi.fn();
    renderEvent(container, page({ ...EMPTY, videos: [YT] }), 'en', { apiBase: '', resumingCheckout: false, onVideoPlay });
    container.querySelector<HTMLButtonElement>('.asa-video__play')!.click();
    expect(onVideoPlay).toHaveBeenCalledWith('youtube');
  });
});

describe('helpers and CSP', () => {
  it('httpsURL accepts only plain https', () => {
    expect(httpsURL('https://a.example/x')?.hostname).toBe('a.example');
    expect(httpsURL('http://a.example')).toBeNull();
    expect(httpsURL('https://user:pw@a.example')).toBeNull();
    expect(httpsURL('/relative')).toBeNull();
    expect(httpsURL('')).toBeNull();
    expect(httpsURL(null)).toBeNull();
  });

  it('frame-src of the CSP lists exactly the embed hosts the page uses', () => {
    const csp = readFileSync(resolve(__dirname, '../../security-headers.conf.template'), 'utf8');
    const frameSrc = /frame-src ([^;]+);/.exec(csp)?.[1] ?? '';
    for (const rule of Object.values(EMBED_RULES)) {
      expect(frameSrc, rule.host).toContain(`https://${rule.host}`);
    }
    expect(frameSrc).toContain('https://challenges.cloudflare.com');
    // Explicit API origin also supports deterministic local HTTP integration; production uses HTTPS.
    expect(/img-src [^;]*__API_ORIGIN__/.test(csp)).toBe(true);
    // Media pictures come from the platform's own https address.
    expect(/img-src [^;]*https:/.test(csp)).toBe(true);
    // No third-party script host was added for the materials.
    expect(/script-src ([^;]+);/.exec(csp)?.[1]).toBe("'self' https://challenges.cloudflare.com");
  });
});

describe('where it takes place — venue addresses', () => {
  const venueA = { name: 'Hall A', country: 'CZ', city: 'Prague', address: 'Main 1', postal_code: '11000', timezone: 'Europe/Prague', session_starts: ['2026-12-01T18:00:00Z'] };
  const venueB = { name: 'Club B', country: 'DE', city: null, address: null, postal_code: null, timezone: null, session_starts: ['2026-12-02T18:00:00Z'] };

  it('prints only the filled address parts of one venue, no invented address', () => {
    const root = renderMaterials(null, 'en', ['Hall A'], {}, [venueA])!;
    const sec = root.querySelector('.asa-materials__map')!;
    expect(sec.querySelector('h2')?.textContent).toBe('Where it takes place');
    expect(sec.textContent).toContain('Main 1');
    expect(sec.textContent).toContain('11000 Prague');
    expect(sec.querySelector('a')).toBeNull();
  });

  it('a name-only venue without a point shows no section', () => {
    expect(renderMaterials(null, 'en', ['Club B'], {}, [{ ...venueB, country: null }])).toBeNull();
  });

  it('with several venues lists each with dates and does not pin the one point on them', () => {
    const root = renderMaterials({ ...EMPTY, map_point: { latitude: 0, longitude: 0, label: null } }, 'en', [], {}, [venueA, venueB])!;
    const sec = root.querySelector('.asa-materials__map')!;
    expect(sec.querySelectorAll('.asa-venue').length).toBe(2);
    expect(sec.textContent).toContain('held at several places');
    expect(sec.textContent).toContain('Point marked by the organizer');
    expect(sec.querySelectorAll('.asa-venue .asa-map__links').length).toBe(0);
    // 0,0 is a valid point.
    expect(sec.textContent).toContain('0.00000, 0.00000');
  });

  it('script text in a venue name stays text', () => {
    const root = renderMaterials(null, 'en', [], {}, [{ ...venueA, name: '<img src=x onerror=alert(1)>' }])!;
    expect(root.querySelector('img')).toBeNull();
  });
});

describe('widget mount — no second copy of the materials', () => {
  it('the event page widget is told materials="hidden"', () => {
    const c = document.createElement('div');
    renderEvent(c, page({ ...EMPTY, partner_links: [{ url: 'https://other.example/t', seller_name: 'Other' }] }), 'en', { apiBase: '', resumingCheckout: false });
    expect(c.querySelector('arena-tickets')?.getAttribute('materials')).toBe('hidden');
    expect(c.querySelectorAll('.asa-materials').length).toBe(1);
  });
});
