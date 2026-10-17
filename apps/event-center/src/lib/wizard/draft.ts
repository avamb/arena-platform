/**
 * The draft of the event wizard (task 6-1a, spec 40 section 5.6, plan stage 6).
 *
 * Pure types and a reducer, free of React, so every rule is tested without a
 * DOM. The draft holds what the person typed in the sections "Event",
 * "Venue", "Dates" and "Tickets" (the last two are datesDraft.ts, 6-1b).
 * Saving joins in 6-1c. A new section bumps DRAFT_VERSION, and an older
 * stored draft is then dropped, never half read, EXCEPT a version-2 draft
 * (no poster yet), which is read whole and gets an empty poster (7-1): the
 * poster field adds nothing a person typed, so no work is thrown away.
 *
 * The draft lives in this browser only (localStorage, one per organization).
 * It holds an event name, a description and a venue: no personal data. The
 * poster part holds two stable ids at most (the uploaded media object and the
 * created event): never the file, a blob address, a signed link or a token.
 */
import { applyDateAction, emptyDate, isBlankDate, isDateAction, parseDates, type DateAction, type DateEntry } from "@/lib/wizard/datesDraft";

/** The age ratings the database accepts (`events_age_rating_check`, migration 0051). */
export const AGE_OPTIONS = ["0+", "6+", "12+", "16+", "18+", "NR"] as const;
export type AgeRating = (typeof AGE_OPTIONS)[number];

export function isAgeRating(value: string): value is AgeRating {
  return (AGE_OPTIONS as readonly string[]).includes(value);
}

/** Longest event name and description, as the bot's wizard keeps them. */
export const MAX_NAME_RUNES = 200;
export const MAX_DESCRIPTION_RUNES = 4000;
/** The largest hall the wizard accepts, as the bot's `maxCount`. */
export const MAX_CAPACITY = 1_000_000;

export const DRAFT_VERSION = 3;
/** The oldest stored version that is still read (and upgraded) instead of dropped. */
export const OLDEST_READABLE_DRAFT_VERSION = 2;

export type VenueMode = "" | "existing" | "new";

export interface EventSection {
  name: string;
  description: string;
  /** "" = not chosen. */
  age: AgeRating | "";
  /** "" = the organization itself promotes the event. */
  promoterId: string;
  /** The name shown for the chosen promoter, kept so a reload shows it at once. */
  promoterName: string;
}

export interface VenueSection {
  mode: VenueMode;
  /** The chosen venue of the organization (mode "existing"). */
  existingId: string;
  existingName: string;
  /** The time zone of the chosen venue, read from the list. */
  existingTimezone: string;
  existingCapacity: number | null;
  /** A new venue (mode "new"). Kept when the person switches mode and back. */
  newName: string;
  /** ISO 3166-1 alpha-2, upper case; "" = not chosen. */
  countryIso2: string;
  cityName: string;
  /** IANA zone. */
  timezone: string;
  /** Typed as is; parsed by `parseCapacity`. */
  capacity: string;
}

/**
 * What the poster of this draft has reached. The chosen FILE lives in memory
 * only; these ids are what survives a reload.
 *  - `mediaId`: the picture is uploaded (not yet the poster of anything);
 *  - `eventId`: the event exists (the draft's own key made it); the poster may
 *    still be waiting to be attached.
 */
export interface PosterRef {
  mediaId: string;
  eventId: string;
}

export function emptyPoster(): PosterRef {
  return { mediaId: "", eventId: "" };
}

export function hasPosterRefs(d: WizardDraft): boolean {
  return d.poster.mediaId !== "" || d.poster.eventId !== "";
}

export interface WizardDraft {
  version: typeof DRAFT_VERSION;
  /** Names the draft in the idempotency key of the later save (6-1c). */
  id: string;
  event: EventSection;
  venue: VenueSection;
  /** The dates, in the order the person added them; always at least one. */
  dates: DateEntry[];
  /** ISO 4217, upper case; "" = not known yet. */
  currency: string;
  /** Where the currency came from: the country (auto), or typed by the person. */
  currencySource: "auto" | "typed";
  poster: PosterRef;
  updatedAt: string;
}

