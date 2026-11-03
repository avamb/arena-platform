import { afterEach, describe, expect, it } from 'vitest';
import { promoFromSearch, renderEvent } from '../lib/render.ts';
import type { HostedPageResponse } from '../lib/api.ts';

const page: HostedPageResponse = {
  org: { slug: 'arenasoldout', name: 'Arena Sold Out', logo_url: null },
  event: {
    id: '01929d0e-0e47-7000-8000-000000000301',
    slug: 'summer-festival',
    title: 'Summer Festival 2026',
    description: 'Open-air festival.',
    short_description: 'One night, three stages.',
    image_url: null,
    poster_url: null,
    age_rating: '16+',
    venue_names: ['Forum Karlin'],
    first_session_at: '2026-08-15T18:00:00Z',
    last_session_at: '2026-08-15T22:00:00Z',
    first_session_timezone: 'Europe/Prague',
  },
  feed_token: 'ft_abc123',
  default_locale: 'en',
};

describe('promoFromSearch (a friend link: ?promo=<code>)', () => {
  it('reads, trims and upper-cases a good code', () => {
    expect(promoFromSearch('?promo=anna-7k2q')).toBe('ANNA-7K2Q');
    expect(promoFromSearch('?promo=%20anna-7k2q%20&utm_source=referral&utm_medium=friend')).toBe('ANNA-7K2Q');
  });

  it('is null for an absent, short, long or unsafe value', () => {
    expect(promoFromSearch('')).toBeNull();
    expect(promoFromSearch('?promo=')).toBeNull();
    expect(promoFromSearch('?promo=ab')).toBeNull();
    expect(promoFromSearch(`?promo=${'A'.repeat(65)}`)).toBeNull();
    expect(promoFromSearch('?promo=AB%20CD')).toBeNull();
    expect(promoFromSearch('?promo=<script>')).toBeNull();
    expect(promoFromSearch('?promo=AB_CD')).toBeNull();
  });
});

describe('renderEvent hands the promo code to the widget', () => {
  const original = window.location.href;

  afterEach(() => {
    window.history.replaceState(null, '', original);
    document.body.innerHTML = '';
  });

  function mount(): HTMLElement {
    const container = document.createElement('main');
    document.body.appendChild(container);
    renderEvent(container, page, 'en', { apiBase: '', resumingCheckout: false });
    return container;
  }

  it('sets the promo attribute and leaves the address as it was', () => {
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?promo=anna-7k2q&utm_source=referral');
    const widget = mount().querySelector('arena-tickets');
    expect(widget?.getAttribute('promo')).toBe('ANNA-7K2Q');
    expect(window.location.search).toBe('?promo=anna-7k2q&utm_source=referral');
  });

  it('sets nothing without a usable code', () => {
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?utm_source=referral');
    expect(mount().querySelector('arena-tickets')?.hasAttribute('promo')).toBe(false);
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?promo=x');
    expect(mount().querySelector('arena-tickets')?.hasAttribute('promo')).toBe(false);
  });
});
