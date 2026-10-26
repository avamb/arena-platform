/**
 * UXH-23: a cancelled date.
 *
 * The backend refuses every hold on a cancelled session and reports its
 * categories with `available: 0`, but the session stays in the feed (a buyer
 * who holds tickets must still see the date). The widget therefore has to
 * label the chip, and show a notice INSTEAD of the map, the category steppers
 * and the cart bar.
 *
 * SessionList is server-rendered (svelte/server) so the assertions are on the
 * markup a buyer gets; ArenaTickets is pinned with source assertions, the idiom
 * of this suite (its state is filled by effects that do not run in SSR).
 */

import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import SessionList from './components/SessionList.svelte';
import widgetSource from './ArenaTickets.svelte?raw';
import { CHECKOUT_I18N } from './lib/checkout.js';
import type { FeedSession } from './types.js';

function session(id: string, status: string, startAt: string): FeedSession {
  return {
    id,
    start_at: startAt,
    end_at: startAt,
    capacity_total: 10,
    status,
    admission_mode: 'general_admission',
    buyer_fields: [],
    tiers: [],
    media_gallery: [],
    timezone: 'Europe/Prague',
  } as unknown as FeedSession;
}

function listHtml(sessions: FeedSession[], locale = 'en', selected: FeedSession | null = null): string {
  return render(SessionList, {
    props: { sessions, selectedSession: selected, onSelectSession: () => {}, locale },
  }).body;
}

const live = session('s-live', 'scheduled', '2026-12-01T18:00:00Z');
const cancelled = session('s-cancelled', 'cancelled', '2026-11-01T18:00:00Z');

describe('SessionList: the chip of a cancelled date', () => {
  it('carries a visible "Cancelled" label and is disabled', () => {
    const html = listHtml([cancelled]);
    expect(html).toContain('data-testid="session-cancelled-label"');
    expect(html).toContain(CHECKOUT_I18N.en.session_cancelled);
    expect(html).toContain('chip--cancelled');
    expect(html).toMatch(/<button[^>]*disabled/);
  });

  it('puts the label on the cancelled chip only', () => {
    const html = listHtml([cancelled, live], 'en', live);
    expect(html.match(/data-testid="session-cancelled-label"/g)).toHaveLength(1);
    // The live chip is the second button and has no label.
    const liveChip = html.slice(html.lastIndexOf('<button'));
    expect(liveChip).not.toContain('session-cancelled-label');
    expect(liveChip).not.toMatch(/^<button[^>]*disabled/);
  });

  it('prints no label when nothing is cancelled', () => {
    expect(listHtml([live], 'en', live)).not.toContain('session-cancelled-label');
  });

  it.each([
    ['en', 'Cancelled'],
    ['ru', 'Отменено'],
    ['cs', 'Zrušeno'],
    ['he', 'בוטל'],
  ])('is translated: %s', (locale, word) => {
    expect(listHtml([cancelled], locale)).toContain(`>${word}<`);
  });

  it('falls back to English for a locale the widget has no table for', () => {
    expect(listHtml([cancelled], 'xx')).toContain('>Cancelled<');
  });
});

describe('cancelled strings', () => {
  for (const locale of ['en', 'ru', 'cs', 'he'] as const) {
    it(`${locale}: label and notice exist`, () => {
      expect(CHECKOUT_I18N[locale].session_cancelled).toBeTruthy();
      expect(CHECKOUT_I18N[locale].session_cancelled_notice).toBeTruthy();
    });
  }

  it('the notice reads "This date has been cancelled" in English', () => {
    expect(CHECKOUT_I18N.en.session_cancelled_notice).toBe('This date has been cancelled.');
  });

  it('the other locales are real translations, not the English text', () => {
    for (const locale of ['ru', 'cs', 'he'] as const) {
      expect(CHECKOUT_I18N[locale].session_cancelled).not.toBe(CHECKOUT_I18N.en.session_cancelled);
      expect(CHECKOUT_I18N[locale].session_cancelled_notice).not.toBe(CHECKOUT_I18N.en.session_cancelled_notice);
    }
  });
});

describe('ArenaTickets: a cancelled selection offers nothing to buy', () => {
  it('derives one flag from the selection AND from an all-cancelled event', () => {
    expect(widgetSource).toMatch(
      /const salesClosed = \$derived\(isSessionCancelled\(selectedSession\) \|\| allSessionsCancelled\(event\)\)/,
    );
  });

  it('shows the translated notice when the flag is set', () => {
    expect(widgetSource).toMatch(/\{#if salesClosed\}\s*<p[^>]*session-cancelled-notice[^>]*>\{t\.session_cancelled_notice\}<\/p>/);
  });

  it('keeps the seat map, the category steppers and the cart bar behind the flag', () => {
    expect(widgetSource).toContain('{#if !salesClosed && selectedSession && selectedSession.schema_url}');
    expect(widgetSource).toContain('{#if !salesClosed && gaTiers.length > 0}');
    expect(widgetSource).toContain('{#if selectedSession && !salesClosed}');
  });
});