export function emptyEvent(): EventSection {
  return { name: "", description: "", age: "", promoterId: "", promoterName: "" };
}

export function emptyVenue(): VenueSection {
  return {
    mode: "",
    existingId: "",
    existingName: "",
    existingTimezone: "",
    existingCapacity: null,
    newName: "",
    countryIso2: "",
    cityName: "",
    timezone: "",
    capacity: "",
  };
}

/**
 * A new draft with one empty date. `defaultName` is the name of its first
 * category ("Admission" in the person's language), like the bot's default
 * button.
 */
export function newDraft(id: string, now: Date = new Date(), defaultName = ""): WizardDraft {
  return {
    version: DRAFT_VERSION,
    id,
    event: emptyEvent(),
    venue: emptyVenue(),
    dates: [emptyDate("d1", defaultName)],
    currency: "",
    currencySource: "auto",
    poster: emptyPoster(),
    updatedAt: now.toISOString(),
  };
}

/** A venue of the organization, as the wizard needs it (a slice of `VenueItem`). */
export interface VenueChoice {
  id: string;
  name: string;
  timezone: string | null;
  capacity: number | null;
  country: string | null;
}

export type DraftAction =
  | { type: "event"; field: "name" | "description"; value: string }
  | { type: "age"; value: AgeRating | "" }
  | { type: "promoter"; id: string; name: string }
  | { type: "venueMode"; mode: VenueMode }
  | { type: "venueExisting"; venue: VenueChoice | null }
  | { type: "venueNew"; field: "newName" | "cityName" | "capacity"; value: string }
  | { type: "country"; iso2: string }
  | { type: "timezone"; value: string }
  | { type: "currency"; value: string; source: "auto" | "typed" }
  | { type: "posterUploaded"; mediaId: string }
  | { type: "posterCreated"; eventId: string }
  | { type: "posterCleared" }
  | DateAction
  | { type: "reset"; id: string; defaultName: string };

/** Folds an answer into the draft. The clock is passed in, so the reducer stays pure. */
export function reduceDraft(draft: WizardDraft, action: DraftAction, now: Date = new Date()): WizardDraft {
  const next = apply(draft, action);
  return next === draft ? draft : { ...next, updatedAt: now.toISOString() };
}

function apply(d: WizardDraft, a: DraftAction): WizardDraft {
  switch (a.type) {
    case "event":
      return { ...d, event: { ...d.event, [a.field]: a.value } };
    case "age":
      return { ...d, event: { ...d.event, age: a.value } };
    case "promoter":
      return { ...d, event: { ...d.event, promoterId: a.id, promoterName: a.name } };
    case "venueMode":
      return d.venue.mode === a.mode ? d : { ...d, venue: { ...d.venue, mode: a.mode } };
    case "venueExisting": {
      if (a.venue === null) {
        return {
          ...d,
          venue: { ...d.venue, existingId: "", existingName: "", existingTimezone: "", existingCapacity: null },
        };
      }
      return {
        ...d,
        venue: {
          ...d.venue,
          mode: "existing",
          existingId: a.venue.id,
          existingName: a.venue.name,
          existingTimezone: a.venue.timezone ?? "",
          existingCapacity: a.venue.capacity,
        },
      };
    }
    case "venueNew":
      return { ...d, venue: { ...d.venue, [a.field]: a.value } };
    case "country": {
      const iso2 = a.iso2.trim().toUpperCase();
      if (iso2 === d.venue.countryIso2) return d;
      // A zone and a city belong to one country: a new country clears both.
      return { ...d, venue: { ...d.venue, countryIso2: iso2, cityName: "", timezone: "" } };
    }
    case "timezone":
      return { ...d, venue: { ...d.venue, timezone: a.value } };
    case "currency": {
      const value = a.value.trim().toUpperCase();
      // A value read from the country never overwrites what the person typed.
      if (a.source === "auto" && d.currencySource === "typed" && d.currency !== "") return d;
      if (value === d.currency && a.source === d.currencySource) return d;
      return { ...d, currency: value, currencySource: a.source };
    }
    case "posterUploaded":
      return d.poster.mediaId === a.mediaId ? d : { ...d, poster: { ...d.poster, mediaId: a.mediaId } };
    case "posterCreated":
      return d.poster.eventId === a.eventId ? d : { ...d, poster: { ...d.poster, eventId: a.eventId } };
    case "posterCleared":
      // Another picture (or none) replaces the uploaded one; an existing event stays what it is.
      return d.poster.mediaId === "" ? d : { ...d, poster: { ...d.poster, mediaId: "" } };
    case "reset":
      return newDraft(a.id, new Date(), a.defaultName);
    default:
      return isDateAction(a) ? applyDateAction(d, a) : d;
  }
}

