/**
 * Unit tests for Arena Tickets widget utilities.
 * Uses vitest — no DOM required; tests pure utility functions.
 */

import { describe, it, expect } from 'vitest';
import {
  parseLocale,
  parseFeedToken,
  parseSessionId,
  buildThemeStyle,
  SUPPORTED_LOCALES,
  THEME_CSS_VARS,
} from './utils.js';

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

// ─── buildThemeStyle ─────────────────────────────────────────────────────────

describe('buildThemeStyle', () => {
  it('returns empty string for empty record', () => {
    expect(buildThemeStyle({})).toBe('');
  });

  it('builds a CSS var style fragment', () => {
    const result = buildThemeStyle({ '--arena-accent': '#e11d48' });
    expect(result).toBe('--arena-accent:#e11d48');
  });

  it('joins multiple vars with semicolons', () => {
    const result = buildThemeStyle({
      '--arena-accent': '#e11d48',
      '--arena-bg': '#fff',
    });
    expect(result).toContain('--arena-accent:#e11d48');
    expect(result).toContain('--arena-bg:#fff');
    expect(result).toContain(';');
  });

  it('skips keys that do not start with --', () => {
    const result = buildThemeStyle({
      'color': '#000',
      '--arena-accent': '#e11d48',
    } as Record<string, string>);
    expect(result).not.toContain('color:#000');
    expect(result).toContain('--arena-accent:#e11d48');
  });

  it('skips blank values', () => {
    const result = buildThemeStyle({
      '--arena-accent': '',
      '--arena-bg': '  ',
      '--arena-radius': '8px',
    });
    expect(result).toBe('--arena-radius:8px');
  });
});

// ─── Constants ───────────────────────────────────────────────────────────────

describe('SUPPORTED_LOCALES', () => {
  it('is a non-empty tuple', () => {
    expect(SUPPORTED_LOCALES.length).toBeGreaterThan(0);
  });

  it('includes "en"', () => {
    expect(SUPPORTED_LOCALES).toContain('en');
  });

  it('includes "ru"', () => {
    expect(SUPPORTED_LOCALES).toContain('ru');
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
});
