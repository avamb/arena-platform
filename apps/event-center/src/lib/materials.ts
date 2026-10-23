/**
 * Rules of the sales page materials editors: limits, link and coordinate
 * checks, ordering and the photo-report publication time. Pure functions, no
 * React and no `fetch`, so every rule is tested alone.
 *
 * The SERVER stays the judge (limits, URL scheme, ranges, media ownership);
 * these checks only spare a person a round trip and give a clear message.
 */

/** The server limits (eventmaterials/limits.go). */
export const LIMITS = {
  gallery: 6,
  videos: 4,
  partners: 3,
  report: 30,
  caption: 200,
  url: 500,
  sellerName: 120,
  mapLabel: 200,
} as const;

export type UrlProblem = "empty" | "tooLong" | "notHttps" | "invalid";

/** https only, no credentials, no javascript:, data: or http:. Returns the cleaned address. */
export function checkHttpsUrl(raw: string): { ok: true; url: string } | { ok: false; problem: UrlProblem } {
  const text = raw.trim();
  if (text === "") return { ok: false, problem: "empty" };
  if ([...text].length > LIMITS.url) return { ok: false, problem: "tooLong" };
  let parsed: URL;
  try {
    parsed = new URL(text);
  } catch {
    return { ok: false, problem: "invalid" };
  }
  if (parsed.protocol !== "https:") return { ok: false, problem: "notHttps" };
  if (parsed.username !== "" || parsed.password !== "" || parsed.hostname === "") return { ok: false, problem: "invalid" };
  return { ok: true, url: text };
}

export type CoordProblem = "incomplete" | "number" | "latRange" | "lngRange";

function parseCoord(raw: string): number | null {
  const t = raw.trim().replace(",", ".");
  if (!/^[+-]?(\d+(\.\d*)?|\.\d+)$/.test(t)) return null;
  const n = Number(t);
  return Number.isFinite(n) ? n : null;
}

/**
 * Both fields empty = "no point" (the caller removes it). One empty field is
 * NOT zero: it is incomplete. 0 and 0 is a valid point.
 */
export function checkMapPoint(
  latRaw: string,
  lngRaw: string,
): { kind: "empty" } | { kind: "ok"; latitude: number; longitude: number } | { kind: "bad"; problem: CoordProblem } {
  const la = latRaw.trim();
  const lo = lngRaw.trim();
  if (la === "" && lo === "") return { kind: "empty" };
  if (la === "" || lo === "") return { kind: "bad", problem: "incomplete" };
  const latitude = parseCoord(la);
  const longitude = parseCoord(lo);
  if (latitude === null || longitude === null) return { kind: "bad", problem: "number" };
  if (latitude < -90 || latitude > 90) return { kind: "bad", problem: "latRange" };
  if (longitude < -180 || longitude > 180) return { kind: "bad", problem: "lngRange" };
  return { kind: "ok", latitude, longitude };
}

/** A caption or label as the API wants it: trimmed, blank means none. */
export function cleanText(raw: string): string | null {
  const t = raw.trim();
  return t === "" ? null : t;
}

/** The ids after moving the item at `index` by `delta`; the same list when it cannot move. */
export function moved<T extends { id: string }>(items: readonly T[], index: number, delta: -1 | 1): string[] {
  const ids = items.map((i) => i.id);
  const to = index + delta;
  if (index < 0 || index >= ids.length || to < 0 || to >= ids.length) return ids;
  [ids[index], ids[to]] = [ids[to], ids[index]];
  return ids;
}

export interface SessionTimes {
  end_at?: string | null;
  status: string;
}

/**
 * When the photo report becomes public: the server shows it only after the
 * LAST end of the event's active, non-cancelled sessions (events.last_session_at
 * is MAX(end_at)); with no such end it stays hidden. The browser clock never
 * decides anything: this is only the text beside the editor.
 */
export function reportPublicFrom(sessions: readonly SessionTimes[]): string | null {
  let best: number | null = null;
  for (const s of sessions) {
    if (s.status === "cancelled") continue;
    if (s.end_at === null || s.end_at === undefined) continue;
    const t = Date.parse(s.end_at);
    if (Number.isNaN(t)) continue;
    if (best === null || t > best) best = t;
  }
  return best === null ? null : new Date(best).toISOString();
}

/** The two map links of a point (the public page builds the same ones). */
export function mapUrls(latitude: number, longitude: number): { osm: string; google: string } {
  return {
    osm: `https://www.openstreetmap.org/?mlat=${latitude}&mlon=${longitude}#map=17/${latitude}/${longitude}`,
    google: `https://www.google.com/maps/search/?api=1&query=${latitude},${longitude}`,
  };
}

/** The server refused to add one more: the list is full (`event.*_full`). */
export function isLimitError(err: unknown): boolean {
  const code = typeof err === "object" && err !== null ? (err as { code?: unknown }).code : undefined;
  return typeof code === "string" && code.endsWith("_full");
}
