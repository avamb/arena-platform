/**
 * Unit tests for apps/widget/src/lib/session-label.ts
 *
 * A session has no name, so the chip must distinguish a multi-day pass from
 * the single day it starts on. Actorre's festival (first production client,
 * 2026-09-20) sells three per-day sessions plus one 16–18 October pass; all
 * four used to render as the same "Fri, Oct 16 · 12:00" chip.
 */

import { describe, it, expect } from 'vitest';
import { doorsOpenLabel, sessionChipLabel } from './session-label.js';

describe('sessionChipLabel', () => {
  it('renders a single-day session as date + time', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-16T21:00:00Z', 'en-GB');
    expect(l.date).toContain('16');
    expect(l.time).toMatch(/\d{2}:\d{2}/);
  });

  it('renders a multi-day session as a date range with no time', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-18T21:00:00Z', 'en-GB');
    expect(l.time).toBe('');
    expect(l.date).toContain('–');
    expect(l.date).toContain('16');
    expect(l.date).toContain('18');
  });

  it('a multi-day chip differs from the chip of the day it starts on', () => {
    const pass = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-18T21:00:00Z', 'en-GB');
    const day1 = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-16T21:00:00Z', 'en-GB');
    expect(`${pass.date}|${pass.time}`).not.toBe(`${day1.date}|${day1.time}`);
  });

  it('treats a session with no end_at as single-day', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', null, 'en-GB');
    expect(l.time).toMatch(/\d{2}:\d{2}/);
    expect(l.date).not.toContain('–');
  });

  it('treats an unparseable end_at as single-day', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', 'not-a-date', 'en-GB');
    expect(l.time).toMatch(/\d{2}:\d{2}/);
    expect(l.date).not.toContain('–');
  });

  it('ignores an end_at that precedes the start', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-14T10:00:00Z', 'en-GB');
    expect(l.time).toMatch(/\d{2}:\d{2}/);
    expect(l.date).not.toContain('–');
  });

  it('falls back to ISO slices for an unparseable start', () => {
    const l = sessionChipLabel('nonsense', '2026-10-18T21:00:00Z', 'en-GB');
    expect(l.date).toBe('nonsense');
  });

  it('shows a 24-hour clock and day-first dates for plain en', () => {
    const l = sessionChipLabel('2026-12-19T10:00:00Z', '2026-12-19T11:00:00Z', 'en');
    expect(l.time).toMatch(/^\d{2}:\d{2}$/);
    expect(l.time).not.toMatch(/AM|PM/);
    expect(l.date).toMatch(/^\w{3},? 19 Dec$/);
  });

  it('renders the time of the venue zone, not the viewer machine zone', () => {
    // 10:00Z on 19 December is 11:00 in Madrid, whatever zone the test runs in.
    const l = sessionChipLabel('2026-12-19T10:00:00Z', '2026-12-19T11:00:00Z', 'en', 'Europe/Madrid');
    expect(l.time).toBe('11:00');
    expect(l.date).toContain('19 Dec');
    // The same instant read in Moscow is 13:00 — the old behaviour for a
    // buyer whose phone lives there.
    expect(sessionChipLabel('2026-12-19T10:00:00Z', null, 'en', 'Europe/Moscow').time).toBe('13:00');
  });

  it('judges "same day" in the venue zone', () => {
    // 23:30Z–01:00Z is 00:30–02:00 in Madrid: one day there, two days in UTC.
    const madrid = sessionChipLabel('2026-12-19T23:30:00Z', '2026-12-20T01:00:00Z', 'en', 'Europe/Madrid');
    expect(madrid.time).toBe('00:30');
    expect(madrid.date).not.toContain('–');
    const utc = sessionChipLabel('2026-12-19T23:30:00Z', '2026-12-20T01:00:00Z', 'en', 'UTC');
    expect(utc.time).toBe('');
  });

  it('falls back to the viewer zone for an empty or unknown zone name', () => {
    const none = sessionChipLabel('2026-12-19T10:00:00Z', null, 'en', '');
    const bad = sessionChipLabel('2026-12-19T10:00:00Z', null, 'en', 'Mars/Olympus');
    const local = sessionChipLabel('2026-12-19T10:00:00Z', null, 'en');
    expect(none.time).toBe(local.time);
    expect(bad.time).toBe(local.time);
  });

  it('formats a Russian locale range', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-18T21:00:00Z', 'ru');
    expect(l.time).toBe('');
    expect(l.date).toContain('16');
    expect(l.date).toContain('18');
  });
});

describe('doorsOpenLabel', () => {
  it('prints the doors time on the venue clock in the page language', () => {
    expect(doorsOpenLabel('2026-12-15T18:30:00Z', 'ru', 'Europe/Prague')).toBe('Вход с 19:30');
    expect(doorsOpenLabel('2026-12-15T18:30:00Z', 'en', 'Europe/Prague')).toBe('Doors open 19:30');
    expect(doorsOpenLabel('2026-12-15T18:30:00Z', 'es-ES', 'Europe/Madrid')).toBe('Apertura de puertas 19:30');
  });

  it('falls back to English for an unknown language', () => {
    expect(doorsOpenLabel('2026-12-15T18:30:00Z', 'ja', 'Europe/Prague')).toContain('Doors open');
  });

  it('prints nothing without a doors time', () => {
    expect(doorsOpenLabel(null, 'ru', 'Europe/Prague')).toBe('');
    expect(doorsOpenLabel(undefined)).toBe('');
    expect(doorsOpenLabel('not a date')).toBe('');
  });
});
