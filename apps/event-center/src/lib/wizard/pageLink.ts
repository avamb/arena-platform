/**
 * "Fill in from your event page" (spec 59): the organizer gives the address of
 * their OWN event page, the server reads it (never the browser) and the answer
 * becomes the same suggestions as a poster's reading (`parseFacts`).
 *
 * Pure of React and of the network. The answer is untrusted: the facts go
 * through `parseFacts`, the other dates are kept only as real calendar days and
 * clock times, the picture only as an id and two positive sizes, the warnings
 * only as codes this screen knows.
 */
import { ApiError } from "@/lib/api/http";
import { parseFacts, type PosterFacts } from "@/lib/wizard/posterHints";

/** The longest address the screen sends (the server refuses longer bodies anyway). */
export const MAX_PAGE_URL_LENGTH = 2048;

/** The address a person typed, as it is sent, or null when it is not a web page address. */
export function pageUrlOf(typed: string): string | null {
  const s = typed.trim();
  if (s === "" || s.length > MAX_PAGE_URL_LENGTH || /\s/.test(s)) return null;
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return null;
  }
  if ((u.protocol !== "http:" && u.protocol !== "https:") || u.hostname === "") return null;
  return s;
}

export type PageFailure =
  | "ticketing"
  | "invalid"
  | "notPublic"
  | "notPage"
  | "nothing"
  | "fetch"
  | "rate"
  | "denied"
  | "unavailable"
  | "unknown";

/** What a failed reading means for the person, by the server's code; "aborted" = the screen went away. */
export function classifyPageFailure(err: unknown): PageFailure | "aborted" {
  if (!(err instanceof ApiError)) return "unknown";
  if (err.code === "network.aborted") return "aborted";
  switch (err.code) {
    case "event_page.ticketing_platform":
      return "ticketing";
    case "event_page.invalid_url":
    case "event_page.invalid_request":
      return "invalid";
    case "event_page.destination_not_allowed":
      return "notPublic";
    case "event_page.not_html":
    case "event_page.too_large":
      return "notPage";
    case "event_page.nothing_found":
      return "nothing";
    case "event_page.fetch_failed":
      return "fetch";
    case "event_page.rate_limited":
      return "rate";
  }
  if (err.status === 401 || err.status === 403) return "denied";
  if (err.status === 0) return "unknown";
  return "unavailable";
}

export interface PageDate {
  date: string;
  time: string;
}

export interface PageImage {
  mediaId: string;
  width: number;
  height: number;
  fitsPoster: boolean;
}

/** Warnings the screen explains; any other code is dropped. */
export const PAGE_WARNINGS = [
  "event_page.some_dates_skipped",
  "event_page.past_dates_skipped",
  "event_page.cancelled_dates_skipped",
  "event_page.online_event",
  "event_page.price_range_only",
  "event_page.model_unavailable",
  "event_page.model_quota",
  "event_page.image_unavailable",
  "event_page.javascript_page",
] as const;
export type PageWarning = (typeof PAGE_WARNINGS)[number];

export interface PageReading {
  facts: PosterFacts;
  /** The dates after the first one: the form takes only the first; these are shown. */
  otherDates: PageDate[];
  image: PageImage | null;
  warnings: PageWarning[];
  /** A text model read part of the page: the person is asked to check it carefully. */
  byModel: boolean;
}

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function size(v: unknown): number {
  return typeof v === "number" && Number.isSafeInteger(v) && v > 0 && v <= 20_000 ? v : 0;
}

/** The server's answer as the screen may use it, or null when it is not one. */
export function parsePageReading(raw: unknown): PageReading | null {
  const facts = parseFacts(raw);
  if (facts === null) return null;
  const r = raw as Record<string, unknown>;
  const f = r.facts as Record<string, unknown>;

  const otherDates: PageDate[] = [];
  if (Array.isArray(f.dates)) {
    for (const d of f.dates.slice(1, 10)) {
      if (d === null || typeof d !== "object") continue;
      const o = d as Record<string, unknown>;
      if (typeof o.date !== "string" || !DAY.test(o.date)) continue;
      otherDates.push({ date: o.date, time: typeof o.time === "string" && CLOCK.test(o.time) ? o.time : "" });
    }
  }

  let image: PageImage | null = null;
  const img = f.image;
  if (img !== null && typeof img === "object") {
    const o = img as Record<string, unknown>;
    const width = size(o.width);
    const height = size(o.height);
    if (typeof o.media_id === "string" && UUID.test(o.media_id) && width > 0 && height > 0) {
      image = { mediaId: o.media_id, width, height, fitsPoster: o.fits_poster === true };
    }
  }

  const known = new Set<string>(PAGE_WARNINGS);
  const warnings: PageWarning[] = [];
  if (Array.isArray(r.warnings)) {
    for (const w of r.warnings) {
      const code = w !== null && typeof w === "object" ? (w as Record<string, unknown>).code : undefined;
      if (typeof code === "string" && known.has(code) && !warnings.includes(code as PageWarning)) {
        warnings.push(code as PageWarning);
      }
    }
  }

  let byModel = false;
  const sources = r.sources;
  if (sources !== null && typeof sources === "object") {
    byModel = Object.values(sources as Record<string, unknown>).some((v) => v === "model");
  }

  return { facts, otherDates, image, warnings, byModel };
}
