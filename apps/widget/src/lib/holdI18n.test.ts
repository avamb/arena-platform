/**
 * The words of the seat hold (spec 67 §8.2): every language has every key, the
 * placeholders match the English text, ru and en are the spec's table.
 */

import { describe, expect, it } from 'vitest';
import { HOLD_I18N, HOLD_KEYS, fillHold, getHoldStrings, holdCountdownText, holdErrorText } from './holdI18n.js';

const LOCALES = ['en', 'ru', 'cs', 'he', 'es', 'de', 'fr'] as const;

function placeholders(text: string): string[] {
  return [...text.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!).sort();
}

describe('hold texts', () => {
  it('has the 14 keys of the spec in the 7 languages', () => {
    expect(HOLD_KEYS).toHaveLength(14);
    expect(Object.keys(HOLD_I18N).sort()).toEqual([...LOCALES].sort());
    for (const loc of LOCALES) {
      const s = HOLD_I18N[loc]!;
      expect(Object.keys(s).sort(), loc).toEqual([...HOLD_KEYS].sort());
      for (const k of HOLD_KEYS) expect(s[k], `${loc}.${k}`).toBeTruthy();
    }
  });

  it('keeps the placeholders of the English text in every language', () => {
    for (const loc of LOCALES) {
      for (const k of HOLD_KEYS) {
        expect(placeholders(HOLD_I18N[loc]![k]), `${loc}.${k}`).toEqual(placeholders(HOLD_I18N.en![k]));
      }
    }
  });

  it('is the spec table in English and Russian', () => {
    expect(HOLD_I18N.en!.hold_countdown).toBe('Seats held for you: {time}');
    expect(HOLD_I18N.ru!.hold_countdown).toBe('Места за вами: {time}');
    expect(HOLD_I18N.en!.hold_rebook).toBe('Hold the seats again');
    expect(HOLD_I18N.ru!.hold_rebook).toBe('Забронировать снова');
    expect(HOLD_I18N.ru!.hold_in_payment).toBe('Чтобы изменить места, вернитесь к выбору мест.');
    expect(HOLD_I18N.en!.hold_in_payment).toBe('To change seats, go back to the seating plan.');
  });

  it('looks a language up by its subtag and falls back to English', () => {
    expect(getHoldStrings('es-ES')).toBe(HOLD_I18N.es);
    expect(getHoldStrings('de_AT')).toBe(HOLD_I18N.de);
    expect(getHoldStrings('xx')).toBe(HOLD_I18N.en);
    expect(getHoldStrings('')).toBe(HOLD_I18N.en);
  });

  it('prints real values, never a raw placeholder', () => {
    for (const loc of LOCALES) {
      expect(holdCountdownText(loc, '9:59'), loc).toContain('9:59');
      expect(holdCountdownText(loc, '9:59'), loc).not.toContain('{');
      expect(holdErrorText('hold.too_many_seats', loc, { max_seats: 8 }), loc).toContain('8');
      expect(holdErrorText('hold.rehold_paused', loc, { retry_after_seconds: 120 }), loc).toContain('2');
      for (const code of ['hold.too_many_seats', 'hold.too_many_active', 'hold.rehold_paused', 'hold.in_payment', 'hold.zzz']) {
        expect(holdErrorText(code, loc), `${loc} ${code}`).not.toContain('{');
      }
    }
  });
});

describe('holdErrorText', () => {
  it('maps the hold.* codes to the texts', () => {
    expect(holdErrorText('hold.too_many_seats', 'en')).toBe('One hold can have up to 10 seats.');
    expect(holdErrorText('hold.too_many_active', 'ru')).toBe(HOLD_I18N.ru!.hold_too_many_active);
    expect(holdErrorText('hold.rehold_paused', 'en', { retry_after_seconds: 61 })).toBe(
      'You can select these seats again in 2 min. Select other seats.',
    );
    expect(holdErrorText('hold.in_payment', 'en')).toBe(HOLD_I18N.en!.hold_in_payment);
    expect(holdErrorText('hold.expired', 'en')).toBe(HOLD_I18N.en!.hold_expired);
  });

  it('says "could not select" for anything else', () => {
    expect(holdErrorText('hold.failed', 'en')).toBe(HOLD_I18N.en!.hold_failed);
    expect(holdErrorText(undefined, 'ru')).toBe(HOLD_I18N.ru!.hold_failed);
  });

  it('fills only known placeholders', () => {
    expect(fillHold('a {x} {y}', { x: 1 })).toBe('a 1 {y}');
  });
});
