import { describe, it, expect, vi, afterEach } from 'vitest';
import {
  buildShareLinks,
  shareTarget,
  noteVisible,
  cleanSocials,
  pickRelatedEvents,
  marketingBody,
  subscribeToNews,
  consentText,
  safeHttpUrl,
} from './lib/thankYou.js';
import { readFileSync } from 'node:fs';
import { KEYS, THANK_YOU_LANGS, getThankYouStrings, rowLengths } from './lib/thankYouI18n.js';
import type { FeedEventSummary } from './types.js';

afterEach(() => vi.unstubAllGlobals());

const NOW = new Date('2026-11-25T12:00:00Z');
function ev(over: Partial<FeedEventSummary>): FeedEventSummary {
  return {
    id: 'e1',
    display_number: 1,
    org_id: 'o',
    name: 'Show',
    status: 'published',
    first_session_at: '2026-12-01T18:00:00Z',
    last_session_at: '2026-12-01T20:00:00Z',
    venue_names: [],
    visibility: 'public',
    poster_url: 'https://x/p.jpg',
    page_url: 'https://site/e1',
    created_at: '',
    updated_at: '',
    ...over,
  } as FeedEventSummary;
}

describe('thank-you share', () => {
  it('encodes the address and the name', () => {
    const links = buildShareLinks('https://s.com/a?x=1&y=2', 'Rock & Roll');
    expect(links.map((l) => l.network)).toEqual(['facebook', 'linkedin', 'reddit', 'telegram', 'whatsapp', 'email', 'x']);
    expect(links[0]!.href).toBe(
      'https://www.facebook.com/sharer/sharer.php?u=' + encodeURIComponent('https://s.com/a?x=1&y=2'),
    );
    expect(links.find((l) => l.network === 'x')!.href).toContain('text=Rock%20%26%20Roll');
    expect(links.find((l) => l.network === 'email')!.href.startsWith('mailto:?subject=Rock%20%26%20Roll')).toBe(true);
  });
  it('falls back to origin+pathname', () => {
    expect(shareTarget('https://site/e1', { origin: 'https://a.com', pathname: '/p' })).toBe('https://site/e1');
    expect(shareTarget('', { origin: 'https://a.com', pathname: '/p' })).toBe('https://a.com/p');
    expect(shareTarget(undefined, null)).toBe('');
  });
});

describe('thank-you visibility', () => {
  it('hides an empty note and drops bad socials', () => {
    expect(noteVisible(null)).toBe(false);
    expect(noteVisible({ title: '', text: '', promo_code: null, url: null })).toBe(false);
    expect(noteVisible({ title: 'Thanks', text: '', promo_code: null, url: null })).toBe(true);
    expect(
      cleanSocials([
        { network: 'facebook', url: 'https://f.com/x' },
        { network: 'nope', url: 'https://a' },
        { network: 'x', url: 'javascript:1' },
        { network: 'x', url: 'http://x.com/plain' },
      ]),
    ).toEqual([{ network: 'facebook', url: 'https://f.com/x' }]);
    expect(cleanSocials(undefined)).toEqual([]);
  });
});

describe('related events', () => {
  it('keeps future events with page_url and poster, excludes the current one, nearest first, max 3', () => {
    const d = (iso: string) => ({ first_session_at: iso, last_session_at: iso });
    const list = [
      ev({ id: 'cur' }),
      ev({ id: 'past', ...d('2026-01-01T00:00:00Z') }),
      ev({ id: 'nopage', page_url: '' }),
      ev({ id: 'noposter', poster_url: null, image_url: null }),
      ev({ id: 'late', ...d('2027-03-01T00:00:00Z') }),
      ev({ id: 'soon', ...d('2026-11-30T00:00:00Z') }),
      ev({ id: 'mid1', ...d('2026-12-10T00:00:00Z') }),
      ev({ id: 'mid2', ...d('2026-12-20T00:00:00Z') }),
    ];
    expect(pickRelatedEvents(list, 'cur', NOW).map((e) => e.id)).toEqual(['soon', 'mid1', 'mid2']);
  });
  it('is empty when nothing fits', () => {
    expect(pickRelatedEvents([ev({ id: 'cur' })], 'cur', NOW)).toEqual([]);
    expect(pickRelatedEvents(undefined, 'cur', NOW)).toEqual([]);
  });
});

const VER = 'marketing-thankyou-2026-11-25';

describe('newsletter', () => {
  it('keeps the offered version equal to the platform constant', () => {
    const go = readFileSync(new URL('../../backend/internal/platform/marketing/marketing.go', import.meta.url), 'utf8');
    expect(go).toContain(`const ThankYouTextVersion = "${VER}"`);
  });
  it('names the organizer in the consent line of every language', () => {
    for (const lang of THANK_YOU_LANGS) {
      const line = getThankYouStrings(lang).news_consent;
      expect(line, lang).toContain('{org}');
      expect(consentText(line, 'Teatro Sur'), lang).toContain('Teatro Sur');
    }
    expect(safeHttpUrl('http://a.com')).toBeNull();
  });
  it('builds the body with the text version and the page url', () => {
    expect(marketingBody(VER, 'https://a.com/p?x=1')).toEqual({
      text_version: VER,
      page_url: 'https://a.com/p?x=1',
    });
    expect(marketingBody(VER, '')).toEqual({ text_version: VER });
  });
  it('posts to the marketing route and maps answers', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'pending' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);
    expect(await subscribeToNews('tok 1', 'https://api', 'https://a.com/p', VER)).toBe('pending');
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('https://api/v1/public/checkout/tok%201/marketing');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ text_version: VER, page_url: 'https://a.com/p' });

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'already_confirmed' }), { status: 202 })));
    expect(await subscribeToNews('t', '', '', VER)).toBe('already_confirmed');
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'checkout.not_paid', message: 'x' } }), { status: 409 })),
    );
    expect(await subscribeToNews('t', '', '', VER)).toBe('not_paid');
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: 'marketing.not_offered', message: 'x' } }), { status: 409 })),
    );
    expect(await subscribeToNews('t', '', '', VER)).toBe('not_offered');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { status: 429 })));
    expect(await subscribeToNews('t', '', '', VER)).toBe('rate_limited');
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')));
    expect(await subscribeToNews('t', '', '', VER)).toBe('error');
  });
});

describe('thank-you words', () => {
  it('has every key in all seven languages', () => {
    expect(THANK_YOU_LANGS).toEqual(['en', 'ru', 'cs', 'he', 'es', 'de', 'fr']);
    expect(rowLengths().every((n) => n === KEYS.length)).toBe(true);
    expect(getThankYouStrings('es-ES').share_title).toBe('Cuéntaselo a tus amigos');
    expect(getThankYouStrings('xx').share_title).toBe('Tell your friends');
  });
});
