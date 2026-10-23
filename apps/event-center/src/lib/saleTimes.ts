/**
 * Sales end and doors-open time of a session (task 5-3), the web version of
 * the bot's `sessions_sale_times.go`.
 *
 * The person types a CLOCK TIME on a day of the venue's wall clock; the API
 * wants an RFC 3339 instant. This file converts between the two and builds
 * the PATCH body. Everything here is pure (the zone and the clock are
 * parameters), so it runs under the Node test environment.
 *
 * Server rules (hcatalog/session_sale_times.go): `sales_end_at` absent keeps
 * the value; `doors_open_at` absent keeps, null clears; doors may not be
 * after the start (400 `session.doors_after_start`).
 */
import { isIsoDay } from "@/lib/dates";

const MS_MIN = 60_000;

/** "18:30" → minutes since midnight, or null for anything that is not a clock time. */
export function parseClock(text: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(text.trim());
  if (m === null) return null;
  const h = Number(m[1]);
  const mi = Number(m[2]);
  if (h > 23 || mi > 59) return null;
  return h * 60 + mi;
}

/** Minutes since midnight → "HH:MM". */
export function clockText(minutes: number): string {
  return `${String(Math.floor(minutes / 60)).padStart(2, "0")}:${String(minutes % 60).padStart(2, "0")}`;
}

function zoneOk(timeZone: string): boolean {
  try {
    new Intl.DateTimeFormat("en", { timeZone });
    return true;
  } catch {
    return false;
  }
}

/** The zone's offset from UTC at an instant, in minutes (Madrid in summer: 120). */
export function zoneOffsetMinutes(utcMs: number, timeZone: string): number {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(utcMs));
  const get = (type: string) => Number(parts.find((p) => p.type === type)?.value);
  const asUtc = Date.UTC(get("year"), get("month") - 1, get("day"), get("hour"), get("minute"), get("second"));
  return Math.round((asUtc - Math.floor(utcMs / 1000) * 1000) / MS_MIN);
}

export interface Wall {
  /** ISO calendar day on the venue's wall clock. */
  day: string;
  /** "HH:MM" on the venue's wall clock. */
  time: string;
}

/** An instant as the venue's wall clock. Null for a broken instant or zone. */
export function instantToWall(iso: string, timeZone: string): Wall | null {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t) || !zoneOk(timeZone)) return null;
  const shifted = new Date(t + zoneOffsetMinutes(t, timeZone) * MS_MIN);
  const day = shifted.toISOString().slice(0, 10);
  const minutes = shifted.getUTCHours() * 60 + shifted.getUTCMinutes();
  return { day, time: clockText(minutes) };
}

/** RFC 3339 with whole seconds and Z, the form the bot sends. */
function rfc3339(ms: number): string {
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
}

/**
 * A day and a clock time on the venue's wall clock → the instant, or null when
 * the input is not a real day or time, or the time does not exist (the hour
 * skipped when the clocks go forward).
 */
export function wallToInstant(day: string, time: string, timeZone: string): string | null {
  const minutes = parseClock(time);
  if (minutes === null || !isIsoDay(day) || !zoneOk(timeZone)) return null;
  const [y, mo, d] = day.split("-").map(Number);
  const guess = Date.UTC(y, mo - 1, d, 0, minutes);
  // Inspect both sides of a clock change. Two instants may share this wall
  // time when clocks go back: choose the first, including half-hour changes.
  const dayMs = 24 * 60 * MS_MIN;
  const offsets = new Set([-dayMs, 0, dayMs].map((delta) => zoneOffsetMinutes(guess + delta, timeZone)));
  const matches = [...offsets]
    .map((offset) => guess - offset * MS_MIN)
    .filter((candidate) => {
      const back = instantToWall(rfc3339(candidate), timeZone);
      return back !== null && back.day === day && back.time === clockText(minutes);
    });
  return matches.length === 0 ? null : rfc3339(Math.min(...matches));
}

export type SaleTimeKind = "salesEnd" | "doors";

/** What a button fills in: an instant relative to the session's start. */
export interface Preset {
  key: "atStart" | "before30m" | "before1h" | "before1d";
  /** Minutes before the start. */
  minutes: number;
}

export const PRESETS: Record<SaleTimeKind, readonly Preset[]> = {
  salesEnd: [
    { key: "atStart", minutes: 0 },
    { key: "before1h", minutes: 60 },
    { key: "before1d", minutes: 24 * 60 },
  ],
  doors: [
    { key: "before30m", minutes: 30 },
    { key: "before1h", minutes: 60 },
  ],
};

/** The instant `minutes` before the start. */
export function presetInstant(startIso: string, minutes: number): string {
  return rfc3339(new Date(startIso).getTime() - minutes * MS_MIN);
}

/** What the editor holds while the person works. */
export interface Draft {
  /** True once the person asked for a day other than the session's. */
  anotherDay: boolean;
  /** The chosen day when `anotherDay`, else ignored (the session's day counts). */
  day: string | null;
  /** The clock text as typed. */
  time: string;
}

/** The draft for an existing value; an empty draft (session day, no time) for none. */
export function draftFor(currentIso: string | null, startIso: string, timeZone: string): Draft {
  const start = instantToWall(startIso, timeZone);
  const cur = currentIso === null ? null : instantToWall(currentIso, timeZone);
  if (cur === null) return { anotherDay: false, day: start?.day ?? null, time: "" };
  return { anotherDay: start !== null && cur.day !== start.day, day: cur.day, time: cur.time };
}

export type DraftProblem = "time" | "day" | "gap";

export type DraftResult = { ok: true; iso: string } | { ok: false; problem: DraftProblem };

/** The instant a draft means. The session's day counts unless `anotherDay`. */
export function draftInstant(draft: Draft, startIso: string, timeZone: string): DraftResult {
  if (parseClock(draft.time) === null) return { ok: false, problem: "time" };
  const sessionDay = instantToWall(startIso, timeZone)?.day ?? null;
  const day = draft.anotherDay ? draft.day : sessionDay;
  if (day === null || !isIsoDay(day)) return { ok: false, problem: "day" };
  const iso = wallToInstant(day, draft.time, timeZone);
  if (iso === null) return { ok: false, problem: "gap" };
  return { ok: true, iso };
}

/** Doors may not open after the session starts (the server answers 400 otherwise). */
export function doorsAfterStart(doorsIso: string, startIso: string): boolean {
  return new Date(doorsIso).getTime() > new Date(startIso).getTime();
}

/** The PATCH body that moves the sales end. */
export function salesEndBody(iso: string): { sales_end_at: string } {
  return { sales_end_at: iso };
}

/** The PATCH body that sets (an instant) or clears (null) the doors time. */
export function doorsBody(iso: string | null): { doors_open_at: string | null } {
  return { doors_open_at: iso };
}