/** True when the person has typed or chosen anything (an untouched draft is not worth keeping). */
export function isBlank(d: WizardDraft): boolean {
  const e = d.event;
  const v = d.venue;
  return (
    e.name === "" &&
    e.description === "" &&
    e.age === "" &&
    e.promoterId === "" &&
    v.mode === "" &&
    v.newName === "" &&
    v.countryIso2 === "" &&
    v.cityName === "" &&
    v.capacity === "" &&
    d.dates.length === 1 &&
    isBlankDate(d.dates[0])
  );
}

/** Whole places, 1 to MAX_CAPACITY; "1 200" is read as 1200. Null when it is not a count. */
export function parseCapacity(raw: string): number | null {
  const s = raw.replace(/[\s  ]/g, "");
  if (!/^\d{1,7}$/.test(s)) return null;
  const n = Number(s);
  return n >= 1 && n <= MAX_CAPACITY ? n : null;
}

/** The currency of the draft when it looks like an ISO 4217 code, else "". */
export function validCurrency(raw: string): string {
  const s = raw.trim().toUpperCase();
  return /^[A-Z]{3}$/.test(s) ? s : "";
}

export function runeLength(s: string): number {
  return [...s].length;
}

/** The time zone the draft's venue will have, whichever mode it is in. */
export function venueTimezone(v: VenueSection): string {
  return v.mode === "existing" ? v.existingTimezone : v.timezone;
}

/** The place count of the venue the person named, null when unknown. */
export function venueCapacity(v: VenueSection): number | null {
  return v.mode === "existing" ? v.existingCapacity : parseCapacity(v.capacity);
}

// ─── storage ─────────────────────────────────────────────────────────────────

/** One draft per organization, in this browser only. */
export function draftStorageKey(orgId: string): string {
  return `arena.events.wizard.${orgId}`;
}

/**
 * Reads a stored draft. Anything unreadable, from another version or of the
 * wrong shape is dropped (null): a half-read draft would put wrong values into
 * a bundle. Storage can throw (blocked, private window), so it is wrapped.
 */
export function loadDraft(orgId: string, storage: Pick<Storage, "getItem"> | null = safeStorage()): WizardDraft | null {
  if (storage === null) return null;
  try {
    const raw = storage.getItem(draftStorageKey(orgId));
    if (raw === null) return null;
    return parseDraft(JSON.parse(raw));
  } catch {
    return null;
  }
}

export function saveDraft(
  orgId: string,
  draft: WizardDraft,
  storage: Pick<Storage, "setItem" | "removeItem"> | null = safeStorage(),
): void {
  if (storage === null) return;
  try {
    if (isBlank(draft) && !hasPosterRefs(draft)) storage.removeItem(draftStorageKey(orgId));
    else storage.setItem(draftStorageKey(orgId), JSON.stringify(draft));
  } catch {
    // The draft then lives for this visit only.
  }
}

/**
 * Records poster progress in the STORED draft `draftId` (an upload or a save
 * that finishes after the screen is gone must still not be lost). Another
 * draft of the same organization (another id) is never touched.
 */
