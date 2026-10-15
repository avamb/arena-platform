/**
 * Dates for the event center. A port of the Telegram bot's date rules
 * (apps/backend/internal/platform/eventbot/wizard_dates.go), so a person
 * gets the same readings in the browser and in the chat.
 *
 * - A day is an ISO string "YYYY-MM-DD" (a calendar day, no time zone).
 * - A typed date is never taken on trust: `dateCandidates` lists every date
 *   the text could mean, and the person confirms one of them, spelled with the
 *   month in words and the weekday.
 * - The clock is always a parameter (`today`), never read inside a rule.
 *
 * Everything here is pure, so it runs under the Node test environment.
 */
import type { Locale } from "@/lib/i18n";

export type DateLocale = Locale;

const MS_DAY = 86_400_000;

// ─── ISO days ────────────────────────────────────────────────────────────────

const ISO_DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

/** True for a real calendar day in ISO form. */
export function isIsoDay(value: string): boolean {
  const m = ISO_DAY.exec(value);
  if (m === null) return false;
  const t = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
  return t.getUTCFullYear() === Number(m[1]) && t.getUTCMonth() === Number(m[2]) - 1 && t.getUTCDate() === Number(m[3]);
}

function toUtc(iso: string): Date {
  const m = ISO_DAY.exec(iso);
  if (m === null) return new Date(NaN);
  return new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3])));
}

function fromUtc(t: Date): string {
  return `${String(t.getUTCFullYear()).padStart(4, "0")}-${String(t.getUTCMonth() + 1).padStart(2, "0")}-${String(t.getUTCDate()).padStart(2, "0")}`;
}

/** The day `n` days after `iso` (n may be negative). */
export function addDays(iso: string, n: number): string {
  return fromUtc(new Date(toUtc(iso).getTime() + n * MS_DAY));
}

/** "2026-10" for a day. */
export function monthOf(iso: string): string {
  return iso.slice(0, 7);
}

/** The month `n` months after "YYYY-MM". */
export function addMonths(month: string, n: number): string {
  const [y, m] = month.split("-").map(Number);
  const t = new Date(Date.UTC(y, m - 1 + n, 1));
  return `${String(t.getUTCFullYear()).padStart(4, "0")}-${String(t.getUTCMonth() + 1).padStart(2, "0")}`;
}

/** The calendar day of `now` in the time zone `timeZone` (UTC when unknown). */
export function todayInZone(timeZone: string | undefined, now: Date = new Date()): string {
  if (timeZone !== undefined && timeZone !== "") {
    try {
      // en-CA prints YYYY-MM-DD.
      const out = new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit", day: "2-digit" }).format(now);
      if (isIsoDay(out)) return out;
    } catch {
      // An unknown zone: UTC decides.
    }
  }
  return fromUtc(now);
}

/** "20.10.2026", the short form the bot prints in its range hints. */
export function displayDate(iso: string): string {
  const m = ISO_DAY.exec(iso);
  if (m === null) return iso;
  return `${m[3]}.${m[2]}.${m[1]}`;
}

// ─── words ───────────────────────────────────────────────────────────────────

export const MONTH_NAMES: Record<DateLocale, readonly string[]> = {
  ru: ["январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"],
  en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
  es: ["enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"],
};

/** "20 октября". English and Spanish keep the same words. */
const MONTH_GENITIVE: Record<DateLocale, readonly string[]> = {
  ru: ["января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"],
  en: MONTH_NAMES.en,
  es: MONTH_NAMES.es,
};

/** Monday first. */
export const WEEKDAY_SHORT: Record<DateLocale, readonly string[]> = {
  ru: ["Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"],
  en: ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"],
  es: ["Lu", "Ma", "Mi", "Ju", "Vi", "Sá", "Do"],
};

/** Monday first. */
const WEEKDAY_LONG: Record<DateLocale, readonly string[]> = {
  ru: ["понедельник", "вторник", "среда", "четверг", "пятница", "суббота", "воскресенье"],
  en: ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"],
  es: ["lunes", "martes", "miércoles", "jueves", "viernes", "sábado", "domingo"],
};

/** 0 for Monday … 6 for Sunday. */
export function mondayIndex(iso: string): number {
  return (toUtc(iso).getUTCDay() + 6) % 7;
}

