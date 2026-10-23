/**
 * Event materials (task 2-2): which addresses the widget trusts, how the
 * `materials` block is turned into what is drawn, the words in all seven
 * languages and the markup a buyer gets before any click.
 */

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { render } from 'svelte/server';
import Materials from '../components/Materials.svelte';
import widgetSource from '../ArenaTickets.svelte?raw';
import {
  EMBED_RULES,
  buildMaterialsView,
  embedHosts,
  hasMaterials,
  httpsURL,
  imageURL,
  mapLinks,
  safeEmbedURL,
  stepIndex,
} from './materials.js';
import { MATERIALS_I18N, getMaterialsStrings } from './materialsI18n.js';
import type { FeedMaterials, FeedMaterialVideo } from '../types.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'];

function empty(): FeedMaterials {
  return { gallery: [], videos: [], partner_links: [], map_point: null, report_photos: [] };
}

const photo = (n: number, extra: Record<string, unknown> = {}) => ({
  media_id: `m${n}`,
  url: `https://media.example/p${n}.jpg`,
  caption: null,
  width: null,
  height: null,
  ...extra,
});

const yt: FeedMaterialVideo = {
  provider: 'youtube',
  video_id: 'abc123',
  embed_url: 'https://www.youtube-nocookie.com/embed/abc123',
  url: 'https://www.youtube.com/watch?v=abc123',
};

describe('httpsURL / imageURL', () => {
  it('accepts only https without credentials', () => {
    expect(httpsURL('https://a.example/x')?.hostname).toBe('a.example');
    expect(httpsURL('http://a.example/x')).toBeNull();
    expect(httpsURL('javascript:alert(1)')).toBeNull();
    expect(httpsURL('https://u:p@a.example/')).toBeNull();
    expect(httpsURL('//a.example/x')).toBeNull();
    expect(httpsURL('')).toBeNull();
    expect(httpsURL(null)).toBeNull();
  });

  it('keeps absolute http(s) pictures and resolves a host-relative path on the API base', () => {
    expect(imageURL('https://media.example/p.jpg', '')).toBe('https://media.example/p.jpg');
    expect(imageURL('/v1/media-files/abc?sig=1', 'https://api.example.com')).toBe(
      'https://api.example.com/v1/media-files/abc?sig=1',
    );
    expect(imageURL('/v1/media-files/abc', '', 'https://site.example/page')).toBe('https://site.example/v1/media-files/abc');
  });

  it('refuses script-bearing and credentialed picture addresses', () => {
    expect(imageURL('javascript:alert(1)', 'https://api.example.com')).toBeNull();
    expect(imageURL('data:image/png;base64,AAAA', 'https://api.example.com')).toBeNull();
    expect(imageURL('blob:https://a.example/1', 'https://api.example.com')).toBeNull();
    expect(imageURL('https://u:p@a.example/p.jpg', '')).toBeNull();
    expect(imageURL('', '')).toBeNull();
    expect(imageURL('/relative', '')).toBeNull();
  });
});

describe('safeEmbedURL', () => {
  it('embeds only the three allowed hosts and sets the privacy parameters', () => {
    const y = safeEmbedURL(yt);
    expect(y).toContain('https://www.youtube-nocookie.com/embed/abc123');
    expect(y).toContain('autoplay=1');
    const v = safeEmbedURL({ provider: 'vimeo', video_id: '1', embed_url: 'https://player.vimeo.com/video/1', url: 'https://vimeo.com/1' });
    expect(v).toContain('dnt=1');
    const r = safeEmbedURL({ provider: 'rutube', video_id: 'x', embed_url: 'https://rutube.ru/play/embed/x', url: 'https://rutube.ru/video/x/' });
    expect(r).toBe('https://rutube.ru/play/embed/x');
  });

  it('refuses a host or a path that does not match the provider, whatever the API said', () => {
    expect(safeEmbedURL({ ...yt, embed_url: 'https://www.youtube.com/embed/abc123' })).toBeNull();
    expect(safeEmbedURL({ ...yt, embed_url: 'https://evil.example/embed/abc123' })).toBeNull();
    expect(safeEmbedURL({ ...yt, embed_url: 'https://www.youtube-nocookie.com/watch?v=1' })).toBeNull();
    expect(safeEmbedURL({ ...yt, embed_url: 'https://www.youtube-nocookie.com/embed/' })).toBeNull();
    expect(safeEmbedURL({ ...yt, embed_url: 'http://www.youtube-nocookie.com/embed/abc' })).toBeNull();
    expect(safeEmbedURL({ ...yt, embed_url: null })).toBeNull();
    expect(safeEmbedURL({ ...yt, provider: 'https' })).toBeNull();
    expect(safeEmbedURL({ ...yt, provider: 'unknown' })).toBeNull();
  });

  it('lists exactly the hosts the documentation names', () => {
    const doc = readFileSync(new URL('../../../../docs/widget-embed.md', import.meta.url), 'utf8');
    expect(embedHosts().sort()).toEqual(Object.values(EMBED_RULES).map((r) => r.host).sort());
    for (const host of embedHosts()) expect(doc).toContain(host);
  });
});

