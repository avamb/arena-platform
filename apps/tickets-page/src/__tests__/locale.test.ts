import { describe, expect, it } from 'vitest';
import { isRtlLocale, resolveLocale, toWidgetLocale } from '../lib/locale.ts';

describe('resolveLocale', () => {
  it('prefers ?lang= over navigator languages', () => {
    expect(resolveLocale('?lang=ru', ['en-US'])).toBe('ru');
  });

  it('matches a ?lang= language subtag with a region', () => {
    expect(resolveLocale('?lang=he-IL', [])).toBe('he');
  });

  it('resolves ?lang=es (the page-only locale not understood by the widget)', () => {
    expect(resolveLocale('?lang=es', [])).toBe('es');
  });

  it('matches a ?lang=es-ES region variant', () => {
    expect(resolveLocale('?lang=es-ES', [])).toBe('es');
  });

  it('ignores an unsupported ?lang= value and falls back to navigator languages', () => {
    expect(resolveLocale('?lang=fr', ['cs-CZ'])).toBe('cs');
  });

  it('falls back to navigator.languages when there is no ?lang=', () => {
    expect(resolveLocale('', ['de-DE', 'ru-RU'])).toBe('ru');
  });

  it('resolves es from navigator.languages', () => {
    expect(resolveLocale('', ['es-ES', 'en-US'])).toBe('es');
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

  it('does not flag en/ru/cs/es as RTL', () => {
    expect(isRtlLocale('en')).toBe(false);
    expect(isRtlLocale('ru')).toBe(false);
    expect(isRtlLocale('cs')).toBe(false);
    expect(isRtlLocale('es')).toBe(false);
  });
});

describe('toWidgetLocale', () => {
  it('passes every widget-supported locale through unchanged', () => {
    expect(toWidgetLocale('en')).toBe('en');
    expect(toWidgetLocale('ru')).toBe('ru');
    expect(toWidgetLocale('cs')).toBe('cs');
    expect(toWidgetLocale('he')).toBe('he');
  });

  it('maps the page-only es locale onto en for the widget', () => {
    expect(toWidgetLocale('es')).toBe('en');
  });
});

describe('resolveLocale — remembered explicit choice', () => {
  it('prefers the remembered language over the browser when the URL has no lang', () => {
    expect(resolveLocale('?checkout_token=abc', ['en-US'], 'ru')).toBe('ru');
  });
  it('lets an explicit ?lang= win over the remembered one', () => {
    expect(resolveLocale('?lang=cs', ['en-US'], 'ru')).toBe('cs');
  });
  it('ignores an unsupported remembered value', () => {
    expect(resolveLocale('', ['ru-RU'], 'xx')).toBe('ru');
  });
});