/** Spells a day for a confirmation: "вторник, 20 октября 2026". */
export function dateWords(locale: DateLocale, iso: string): string {
  if (!isIsoDay(iso)) return iso;
  const t = toUtc(iso);
  const wd = WEEKDAY_LONG[locale][mondayIndex(iso)];
  const month = MONTH_GENITIVE[locale][t.getUTCMonth()];
  if (locale === "es") return `${wd}, ${t.getUTCDate()} de ${month} de ${t.getUTCFullYear()}`;
  return `${wd}, ${t.getUTCDate()} ${month} ${t.getUTCFullYear()}`;
}

// ─── reading a typed date ────────────────────────────────────────────────────

const NUMERIC_DATE = /^(\d{1,2})[\s./,-]+(\d{1,2})(?:[\s./,-]+(\d{2}|\d{4}))?$/;
const COMPACT_DATE = /^(\d{2})(\d{2})(\d{2}|\d{4})$/;
const YEAR_FIRST = /^(\d{4})[\s./,-]+(\d{1,2})[\s./,-]+(\d{1,2})$/;
const DAY_MONTH_WORD = /^(\d{1,2})(?:-?(?:го|е|th|st|nd|rd))?\s+([a-zа-яё]+)\.?(?:\s+(\d{4}))?$/;
const MONTH_WORD_DAY = /^([a-z]+)\.?\s+(\d{1,2})(?:th|st|nd|rd)?(?:,?\s+(\d{4}))?$/;

const MONTH_PREFIXES: Record<string, number> = {
  янв: 1, фев: 2, мар: 3, апр: 4, мая: 5, май: 5, июн: 6, июл: 7, авг: 8, сен: 9, окт: 10, ноя: 11, дек: 12,
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6, jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
  // Spanish: only the prefixes that differ from English.
  ene: 1, abr: 4, ago: 8, set: 9, dic: 12,
};

/** The "de" of "20 de octubre de 2026" is dropped before a date is read. */
const SPANISH_DE = /\s+de\s+/g;

function monthFromWord(word: string): number | null {
  const w = word.trim().toLowerCase();
  const chars = Array.from(w);
  if (chars.length < 3) return null;
  const m = MONTH_PREFIXES[chars.slice(0, 3).join("")];
  return m === undefined ? null : m;
}

/**
 * Lists every calendar day `raw` can mean, most likely first (day.month
 * before month.day). A date with no year is the next such day on or after
 * `today`. Lists nothing when `raw` is not a date at all.
 */
export function dateCandidates(raw: string, today: string): string[] {
  let s = raw.trim().toLowerCase();
  s = s.replace(/[.,;:!? \t]+$/, "");
  if (s === "") return [];
  if (!isIsoDay(today)) return [];
  const [ty, tm, td] = today.split("-").map(Number);

  switch (s) {
    case "сегодня":
    case "today":
    case "hoy":
      return [today];
    case "завтра":
    case "tomorrow":
    case "mañana":
    case "manana":
      return [addDays(today, 1)];
    case "послезавтра":
    case "pasado mañana":
    case "pasado manana":
      return [addDays(today, 2)];
  }
  s = s.replace(SPANISH_DE, " ");

  const out: string[] = [];
  const seen = new Set<string>();
  const add = (yearIn: number, m: number, d: number, yearGiven: boolean): void => {
    let y = yearIn;
    if (m < 1 || m > 12 || d < 1 || d > 31) return;
    if (!yearGiven) {
      // The next such day on or after today. 29.02 in a common year is no date.
      const cand = new Date(Date.UTC(ty, m - 1, d));
      if (cand.getUTCMonth() !== m - 1 || cand.getUTCDate() !== d) return;
      y = cand.getTime() < Date.UTC(ty, tm - 1, td) ? ty + 1 : ty;
    }
    if (y < 100) y += 2000;
    if (y < 2000 || y > 2100) return;
    const t = new Date(Date.UTC(y, m - 1, d));
    if (t.getUTCDate() !== d || t.getUTCMonth() !== m - 1) return; // 31.02
    const iso = fromUtc(t);
    if (!seen.has(iso)) {
      seen.add(iso);
      out.push(iso);
    }
  };
  const num = (v: string | undefined): number => (v === undefined || v === "" ? 0 : Number(v));
  const pairs = (a: number, b: number, y: string | undefined): void => {
    const given = y !== undefined && y !== "";
    add(num(y), b, a, given); // day.month
    if (a <= 12 && b <= 12 && a !== b) add(num(y), a, b, given); // month.day
  };

  let m = YEAR_FIRST.exec(s);
  if (m !== null) {
    add(num(m[1]), num(m[2]), num(m[3]), true);
    return out;
  }
  m = NUMERIC_DATE.exec(s);
  if (m !== null) {
    pairs(num(m[1]), num(m[2]), m[3]);
    return out;
  }
  m = COMPACT_DATE.exec(s);
  if (m !== null) {
    pairs(num(m[1]), num(m[2]), m[3]);
    return out;
  }
  m = DAY_MONTH_WORD.exec(s);
  if (m !== null) {
    const mon = monthFromWord(m[2]);
    if (mon !== null) add(num(m[3]), mon, num(m[1]), m[3] !== undefined && m[3] !== "");
    return out;
  }
  m = MONTH_WORD_DAY.exec(s);
  if (m !== null) {
    const mon = monthFromWord(m[1]);
    if (mon !== null) add(num(m[3]), mon, num(m[2]), m[3] !== undefined && m[3] !== "");
    return out;
  }
  return [];
}

