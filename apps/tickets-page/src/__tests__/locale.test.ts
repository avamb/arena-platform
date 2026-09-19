import { describe, expect, it } from 'vitest';
import { isRtlLocale, resolveLocale } from '../lib/locale.ts';

describe('resolveLocale', () => {
  it('prefers ?lang= over navigator languages', () => {
    expect(resolveLocale('?lang=ru', ['en-US'])).toBe('ru');
  });

  it('matches a ?lang= language subtag with a region', () => {
    expect(resolveLocale('?lang=he-IL', [])).toBe('he');
  });

  it('ignores an unsupported ?lang= value and falls back to navigator languages', () => {
    expect(resolveLocale('?lang=fr', ['cs-CZ'])).toBe('cs');
  });

  it('falls back to navigator.languages when there is no ?lang=', () => {
    expect(resolveLocale('', ['de-DE', 'ru-RU'])).toBe('ru');
  });

  it('defaults to en when nothing matches', () => {
    expect(resolveLocale('', ['fr-FR', 'de-DE'])).toBe('en');
  });

  it('defaults to en with no query and no navigator languages', () => {
    expect(resolveLocale('', [])).toBe('en');
  });

  it('accepts a URLSearchParams instance directly', () => {
    const params = new URLSearchParams();
    params.set('lang', 'cs');
    expect(resolveLocale(params, [])).toBe('cs');
  });
});

describe('isRtlLocale', () => {
  it('flags he as RTL', () => {
    expect(isRtlLocale('he')).toBe(true);
  });

  it('does not flag en/ru/cs as RTL', () => {
    expect(isRtlLocale('en')).toBe(false);
    expect(isRtlLocale('ru')).toBe(false);
    expect(isRtlLocale('cs')).toBe(false);
  });
});