describe('mapLinks', () => {
  it('builds two outbound links and the coordinates text', () => {
    const l = mapLinks({ latitude: 50.0755, longitude: 14.4378, label: null });
    expect(l?.osm).toBe('https://www.openstreetmap.org/?mlat=50.0755&mlon=14.4378#map=17/50.0755/14.4378');
    expect(l?.google).toBe('https://www.google.com/maps/search/?api=1&query=50.0755,14.4378');
    expect(l?.text).toBe('50.07550, 14.43780');
  });

  it('refuses coordinates that are not on the globe', () => {
    expect(mapLinks({ latitude: 91, longitude: 0, label: null })).toBeNull();
    expect(mapLinks({ latitude: 0, longitude: 181, label: null })).toBeNull();
    expect(mapLinks({ latitude: Number.NaN, longitude: 0, label: null })).toBeNull();
    expect(mapLinks({ latitude: '1' as unknown as number, longitude: 0, label: null })).toBeNull();
  });
});

describe('buildMaterialsView', () => {
  it('is empty for an older backend (no block) and for empty lists', () => {
    expect(hasMaterials(buildMaterialsView(undefined))).toBe(false);
    expect(hasMaterials(buildMaterialsView(null))).toBe(false);
    expect(hasMaterials(buildMaterialsView(empty()))).toBe(false);
    expect(hasMaterials(buildMaterialsView({} as FeedMaterials))).toBe(false);
  });

  it('keeps usable entries and drops the unusable ones one by one', () => {
    const view = buildMaterialsView(
      {
        gallery: [photo(1, { caption: '  Hall  ', width: 800, height: 600 }), photo(2, { url: 'javascript:alert(1)' }), photo(3)],
        videos: [
          yt,
          { provider: 'https', video_id: null, embed_url: null, url: 'https://films.example/x' },
          { provider: 'https', video_id: null, embed_url: null, url: 'http://insecure.example/x' },
          { ...yt, embed_url: 'https://evil.example/embed/1', url: 'http://insecure.example/y' },
        ],
        partner_links: [
          { url: 'https://tickets.example/e/1', seller_name: ' Partner Ltd ' },
          { url: 'https://shop.example/x', seller_name: '' },
          { url: 'http://nope.example/', seller_name: 'No' },
        ],
        map_point: { latitude: 41.39, longitude: 2.17, label: null },
        report_photos: [photo(9)],
      },
      '',
      ['Palau', 'Sala B'],
    );
    expect(view.gallery.map((p) => p.url)).toEqual(['https://media.example/p1.jpg', 'https://media.example/p3.jpg']);
    expect(view.gallery[0]).toMatchObject({ caption: 'Hall', width: 800, height: 600 });
    expect(view.gallery[1]).toMatchObject({ caption: '', width: null, height: null });
    expect(view.videos).toHaveLength(2);
    expect(view.videos[0]).toMatchObject({ provider: 'youtube', label: 'YouTube' });
    expect(view.videos[0]!.embed).toContain('youtube-nocookie.com');
    expect(view.videos[1]).toMatchObject({ embed: null, link: 'https://films.example/x' });
    expect(view.partners).toEqual([
      { url: 'https://tickets.example/e/1', host: 'tickets.example', seller: 'Partner Ltd' },
      { url: 'https://shop.example/x', host: 'shop.example', seller: 'shop.example' },
    ]);
    expect(view.map?.label).toBe('Palau, Sala B');
    expect(view.report).toHaveLength(1);
  });

  it('prefers the organizer label of the map point over the venue names', () => {
    const view = buildMaterialsView({ ...empty(), map_point: { latitude: 1, longitude: 2, label: 'Back entrance' } }, '', ['Venue']);
    expect(view.map?.label).toBe('Back entrance');
  });
});

describe('stepIndex', () => {
  it('wraps around both ways', () => {
    expect(stepIndex(0, -1, 3)).toBe(2);
    expect(stepIndex(2, 1, 3)).toBe(0);
    expect(stepIndex(1, 1, 3)).toBe(2);
    expect(stepIndex(0, 1, 0)).toBe(0);
  });
});

describe('materialsI18n', () => {
  it('carries every key in all seven languages', () => {
    const keys = Object.keys(MATERIALS_I18N.en!).sort();
    for (const l of LOCALES) {
      expect(Object.keys(MATERIALS_I18N[l] ?? {}).sort()).toEqual(keys);
      for (const v of Object.values(MATERIALS_I18N[l]!)) expect(v.trim()).not.toBe('');
    }
  });

  it('looks up by language subtag and falls back to English', () => {
    expect(getMaterialsStrings('es-ES').gallery).toBe(MATERIALS_I18N.es!.gallery);
    expect(getMaterialsStrings('HE').gallery).toBe(MATERIALS_I18N.he!.gallery);
    expect(getMaterialsStrings('ja').gallery).toBe(MATERIALS_I18N.en!.gallery);
    expect(getMaterialsStrings('').gallery).toBe(MATERIALS_I18N.en!.gallery);
  });
});

