// @vitest-environment jsdom
/** ThankYouBlocks rendered with a mocked fetch: blocks by data, silent on 404. */

import { afterEach, describe, expect, it, vi } from 'vitest';

// The suite resolves svelte to its server build; this file needs the client one.
vi.mock('svelte', async () => await import('../node_modules/svelte/src/index-client.js'));

import { flushSync, mount, unmount } from 'svelte';
import ThankYouBlocks from './components/thankyou/ThankYouBlocks.svelte';

let current: ReturnType<typeof mount> | null = null;

afterEach(() => {
  if (current) unmount(current);
  current = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const SITE = {
  thank_you: { title: 'Thanks!', text: 'Here is a gift', promo_code: 'GIFT10', url: 'https://shop.example/x' },
  socials: [{ network: 'instagram', url: 'https://instagram.com/org' }],
};

const OFFER = { org_name: 'Teatro Sur', text_version: 'marketing-thankyou-2026-11-25' };

async function show(fetchImpl: (url: string) => Response, marketingOffer: { org_name: string; text_version: string } | null = OFFER) {
  const fetchMock = vi.fn(async (url: string) => fetchImpl(String(url)));
  vi.stubGlobal('fetch', fetchMock);
  const target = document.createElement('div');
  document.body.appendChild(target);
  current = mount(ThankYouBlocks, {
    target,
    props: {
      locale: 'en',
      apiBase: 'https://api',
      feedToken: 'feed1',
      checkoutToken: 'co1',
      event: { id: 'cur', name: 'Show', page_url: 'https://site/show' },
      marketingOffer,
    },
  });
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
    await new Promise((r) => setTimeout(r, 0));
    flushSync();
  }
  return { fetchMock, q: (id: string) => document.querySelector(`[data-testid="${id}"]`) };
}

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status });

describe('ThankYouBlocks', () => {
  it('renders share, follow, promo, related from the answers', async () => {
    const { fetchMock, q } = await show((url) => {
      if (url.endsWith('/site')) return json(SITE);
      return json({
        events: [{ id: 'n1', name: 'Next', page_url: 'https://site/n1', poster_url: 'https://x/p.jpg',
          first_session_at: '2099-01-01T10:00:00Z', last_session_at: '2099-01-01T12:00:00Z' }],
      });
    });
    expect(fetchMock.mock.calls.map((c) => c[0]).sort()).toEqual([
      'https://api/v1/public/feeds/feed1/events',
      'https://api/v1/public/feeds/feed1/site',
    ]);
    expect(q('ty-share')).not.toBeNull();
    expect(q('ty-share')!.querySelectorAll('a').length).toBe(7);
    expect(q('ty-follow')!.querySelector('a')!.getAttribute('href')).toBe('https://instagram.com/org');
    expect(q('ty-follow')!.querySelector('a')!.getAttribute('rel')).toBe('noopener noreferrer');
    expect(q('ty-promo-code')!.textContent).toBe('GIFT10');
    expect(q('ty-newsletter')).not.toBeNull();
    expect(q('ty-newsletter')!.textContent).toContain('Teatro Sur');
    expect(q('ty-related')!.querySelectorAll('li').length).toBe(1);
  });

  it('hides follow, promo and related on 404 but keeps share', async () => {
    const { q } = await show(() => json({ error: { code: 'not_found' } }, 404));
    expect(q('ty-follow')).toBeNull();
    expect(q('ty-promo')).toBeNull();
    expect(q('ty-related')).toBeNull();
    expect(q('ty-share')).not.toBeNull();
  });

  it('hides the newsletter when there is no marketing offer', async () => {
    const { q } = await show(() => json({}, 404), null);
    expect(q('ty-newsletter')).toBeNull();
  });
});
