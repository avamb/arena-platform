import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  PLATFORM_BADGE_I18N,
  PLATFORM_BADGE_URL,
  getPlatformBadgeStrings,
  showPlatformBadge,
} from './platformBadge.js';

const widgetSource = readFileSync(fileURLToPath(new URL('../ArenaTickets.svelte', import.meta.url)), 'utf8');
const typesSource = readFileSync(fileURLToPath(new URL('../types.ts', import.meta.url)), 'utf8');

describe('platform badge (white label, spec 52)', () => {
  it('is shown by default, also when the backend sends no flag', () => {
    expect(showPlatformBadge([{}, {}])).toBe(true);
    expect(showPlatformBadge([])).toBe(true);
    expect(showPlatformBadge(null)).toBe(true);
    expect(showPlatformBadge([{ white_label: false }], { white_label: false })).toBe(true);
  });

  it('is hidden only when a session says white_label is exactly true', () => {
    expect(showPlatformBadge([{ white_label: true }])).toBe(false);
    expect(showPlatformBadge([{}, { white_label: true }])).toBe(false);
    expect(showPlatformBadge([], { white_label: true })).toBe(false);
  });

  it('speaks every buyer language and falls back to English', () => {
    for (const lang of ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr']) {
      const s = PLATFORM_BADGE_I18N[lang];
      expect(s, lang).toBeDefined();
      expect(s!.prefix.trim().length, lang).toBeGreaterThan(0);
      expect(s!.new_tab.trim().length, lang).toBeGreaterThan(0);
    }
    expect(getPlatformBadgeStrings('ru-RU').prefix).toBe('Билеты:');
    expect(getPlatformBadgeStrings('xx').prefix).toBe('Tickets:');
  });

  it('links to the platform site in a new tab', () => {
    expect(PLATFORM_BADGE_URL).toBe('https://arenasoldout.com');
    expect(widgetSource).toContain('data-arena-platform-badge');
    expect(widgetSource).toContain('href={PLATFORM_BADGE_URL}');
    expect(widgetSource).toContain('rel="noopener"');
    expect(widgetSource).toContain('showPlatformBadge(event.sessions, selectedSession)');
  });

  it('reads white_label off the session', () => {
    expect(typesSource).toContain('white_label?: boolean');
  });
});