export function recordPosterProgress(
  orgId: string,
  draftId: string,
  patch: Partial<PosterRef>,
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem"> | null = safeStorage(),
): void {
  if (storage === null) return;
  const stored = loadDraft(orgId, storage);
  if (stored === null || stored.id !== draftId) return;
  saveDraft(orgId, { ...stored, poster: { ...stored.poster, ...patch } }, storage);
}

export function clearDraft(orgId: string, storage: Pick<Storage, "removeItem"> | null = safeStorage()): void {
  if (storage === null) return;
  try {
    storage.removeItem(draftStorageKey(orgId));
  } catch {
    // Nothing to do.
  }
}

/**
 * Removes the stored draft only when it is the draft that was saved. A newer
 * draft of another tab (another id) is somebody else's work and stays.
 */
export function clearDraftIfSaved(
  orgId: string,
  savedId: string,
  storage: Pick<Storage, "getItem" | "removeItem"> | null = safeStorage(),
): boolean {
  if (storage === null) return false;
  const stored = loadDraft(orgId, storage);
  if (stored === null || stored.id !== savedId) return false;
  clearDraft(orgId, storage);
  return true;
}

function safeStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function str(v: unknown): string | null {
  return typeof v === "string" ? v : null;
}

/** Checks the shape of a stored value field by field; null when anything is off. */
export function parseDraft(raw: unknown): WizardDraft | null {
  if (raw === null || typeof raw !== "object") return null;
  const o = raw as Record<string, unknown>;
  if (typeof o.version !== "number" || o.version < OLDEST_READABLE_DRAFT_VERSION || o.version > DRAFT_VERSION) return null;
  const id = str(o.id);
  const updatedAt = str(o.updatedAt);
  const currency = str(o.currency);
  const source = o.currencySource;
  if (id === null || id === "" || updatedAt === null || currency === null) return null;
  if (source !== "auto" && source !== "typed") return null;
  const e = o.event as Record<string, unknown> | null;
  const v = o.venue as Record<string, unknown> | null;
  if (e === null || typeof e !== "object" || v === null || typeof v !== "object") return null;
  const age = str(e.age);
  if (age === null || (age !== "" && !isAgeRating(age))) return null;
  const name = str(e.name);
  const description = str(e.description);
  const promoterId = str(e.promoterId);
  const promoterName = str(e.promoterName);
  if (name === null || description === null || promoterId === null || promoterName === null) return null;
  const mode = str(v.mode);
  if (mode !== "" && mode !== "existing" && mode !== "new") return null;
  const existingId = str(v.existingId);
  const existingName = str(v.existingName);
  const existingTimezone = str(v.existingTimezone);
  const newName = str(v.newName);
  const countryIso2 = str(v.countryIso2);
  const cityName = str(v.cityName);
  const timezone = str(v.timezone);
  const capacity = str(v.capacity);
  const existingCapacity = v.existingCapacity;
  if (
    existingId === null ||
    existingName === null ||
    existingTimezone === null ||
    newName === null ||
    countryIso2 === null ||
    cityName === null ||
    timezone === null ||
    capacity === null
  ) {
    return null;
  }
  if (existingCapacity !== null && typeof existingCapacity !== "number") return null;
  const dates = parseDates(o.dates);
  if (dates === null) return null;
  // A version-2 draft has no poster; a newer one must carry two strings.
  let poster = emptyPoster();
  if (o.version === DRAFT_VERSION) {
    const p = o.poster as Record<string, unknown> | null | undefined;
    if (p === null || p === undefined || typeof p !== "object") return null;
    const mediaId = str(p.mediaId);
    const eventId = str(p.eventId);
    if (mediaId === null || eventId === null) return null;
    poster = { mediaId, eventId };
  }
  return {
    version: DRAFT_VERSION,
    id,
    event: { name, description, age, promoterId, promoterName },
    venue: {
      mode,
      existingId,
      existingName,
      existingTimezone,
      existingCapacity,
      newName,
      countryIso2,
      cityName,
      timezone,
      capacity,
    },
    dates,
    currency,
    currencySource: source,
    poster,
    updatedAt,
  };
}
