/**
 * session-label.ts — chip labels for the session selector.
 *
 * A session carries no name of its own, so the chip has to say everything
 * from its start/end timestamps. A single-day session reads as a date plus a
 * start time; a session that SPANS several days — a festival pass sold
 * alongside the individual days, for example — reads as a date range with no
 * time, because "16 Oct 12:00" would be indistinguishable from the chip for
 * the first day of the same festival.
 *
 * Every date and time is rendered in the VENUE's zone (`timeZone`, the
 * session's `timezone` from the feed) when one is known: a buyer whose phone
 * lives in another zone must still read the time printed on the door. An
 * organizer in UTC+3 saw her 11:00 and 12:30 Madrid shows as 13:00 and 14:30
 * before this (2026-09-29). Without a zone the viewer's own is used.
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

/** The calendar day of an instant in the given zone, as "YYYY-MM-DD". */
function dayKey(d: Date, timeZone: string | undefined): string {
  try {
    return new Intl.DateTimeFormat('en-CA', { dateStyle: 'short', timeZone }).format(d);
  } catch {
    return d.toISOString().slice(0, 10);
  }
}

/** True when the two instants fall on different calendar days of the zone. */
function spansDays(start: Date, end: Date, timeZone: string | undefined): boolean {
  return dayKey(start, timeZone) !== dayKey(end, timeZone);
}

/** The BCP 47 tag handed to Intl. A bare `en` is en-US to Intl ("Dec 19",
 * "11:00 AM"); the events the widget sells are in Europe, so English
 * follows en-GB: day first, 24-hour clock. Every other tag passes through
 * — including an explicit `en-US` from an embedding site. */
function intlTag(locale: string): string {
  return locale.toLowerCase() === 'en' ? 'en-GB' : locale;
}

/** A zone Intl accepts, or undefined (the viewer's own) for "", absent or
 * an unknown name — a bad zone must never blank every chip. */
function usableZone(timeZone: string | null | undefined): string | undefined {
  if (!timeZone) return undefined;
  try {
    new Intl.DateTimeFormat('en-CA', { timeZone });
    return timeZone;
  } catch {
    return undefined;
  }
}

function fmtDay(d: Date, locale: string, timeZone: string | undefined): string {
  try {
    return d.toLocaleDateString(intlTag(locale), { weekday: 'short', month: 'short', day: 'numeric', timeZone });
  } catch {
    return d.toISOString().slice(0, 10);
  }
}

function fmtDayShort(d: Date, locale: string, timeZone: string | undefined): string {
  try {
    return d.toLocaleDateString(intlTag(locale), { month: 'short', day: 'numeric', timeZone });
  } catch {
    return d.toISOString().slice(0, 10);
  }
}

function fmtTime(d: Date, locale: string, timeZone: string | undefined): string {
  try {
    return d.toLocaleTimeString(intlTag(locale), { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone });
  } catch {
    return d.toISOString().slice(11, 16);
  }
}

/**
 * Build the two lines of a session chip.
 *
 * `endAt` is optional: a session without one, or with an unparseable one, is
 * treated as single-day and keeps the previous date + time rendering.
 * `timeZone` is the venue's IANA zone; "" / absent / unknown means the
 * viewer's own.
 */
export function sessionChipLabel(
  startAt: string,
  endAt: string | null | undefined,
  locale = 'en',
  timeZone?: string | null,
): SessionChipLabel {
  const start = parse(startAt);
  if (!start) {
    // Unparseable start: fall back to raw ISO slices, as before.
    return { date: (startAt ?? '').slice(0, 10), time: (startAt ?? '').slice(11, 16) };
  }
  const tz = usableZone(timeZone);
  const end = parse(endAt);
  if (end && end > start && spansDays(start, end, tz)) {
    return { date: `${fmtDayShort(start, locale, tz)} – ${fmtDayShort(end, locale, tz)}`, time: '' };
  }
  return { date: fmtDay(start, locale, tz), time: fmtTime(start, locale, tz) };
}

/** "Doors open" in the languages the widget is embedded in; English for
 * anything else. Keyed by the primary subtag. */
const DOORS_WORDS: Record<string, string> = {
  en: 'Doors open',
  ru: 'Вход с',
  uk: 'Вхід з',
  cs: 'Vstup od',
  pl: 'Wejście od',
  de: 'Einlass ab',
  fr: 'Ouverture des portes',
  it: 'Apertura porte',
  es: 'Apertura de puertas',
  he: 'פתיחת דלתות',
};

/**
 * The doors-open line of a session ("Doors open 19:30"), on the venue's
 * clock like the chips. Empty when the session has no doors time or it
 * does not parse.
 */
export function doorsOpenLabel(
  doorsAt: string | null | undefined,
  locale = 'en',
  timeZone?: string | null,
): string {
  const d = parse(doorsAt);
  if (!d) return '';
  const word = DOORS_WORDS[locale.toLowerCase().split('-')[0]] ?? DOORS_WORDS.en;
  return `${word} ${fmtTime(d, locale, usableZone(timeZone))}`;
}
