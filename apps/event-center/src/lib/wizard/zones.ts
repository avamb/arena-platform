/**
 * Clock arithmetic in a venue's time zone, the way the bot's `BuildBundle`
 * does it with Go's `time` package (task 6-1b).
 *
 * The bundle carries instants as RFC 3339 text in the VENUE's zone
 * ("2027-03-05T19:00:00+01:00", "Z" for UTC), made from a wall-clock day and
 * time. A zone that is empty or unknown counts as UTC, as in the bot. Pure:
 * no React, no network, only `Intl`.
 */
import { addDays } from "@/lib/dates";

const MS_MINUTE = 60_000;

const formatters = new Map<string, Intl.DateTimeFormat | null>();

function formatterFor(tz: string): Intl.DateTimeFormat | null {
  if (formatters.has(tz)) return formatters.get(tz) ?? null;
  let f: Intl.DateTimeFormat | null = null;
  try {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
      second: "numeric",
    });
  } catch {
    f = null; // an unknown zone: UTC decides
  }
  formatters.set(tz, f);
  return f;
}

/** True when `tz` is a zone name this browser knows. */
export function isKnownZone(tz: string): boolean {
  return tz !== "" && formatterFor(tz) !== null;
}

/** The zone's offset east of UTC, in minutes, at the instant `epochMs`. 0 for an unknown zone. */
export function zoneOffsetMinutes(epochMs: number, tz: string): number {
  const f = tz === "" ? null : formatterFor(tz);
  if (f === null) return 0;
  const parts: Record<string, number> = {};
  for (const p of f.formatToParts(new Date(epochMs))) {
    if (p.type !== "literal") parts[p.type] = Number(p.value);
  }
  const asUtc = Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second);
  const whole = Math.floor(epochMs / 1000) * 1000;
  return Math.round((asUtc - whole) / MS_MINUTE);
}

/**
 * The instant of a wall-clock day ("YYYY-MM-DD") and time ("HH:MM") in `tz`.
 * The same two-step lookup as Go's `time.Date`: take the offset at the wall
 * time read as UTC, then correct it when that lands in another zone period.
 * Null when the day or the time is not a real one.
 */
export function wallToEpoch(day: string, hhmm: string, tz: string): number | null {
  const d = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  const t = /^(\d{2}):(\d{2})$/.exec(hhmm);
  if (d === null || t === null) return null;
  const [y, mo, da] = [Number(d[1]), Number(d[2]), Number(d[3])];
  const [h, mi] = [Number(t[1]), Number(t[2])];
  if (h > 23 || mi > 59) return null;
  const wall = Date.UTC(y, mo - 1, da, h, mi);
  const check = new Date(wall);
  if (check.getUTCFullYear() !== y || check.getUTCMonth() !== mo - 1 || check.getUTCDate() !== da) return null;
  const first = zoneOffsetMinutes(wall, tz);
  const utc = wall - first * MS_MINUTE;
  const second = zoneOffsetMinutes(utc, tz);
  return wall - second * MS_MINUTE;
}

function two(n: number): string {
  return String(n).padStart(2, "0");
}

/** An instant as Go's `time.RFC3339` prints it in `tz`: "Z" for a zero offset. */
export function formatRfc3339(epochMs: number, tz: string): string {
  const off = zoneOffsetMinutes(epochMs, tz);
  const local = new Date(epochMs + off * MS_MINUTE);
  const stamp = `${String(local.getUTCFullYear()).padStart(4, "0")}-${two(local.getUTCMonth() + 1)}-${two(local.getUTCDate())}T${two(local.getUTCHours())}:${two(local.getUTCMinutes())}:${two(local.getUTCSeconds())}`;
  if (off === 0) return `${stamp}Z`;
  const abs = Math.abs(off);
  return `${stamp}${off < 0 ? "-" : "+"}${two(Math.floor(abs / 60))}:${two(abs % 60)}`;
}

/** The start of a calendar day in `tz`, as RFC 3339 (the bot's `startOfDay(...).Format`). */
export function startOfDayRfc3339(day: string, tz: string): string {
  const e = wallToEpoch(day, "00:00", tz);
  return e === null ? "" : formatRfc3339(e, tz);
}

/** The start of the day AFTER `day`: an inclusive last day ends there (the bot's `AddDate(0, 0, 1)`). */
export function endOfInclusiveDayRfc3339(day: string, tz: string): string {
  return startOfDayRfc3339(addDays(day, 1), tz);
}
