/**
 * The price legend above the seat map shows each tier in the colour its seats
 * carry on the plan (category_prices[].color by tier_id), not one fixed
 * indigo for every tier. Without a plan the old neutral dot stays.
 *
 * SessionList is server-rendered (svelte/server) so the assertions are on the
 * markup a buyer gets.
 */

import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import SessionList from './components/SessionList.svelte';
import type { FeedSession, Tier } from './types.js';

function tier(id: string, name: string, sort: number): Tier {
  return {
    id,
    name,
    pricing_mode: 'fixed',
    price_amount: 30000,
    currency: 'czk',
    sort_order: sort,
  };
}

const session = {
  id: 's1',
  start_at: '2026-12-01T18:00:00Z',
  end_at: '2026-12-01T20:00:00Z',
  capacity_total: 96,
  status: 'scheduled',
  admission_mode: 'assigned_seats',
  buyer_fields: [],
  tiers: [tier('t-nave', 'Nave', 1), tier('t-vip', 'VIP', 2)],
  media_gallery: [],
  timezone: 'Europe/Prague',
} as unknown as FeedSession;

function legendHtml(tierColors?: ReadonlyMap<string, string>): string {
  return render(SessionList, {
    props: { sessions: [session], selectedSession: session, onSelectSession: () => {}, tierColors },
  }).body;
}

describe('SessionList: legend colours', () => {
  it('paints each tier in its plan colour', () => {
    const html = legendHtml(new Map([['t-nave', '#3949ab'], ['t-vip', '#dc2626']]));
    expect(html).toContain('background:#3949ab');
    expect(html).toContain('background:#dc2626');
    expect(html).not.toContain('#4f46e5');
  });

  it('keeps the neutral dot for a tier the plan does not colour', () => {
    const html = legendHtml(new Map([['t-vip', '#dc2626']]));
    expect(html).toContain('background:#dc2626');
    expect(html).toContain('background:#4f46e5');
  });

  it('without a plan every dot is neutral', () => {
    expect(legendHtml().match(/background:#4f46e5/g)).toHaveLength(2);
  });
});
