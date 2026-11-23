/**
 * A date sold by a partner box office (spec 63, L3): no seat picker, no cart,
 * one button "Buy at <seller>" to the partner's site in a new tab.
 */
import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import SessionList from './components/SessionList.svelte';
import ExternalSaleNotice from './components/ExternalSaleNotice.svelte';
import widgetSource from './ArenaTickets.svelte?raw';
import { EXTERNAL_SALE_I18N, externalBuyLabel, externalPriceLine, externalSaleOf } from './lib/externalSale.js';
import type { FeedSession } from './types.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'];

function session(over: Record<string, unknown> = {}): FeedSession {
  return {
    id: 's1',
    start_at: '2026-12-01T18:00:00Z',
    end_at: '2026-12-01T20:00:00Z',
    capacity_total: 0,
    status: 'scheduled',
    sales_mode: 'external',
    sales_state: 'external',
    external: { url: 'https://partner.example/tickets?e=1', seller_name: 'Partner Box', price_from: 25, currency: 'eur' },
    buyer_fields: [],
    tiers: [],
    media_gallery: [],
    timezone: 'Europe/Prague',
    ...over,
  } as unknown as FeedSession;
}

function html(s: FeedSession, locale = 'en'): string {
  return render(ExternalSaleNotice, { props: { session: s, locale } }).body;
}

describe('externalSaleOf', () => {
  it('reads an external session', () => {
    expect(externalSaleOf(session())).toEqual({
      url: 'https://partner.example/tickets?e=1',
      seller_name: 'Partner Box',
      price_from: 25,
      currency: 'eur',
    });
  });

  it.each(['open', 'soon', 'sold_out', undefined])('is null for sales_state %s', (state) => {
    expect(externalSaleOf(session({ sales_state: state }))).toBeNull();
  });

  it('is null without a usable web link', () => {
    expect(externalSaleOf(session({ external: null }))).toBeNull();
    expect(externalSaleOf(session({ external: { url: '', seller_name: 'x', price_from: null, currency: null } }))).toBeNull();
    expect(externalSaleOf(session({ external: { url: 'javascript:alert(1)', seller_name: 'x', price_from: null, currency: null } }))).toBeNull();
    expect(externalSaleOf(null)).toBeNull();
  });
});

describe('ExternalSaleNotice', () => {
  it('renders the button to the partner in a new tab, safely', () => {
    const out = html(session());
    expect(out).toContain('data-testid="external-buy"');
    expect(out).toContain('href="https://partner.example/tickets?e=1"');
    expect(out).toContain('target="_blank"');
    expect(out).toContain('rel="noopener noreferrer"');
    expect(out).toContain('Buy at Partner Box');
  });

  it('prints "from <price> <currency>" when price_from is set', () => {
    expect(html(session())).toContain('from 25 EUR');
  });

  it('prints no price line when price_from is null', () => {
    const out = html(session({ external: { url: 'https://p.example/', seller_name: 'P', price_from: null, currency: null } }));
    expect(out).not.toContain('external-price');
    expect(out).toContain('Buy at P');
  });

  it('has no form, input or cart', () => {
    const out = html(session());
    expect(out).not.toContain('<form');
    expect(out).not.toContain('<input');
  });

  it('renders nothing for a platform session', () => {
    expect(html(session({ sales_state: 'open', sales_mode: 'platform', external: null }))).not.toContain('external-buy');
  });

  it('is translated', () => {
    expect(html(session(), 'ru')).toContain('Купить на Partner Box');
    expect(html(session(), 'ru')).toContain('от 25 EUR');
    expect(html(session(), 'de')).toContain('Kaufen bei Partner Box');
    expect(html(session(), 'xx')).toContain('Buy at Partner Box');
  });

  it('works without a seller name', () => {
    expect(externalBuyLabel('en', '  ')).toBe('Buy tickets');
    expect(externalPriceLine('en', { url: 'u', seller_name: '', price_from: 12.5, currency: null })).toBe('from 12.50');
  });
});

describe('words exist in every language', () => {
  it('covers all seven languages with non-empty strings', () => {
    expect(Object.keys(EXTERNAL_SALE_I18N).sort()).toEqual([...LOCALES].sort());
    for (const l of LOCALES) {
      const t = EXTERNAL_SALE_I18N[l]!;
      expect(t.buy_at).toContain('{seller}');
      expect(t.from_price).toContain('{price}');
      expect(t.buy_at_no_name.trim()).not.toBe('');
    }
  });
});

describe('SessionList keeps the date of an external session', () => {
  it('shows the chip with the date and no state label', () => {
    const out = render(SessionList, {
      props: { sessions: [session()], selectedSession: null, onSelectSession: () => {}, locale: 'en' },
    }).body;
    expect(out).toContain('chip-date');
    expect(out).not.toContain('session-state-label');
  });
});

describe('ArenaTickets wiring', () => {
  it('mounts the notice and hides map, steppers and cart for an external date', () => {
    expect(widgetSource).toContain('{#if salesExternal && selectedSession}');
    expect(widgetSource).toContain('<ExternalSaleNotice');
    expect(widgetSource).toContain('const salesExternal = $derived(!salesClosed && externalSaleOf(selectedSession) !== null)');
    expect(widgetSource).toContain('{#if !salesExternal}');
  });
});