describe('Materials component markup', () => {
  const full: FeedMaterials = {
    gallery: [photo(1, { caption: 'Hall' })],
    videos: [yt],
    partner_links: [{ url: 'https://tickets.example/e/1', seller_name: 'Partner Ltd' }],
    map_point: { latitude: 50.0755, longitude: 14.4378, label: 'Main entrance' },
    report_photos: [photo(7)],
  };
  const html = (materials: FeedMaterials | null, locale = 'en', rtl = false) =>
    render(Materials, { props: { materials, locale, rtl, apiBase: '' } }).body;

  it('renders nothing when there is nothing to show', () => {
    expect(html(null)).not.toContain('data-arena-materials');
    expect(html(empty())).not.toContain('data-arena-materials');
  });

  it('renders each section only for data that exists', () => {
    const only = html({ ...empty(), partner_links: full.partner_links });
    expect(only).toContain('data-materials-section="partners"');
    for (const other of ['gallery', 'videos', 'map', 'report']) {
      expect(only).not.toContain(`data-materials-section="${other}"`);
    }
    const all = html(full);
    for (const sec of ['gallery', 'videos', 'map', 'partners', 'report']) {
      expect(all).toContain(`data-materials-section="${sec}"`);
    }
  });

  it('puts no iframe on the page before a video is pressed', () => {
    const all = html(full);
    expect(all).not.toContain('<iframe');
    expect(all).not.toContain('youtube-nocookie.com/embed');
    expect(all).toContain('Play video');
  });

  it('opens outbound links in a new tab with the right rel', () => {
    const all = html(full);
    const partner = /<a[^>]*href="https:\/\/tickets\.example\/e\/1"[^>]*>/.exec(all)?.[0] ?? '';
    expect(partner).toContain('target="_blank"');
    expect(partner).toContain('rel="noopener nofollow"');
    const map = /<a[^>]*href="https:\/\/www\.openstreetmap\.org[^"]*"[^>]*>/.exec(all)?.[0] ?? '';
    expect(map).toContain('rel="noopener noreferrer"');
    expect(all).toContain('Seller: Partner Ltd');
  });

  it('shows the coordinates as text and no embedded map', () => {
    const all = html(full);
    expect(all).toContain('50.07550, 14.43780');
    expect(all).toContain('Main entrance');
    expect(all).not.toContain('<script');
  });

  it('lazy-loads photos and uses the caption as alt text', () => {
    const all = html(full);
    expect(all).toContain('loading="lazy"');
    expect(all).toContain('alt="Hall"');
  });

  it('speaks the buyer language', () => {
    expect(html(full, 'ru')).toContain(MATERIALS_I18N.ru!.partners);
    expect(html(full, 'he', true)).toContain(MATERIALS_I18N.he!.partners);
  });
});

describe('ArenaTickets wiring', () => {
  it('registers the materials attribute and honours materials="hidden"', () => {
    expect(widgetSource).toContain("materials: { type: 'String', attribute: 'materials' }");
    expect(widgetSource).toContain("materials.trim().toLowerCase() === 'hidden'");
    expect(widgetSource).toMatch(/\{#if !materialsHidden && \(event\.materials/);
  });
});

describe('venues — postal address of the real venue', () => {
  const a = { name: 'Hall A', country: 'CZ', city: 'Prague', address: 'Main 1', postal_code: '11000', timezone: 'Europe/Prague', session_starts: ['2026-12-01T18:00:00Z'] };
  const b = { name: 'Club B', country: null, city: null, address: null, postal_code: null, timezone: null, session_starts: [] };

  it('keeps only the filled address parts and reports hasAddress', () => {
    const v = buildMaterialsView(null, '', [], undefined, [a, b]);
    expect(v.venues[0].lines).toEqual(['Main 1', '11000 Prague', 'CZ']);
    expect(v.venues[1].lines).toEqual([]);
    expect(v.hasAddress).toBe(true);
    expect(v.severalVenues).toBe(true);
    expect(hasMaterials(v)).toBe(true);
  });

  it('a name-only venue is not a section', () => {
    expect(hasMaterials(buildMaterialsView(null, '', [], undefined, [b]))).toBe(false);
  });

  it('the one map point is not labelled with several venue names', () => {
    const point = { latitude: 0, longitude: 0, label: null };
    const v = buildMaterialsView({ gallery: [], videos: [], partner_links: [], map_point: point, report_photos: [] }, '', ['Hall A', 'Club B'], undefined, [a, b]);
    expect(v.map?.label).toBe('');
    const one = buildMaterialsView({ gallery: [], videos: [], partner_links: [], map_point: point, report_photos: [] }, '', ['Hall A'], undefined, [a]);
    expect(one.map?.label).toBe('Hall A');
  });
});
