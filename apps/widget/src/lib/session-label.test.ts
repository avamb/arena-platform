/**
 * Unit tests for apps/widget/src/lib/session-label.ts
 *
 * A session has no name, so the chip must distinguish a multi-day pass from
 * the single day it starts on. Actorre's festival (first production client,
 * 2026-09-20) sells three per-day sessions plus one 16–18 October pass; all
 * four used to render as the same "Fri, Oct 16 · 12:00" chip.
 */

import { describe, it, expect } from 'vitest';
import { sessionChipLabel } from './session-label.js';

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

  it('formats a Russian locale range', () => {
    const l = sessionChipLabel('2026-10-16T10:00:00Z', '2026-10-18T21:00:00Z', 'ru');
    expect(l.time).toBe('');
    expect(l.date).toContain('16');
    expect(l.date).toContain('18');
  });
});
