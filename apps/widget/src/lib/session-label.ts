/**
 * session-label.ts — chip labels for the session selector.
 *
 * A session carries no name of its own, so the chip has to say everything
 * from its start/end timestamps. A single-day session reads as a date plus a
 * start time; a session that SPANS several days — a festival pass sold
 * alongside the individual days, for example — reads as a date range with no
 * time, because "16 Oct 12:00" would be indistinguishable from the chip for
 * the first day of the same festival.
 */

export interface SessionChipLabel {
  /** Date line of the chip: one date, or a range for a multi-day session. */
  date: string;
  /** Time line of the chip. Empty for a multi-day session. */
  time: string;
}

function parse(iso: string | null | undefined): Date | null {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

/** True when the two instants fall on different local calendar days. */
function spansDays(start: Date, end: Date): boolean {
  return (
    start.getFullYear() !== end.getFullYear() ||
    start.getMonth() !== end.getMonth() ||
    start.getDate() !== end.getDate()
  );
}

function fmtDay(d: Date, locale: string): string {
  try {
    return d.toLocaleDateString(locale, { weekday: 'short', month: 'short', day: 'numeric' });
  } catch {
    return d.toISOString().slice(0, 10);
  }
}

function fmtDayShort(d: Date, locale: string): string {
  try {
    return d.toLocaleDateString(locale, { month: 'short', day: 'numeric' });
  } catch {
    return d.toISOString().slice(0, 10);
  }
}

function fmtTime(d: Date, locale: string): string {
  try {
    return d.toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' });
  } catch {
    return d.toISOString().slice(11, 16);
  }
}

/**
 * Build the two lines of a session chip.
 *
 * `endAt` is optional: a session without one, or with an unparseable one, is
 * treated as single-day and keeps the previous date + time rendering.
 */
export function sessionChipLabel(
  startAt: string,
  endAt: string | null | undefined,
  locale = 'en',
): SessionChipLabel {
  const start = parse(startAt);
  if (!start) {
    // Unparseable start: fall back to raw ISO slices, as before.
    return { date: (startAt ?? '').slice(0, 10), time: (startAt ?? '').slice(11, 16) };
  }
  const end = parse(endAt);
  if (end && end > start && spansDays(start, end)) {
    return { date: `${fmtDayShort(start, locale)} – ${fmtDayShort(end, locale)}`, time: '' };
  }
  return { date: fmtDay(start, locale), time: fmtTime(start, locale) };
}