// ─── the allowed range ───────────────────────────────────────────────────────

export interface DateBounds {
  /** First allowed day, never before today. */
  min: string;
  /** Last allowed day, "" when the question has no upper limit. */
  max: string;
}

/** The range a date question accepts, inclusive: never before `today`. */
export function dateBounds(today: string, min?: string, max?: string): DateBounds {
  const lower = min !== undefined && min !== "" && min > today ? min : today;
  return { min: lower, max: max ?? "" };
}

export function inBounds(iso: string, b: DateBounds): boolean {
  return iso >= b.min && (b.max === "" || iso <= b.max);
}

/** True when no day is left to pick (the range is empty). */
export function boundsEmpty(b: DateBounds): boolean {
  return b.max !== "" && b.min > b.max;
}

// ─── the month grid ──────────────────────────────────────────────────────────

export interface DayCell {
  iso: string;
  day: number;
  /** False outside the allowed range: a dead cell, not a button. */
  enabled: boolean;
}

export interface MonthView {
  month: string;
  /** Weeks, Monday first. A null cell is padding. */
  weeks: (DayCell | null)[][];
  /** "" when the previous month has nothing to pick. */
  prevMonth: string;
  /** "" when the next month has nothing to pick. */
  nextMonth: string;
}

/** The month to show first: the one of `preferred`, kept inside the range. */
export function clampMonth(preferred: string, b: DateBounds): string {
  let month = preferred;
  const first = monthOf(b.min);
  if (month < first) month = first;
  if (b.max !== "" && month > monthOf(b.max)) month = monthOf(b.max);
  return month;
}

/** One month of days with the dead cells marked, like the bot's calendar. */
export function monthView(month: string, b: DateBounds): MonthView {
  const first = `${month}-01`;
  const weeks: (DayCell | null)[][] = [];
  let week: (DayCell | null)[] = [];
  for (let i = 0; i < mondayIndex(first); i++) week.push(null);
  for (let iso = first; monthOf(iso) === month; iso = addDays(iso, 1)) {
    week.push({ iso, day: Number(iso.slice(8)), enabled: inBounds(iso, b) });
    if (week.length === 7) {
      weeks.push(week);
      week = [];
    }
  }
  if (week.length > 0) {
    while (week.length < 7) week.push(null);
    weeks.push(week);
  }
  const prev = addMonths(month, -1);
  const next = addMonths(month, 1);
  const prevLast = addDays(`${month}-01`, -1);
  return {
    month,
    weeks,
    prevMonth: prevLast >= b.min ? prev : "",
    nextMonth: b.max === "" || `${next}-01` <= b.max ? next : "",
  };
}

/** Today, tomorrow, in a week and in a month, when they fall inside the range. */
export function quickPicks(today: string, b: DateBounds): { key: "today" | "tomorrow" | "week" | "month"; iso: string }[] {
  const all = [
    { key: "today", days: 0 },
    { key: "tomorrow", days: 1 },
    { key: "week", days: 7 },
    { key: "month", days: 30 },
  ] as const;
  return all.map((q) => ({ key: q.key, iso: addDays(today, q.days) })).filter((q) => inBounds(q.iso, b));
}
