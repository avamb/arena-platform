/**
 * What the person types in the dates and tickets sections, read the way the
 * bot reads it (task 6-1b). Ports of the bot's parsers in
 * apps/backend/internal/platform/eventbot (`wizard.go`: ParseTime,
 * ParsePriceMinor, ParseCount; `wizard_sale_times.go`: salesEndOffset,
 * doorsOffset). The golden bundles (testdata/wizard_bundles) prove that the
 * same answers give the same bundle, so a rule changed in the bot must be
 * changed here, and the other way round.
 */

/** The largest place count, as the bot's `maxCount`. */
export const MAX_COUNT = 1_000_000;
/** More than 12 hours before the start is a typo, not a door policy (bot: `maxDoorsMin`). */
export const MAX_DOORS_MIN = 12 * 60;

/** "HH:MM", also "H.MM" and "HHMM"; returns "HH:MM" or null. */
export function parseTime(raw: string): string | null {
  let s = raw.trim().replace(/\./g, ":");
  if (s.length === 4 && !s.includes(":")) s = `${s.slice(0, 2)}:${s.slice(2)}`;
  const parts = s.split(":");
  if (parts.length !== 2) return null;
  const a = parts[0].trim();
  const b = parts[1].trim();
  if (!/^\d+$/.test(a) || !/^\d+$/.test(b)) return null;
  const h = Number(a);
  const m = Number(b);
  if (h < 0 || h > 23 || m < 0 || m > 59) return null;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

const SPACES = /[\s  ]/g;
const PRICE_TRAIL = new Set(["€", "$", "£", "₪", "K", "č"]);

/**
 * "199", "99,90", "1 200", "1.200,50" in minor units. Negative and more than
 * two decimals are refused. The LAST separator is the decimal one when 1-2
 * digits follow it.
 */
export function parsePriceMinor(raw: string): number | null {
  let s = raw.trim().replace(SPACES, "");
  while (s.length > 0 && PRICE_TRAIL.has(s[s.length - 1])) s = s.slice(0, -1);
  if (s === "") return null;
  let dec = -1;
  for (let i = s.length - 1; i >= 0; i -= 1) {
    if (s[i] === "," || s[i] === ".") {
      const n = s.length - i - 1;
      if (n >= 1 && n <= 2) dec = i;
      break;
    }
  }
  let whole = s;
  let frac = "";
  if (dec >= 0) {
    whole = s.slice(0, dec);
    frac = s.slice(dec + 1);
  }
  whole = whole.replace(/[.,]/g, "");
  if (whole === "") whole = "0";
  if (!/^\+?\d{1,13}$/.test(whole)) return null;
  const w = Number(whole);
  let f = 0;
  if (frac !== "") {
    if (!/^\d+$/.test(frac)) return null;
    if (frac.length === 1) frac += "0";
    f = Number(frac);
  }
  return w * 100 + f;
}

/** A positive whole number ("80", "1 200") up to MAX_COUNT; null otherwise. */
export function parseCount(raw: string): number | null {
  const s = raw.trim().replace(/[\s  .,]/g, "");
  if (!/^\d{1,9}$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= MAX_COUNT ? n : null;
}

/** "HH:MM" as minutes after midnight, -1 when it is not one. */
export function minutesOfDay(hhmm: string): number {
  const m = /^(\d{2}):(\d{2})$/.exec(hhmm);
  if (m === null) return -1;
  const h = Number(m[1]);
  const mi = Number(m[2]);
  return h > 23 || mi > 59 ? -1 : h * 60 + mi;
}

/**
 * A typed "HH:MM" sales end as minutes after the start. The time counts on
 * the event's day; a time more than twelve hours before the start is read as
 * the NEXT day (a 22:00 show selling until 01:00). Null when either time is
 * not a clock time.
 */
export function salesEndOffset(start: string, typed: string): number | null {
  const s = minutesOfDay(start);
  const v = minutesOfDay(typed);
  if (s < 0 || v < 0) return null;
  let off = v - s;
  if (off < -12 * 60) off += 24 * 60;
  return off;
}

/**
 * A typed "HH:MM" doors-open time as minutes BEFORE the start: at most twelve
 * hours earlier and strictly before the start. Null when it is neither.
 */
export function doorsOffset(start: string, typed: string): number | null {
  const s = minutesOfDay(start);
  const v = minutesOfDay(typed);
  if (s < 0 || v < 0) return null;
  let before = s - v;
  if (before < 0) before += 24 * 60; // doors the evening before a show after midnight
  if (before <= 0 || before > MAX_DOORS_MIN) return null;
  return before;
}

/** Minor units as the text a person would type ("1890" -> "18.90"). */
export function formatPriceMinor(minor: number): string {
  const whole = Math.floor(minor / 100);
  const frac = minor % 100;
  return `${whole}.${String(frac).padStart(2, "0")}`;
}

/** Minor units as the wire's MAJOR units, like the bot's `money.Major`. */
export function majorOf(minor: number): number {
  return Math.round((minor / 100) * 100) / 100;
}
