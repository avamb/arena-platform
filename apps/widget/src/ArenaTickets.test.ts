/**
 * Unit tests for Arena Tickets widget utilities.
 * Uses vitest — no DOM required; tests pure utility functions.
 */

import { describe, it, expect } from 'vitest';
import {
  parseLocale,
  parseFeedToken,
  parseSessionId,
  SUPPORTED_LOCALES,
  THEME_CSS_VARS,
  isRtlLocale,
  RTL_LOCALES,
} from './utils.js';
import { SUPPORTED_LOCALES as CHECKOUT_SUPPORTED_LOCALES, CHECKOUT_I18N } from './lib/checkout.js';

// ─── parseLocale ─────────────────────────────────────────────────────────────

describe('parseLocale', () => {
  it('returns "en" for null', () => {
    expect(parseLocale(null)).toBe('en');
  });

  it('returns "en" for undefined', () => {
    expect(parseLocale(undefined)).toBe('en');
  });

  it('returns "en" for empty string', () => {
    expect(parseLocale('')).toBe('en');
  });

  it('returns "en" for whitespace-only string', () => {
    expect(parseLocale('   ')).toBe('en');
  });

  it('lowercases locale', () => {
    expect(parseLocale('EN')).toBe('en');
    expect(parseLocale('RU')).toBe('ru');
  });

  it('trims whitespace', () => {
    expect(parseLocale('  ru  ')).toBe('ru');
  });

  it('truncates to 5 chars for BCP-47 tags', () => {
    expect(parseLocale('en-US')).toBe('en-us');
    expect(parseLocale('zh-Hans')).toBe('zh-ha');
  });

  it('handles plain locale codes', () => {
    for (const locale of SUPPORTED_LOCALES) {
      expect(parseLocale(locale)).toBe(locale);
    }
  });
});

// ─── parseFeedToken ───────────────────────────────────────────────────────────

describe('parseFeedToken', () => {
  it('returns empty string for null', () => {
    expect(parseFeedToken(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(parseFeedToken(undefined)).toBe('');
  });

  it('returns empty string for empty string', () => {
    expect(parseFeedToken('')).toBe('');
  });

  it('trims whitespace', () => {
    expect(parseFeedToken('  abc123  ')).toBe('abc123');
  });

  it('preserves token value', () => {
    const token = 'ft_live_abc123XYZ';
    expect(parseFeedToken(token)).toBe(token);
  });
});

// ─── parseSessionId ───────────────────────────────────────────────────────────

describe('parseSessionId', () => {
  it('returns empty string for null', () => {
    expect(parseSessionId(null)).toBe('');
  });

  it('returns empty string for undefined', () => {
    expect(parseSessionId(undefined)).toBe('');
  });

  it('trims whitespace', () => {
    expect(parseSessionId('  sess-001  ')).toBe('sess-001');
  });

  it('preserves UUIDv7-style value', () => {
    const id = '01930000-0000-7000-8000-000000000001';
    expect(parseSessionId(id)).toBe(id);
  });
});

// ─── Constants ───────────────────────────────────────────────────────────────

describe('SUPPORTED_LOCALES', () => {
  it('is exactly the spec set en / ru / cs / he', () => {
    expect([...SUPPORTED_LOCALES]).toEqual(['en', 'ru', 'cs', 'he']);
  });

  it('is the same object as the checkout translation source of truth', () => {
    expect(SUPPORTED_LOCALES).toBe(CHECKOUT_SUPPORTED_LOCALES);
  });

  it('every supported locale has a complete translation table', () => {
    for (const locale of SUPPORTED_LOCALES) {
      expect(CHECKOUT_I18N[locale]).toBeDefined();
      expect(CHECKOUT_I18N[locale].submit_label.length).toBeGreaterThan(0);
    }
  });

  it('does not list locales without translations (de/fr/es/uk dropped)', () => {
    for (const dropped of ['de', 'fr', 'es', 'uk']) {
      expect(SUPPORTED_LOCALES).not.toContain(dropped);
    }
  });

  it('includes "he" (Hebrew RTL)', () => {
    expect(SUPPORTED_LOCALES).toContain('he');
  });
});

describe('THEME_CSS_VARS', () => {
  it('every entry starts with --', () => {
    for (const v of THEME_CSS_VARS) {
      expect(v.startsWith('--')).toBe(true);
    }
  });

  it('includes --arena-accent', () => {
    expect(THEME_CSS_VARS).toContain('--arena-accent');
  });

  it('includes --arena-bg', () => {
    expect(THEME_CSS_VARS).toContain('--arena-bg');
  });

  it('includes --arena-focus-ring (a11y token)', () => {
    expect(THEME_CSS_VARS).toContain('--arena-focus-ring');
  });
});

// ─── isRtlLocale ─────────────────────────────────────────────────────────────

describe('isRtlLocale', () => {
  it('returns true for "he" (Hebrew)', () => {
    expect(isRtlLocale('he')).toBe(true);
  });

  it('returns false for unsupported RTL languages (no translations shipped)', () => {
    expect(isRtlLocale('ar')).toBe(false);
    expect(isRtlLocale('fa')).toBe(false);
    expect(isRtlLocale('ur')).toBe(false);
  });

  it('returns true for "he-IL" (region tag)', () => {
    expect(isRtlLocale('he-IL')).toBe(true);
  });

  it('returns true for uppercase "HE"', () => {
    expect(isRtlLocale('HE')).toBe(true);
  });

  it('returns false for "en"', () => {
    expect(isRtlLocale('en')).toBe(false);
  });

  it('returns false for "ru"', () => {
    expect(isRtlLocale('ru')).toBe(false);
  });

  it('returns false for "de"', () => {
    expect(isRtlLocale('de')).toBe(false);
  });

  it('returns false for empty string', () => {
    expect(isRtlLocale('')).toBe(false);
  });

  it('covers all RTL_LOCALES entries', () => {
    for (const locale of RTL_LOCALES) {
      expect(isRtlLocale(locale)).toBe(true);
    }
  });
});

// ─── RTL_LOCALES ─────────────────────────────────────────────────────────────

describe('RTL_LOCALES', () => {
  it('is exactly ["he"] (spec: Hebrew is the only supported RTL locale)', () => {
    expect([...RTL_LOCALES]).toEqual(['he']);
  });

  it('every RTL locale is a supported locale', () => {
    for (const code of RTL_LOCALES) {
      expect(SUPPORTED_LOCALES).toContain(code);
    }
  });

  it('all entries are 2-char ISO 639-1 codes', () => {
    for (const code of RTL_LOCALES) {
      expect(code).toHaveLength(2);
    }
  });
});
