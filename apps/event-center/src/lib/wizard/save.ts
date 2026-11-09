/**
 * Saving the wizard draft (task 6-1c, finding F-004 of the acceptance run
 * 2026-11-09): the venue first, when the draft describes a new one, then the
 * dates, one `POST /v1/organizations/{org}/imports/event-bundle` (no
 * `dry_run`) per date, in order — exactly what the Telegram bot does.
 *
 * What the server guarantees, and this file relies on (himports/import_arena.go):
 *  - `publish: false` (always sent by `buildBundle`) makes a DRAFT event and
 *    DRAFT sessions: nothing is on sale, nothing is published into a channel;
 *  - `externalRef` is the idempotency key. A repeat call with the same key
 *    EDITS the session it created (200, `created: false`) and never makes a
 *    second one, so a retry after an unknown result is safe as long as it
 *    carries the draft's own keys (`web:<org>:<draft id>:<n>`, `buildBundle`);
 *  - `action.arenaEventId` adds a date to the event the first date created.
 *
 * The new venue is created ONCE: the save reports it (`onVenue`) and the
 * screen stores it in the draft as the chosen venue, so a retry never makes a
 * second one. A venue of the same name already in the organization is reused
 * (the server refuses a duplicate name with 409 `venue.duplicate`).
 */
import { ApiError } from "@/lib/api/http";
import type { CreateVenueRequest, ImportBil24SessionResponse } from "@/lib/api/types";
import { buildBundle, type EventBundle } from "@/lib/wizard/bundle";
import { parseCapacity, reduceDraft, type VenueChoice, type WizardDraft } from "@/lib/wizard/draft";

/** Where a failed save stopped: the venue, or the date with this index. */
export interface SaveProgress {
  step: "venue" | "date";
  /** The date that failed (0-based); -1 when the venue failed. */
  dateIndex: number;
  /** How many dates are already on the platform (as drafts). */
  savedDates: number;
  /** The event the first date created; "" when none is created yet. */
  eventId: string;
}

export type SaveOutcome =
  | { kind: "saved"; eventId: string; sessionId: string; sessionIds: string[]; created: boolean }
  /** The server looked at the request and said no (400, 409, 422): fix and send again. */
  | { kind: "rejected"; status: number; code: string; message: string; requestId?: string; progress?: SaveProgress }
  /** 401/403: the person may not write here. */
  | { kind: "denied"; status: number; code: string; progress?: SaveProgress }
  /** No answer, a 5xx or an unreadable answer: the write MAY have happened. Retry with the same keys, never start a new draft. */
  | { kind: "unknown"; status: number; code: string; progress?: SaveProgress };

export type SaveFailure = Exclude<SaveOutcome, { kind: "saved" }>;

export function classifySaveError(err: unknown): SaveFailure {
  if (err instanceof ApiError) {
    const { status, code, message, requestId } = err;
    if (status === 401 || status === 403) return { kind: "denied", status, code };
    if (status >= 400 && status < 500 && status !== 408 && status !== 429) {
      return { kind: "rejected", status, code, message, requestId };
    }
    return { kind: "unknown", status, code };
  }
  return { kind: "unknown", status: 0, code: "network.failure" };
}

export type PostBundle = (bundle: EventBundle) => Promise<ImportBil24SessionResponse>;

/** The reads and writes a new venue needs (`venue.read`, `city.create`, `venue.create`). */
export interface VenueApi {
  /** The organization's active venues. */
  listVenues(orgId: string): Promise<VenueChoice[]>;
  /** The platform id of a country by its ISO code, or null when the platform does not know it. */
  countryId(iso2: string): Promise<string | null>;
  /** Finds or creates the city by name (the route is idempotent by name); answers its id. */
  createCity(orgId: string, countryId: string, name: string): Promise<string>;
  createVenue(orgId: string, body: CreateVenueRequest): Promise<VenueChoice>;
}

export interface SaveDeps {
  post: PostBundle;
  venues?: VenueApi;
}

export interface SaveHooks {
  /** Called once the new venue exists, BEFORE the first date is sent. */
  onVenue?: (venue: VenueChoice) => void;
}

export interface Saver {
  /**
   * Sends the draft. While a save is in flight every further call returns
   * THAT save's promise: a double click cannot make a second POST.
   */
  save(draft: WizardDraft, orgId: string, hooks?: SaveHooks): Promise<SaveOutcome>;
  inFlight(): boolean;
}

const sameName = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();

function withProgress(f: SaveFailure, progress: SaveProgress): SaveFailure {
  return { ...f, progress };
}

/** The body of the new venue, as the bot sends it (`ArenaClient.CreateVenue`). */
export function newVenueBody(draft: WizardDraft, cityId: string): CreateVenueRequest {
  const v = draft.venue;
  const body: CreateVenueRequest = {
    name: v.newName.trim(),
    city_id: cityId,
    country: v.countryIso2.toUpperCase(),
    timezone: v.timezone.trim(),
    status: "active",
  };
  const address = v.address.trim();
  if (address !== "") body.address_line1 = address;
  const capacity = parseCapacity(v.capacity);
  if (capacity !== null && capacity > 0) body.capacity_default = capacity;
  return body;
}

/**
 * Makes sure the draft names an existing venue: reuses one of the same name,
 * else creates the city and the venue. Answers the draft that names it.
 */
async function ensureVenue(
  draft: WizardDraft,
  orgId: string,
  api: VenueApi,
  hooks: SaveHooks | undefined,
): Promise<{ draft: WizardDraft } | { failure: SaveFailure }> {
  const v = draft.venue;
  if (v.mode !== "new") return { draft };
  const fail = (f: SaveFailure) => ({ failure: withProgress(f, { step: "venue", dateIndex: -1, savedDates: 0, eventId: "" }) });
  const adopt = (venue: VenueChoice) => {
    hooks?.onVenue?.(venue);
    return { draft: reduceDraft(draft, { type: "venueCreated", venue }) };
  };
  try {
    const own = (await api.listVenues(orgId)).find((x) => sameName(x.name, v.newName));
    if (own !== undefined) return adopt(own);
    const countryId = await api.countryId(v.countryIso2);
    if (countryId === null) {
      return fail({ kind: "rejected", status: 422, code: "wizard.venue_country_unknown", message: "" });
    }
    const cityId = await api.createCity(orgId, countryId, v.cityName.trim());
    try {
      return adopt(await api.createVenue(orgId, newVenueBody(draft, cityId)));
    } catch (err) {
      // Another tab (or an earlier lost answer) made it meanwhile: use that one.
      if (err instanceof ApiError && err.status === 409) {
        const again = (await api.listVenues(orgId)).find((x) => sameName(x.name, v.newName));
        if (again !== undefined) return adopt(again);
      }
      throw err;
    }
  } catch (err) {
    return fail(classifySaveError(err));
  }
}

export function createSaver(deps: PostBundle | SaveDeps): Saver {
  const { post, venues } = typeof deps === "function" ? { post: deps, venues: undefined } : deps;
  let current: Promise<SaveOutcome> | null = null;
  return {
    inFlight: () => current !== null,
    save(draft, orgId, hooks) {
      if (current !== null) return current;
      const run = async (): Promise<SaveOutcome> => {
        if (draft.dates.length === 0) return { kind: "rejected", status: 0, code: "wizard.no_dates", message: "" };
        let d = draft;
        if (d.venue.mode === "new") {
          if (venues === undefined) return { kind: "rejected", status: 0, code: "wizard.not_supported", message: "" };
          const got = await ensureVenue(d, orgId, venues, hooks);
          if ("failure" in got) return got.failure;
          d = got.draft;
        }
        let eventId = "";
        const sessionIds: string[] = [];
        let created = false;
        for (let i = 0; i < d.dates.length; i++) {
          const progress = (): SaveProgress => ({ step: "date", dateIndex: i, savedDates: sessionIds.length, eventId });
          try {
            const res = await post(buildBundle(d, { orgId, eventId: eventId === "" ? undefined : eventId }, i));
            if (typeof res?.event_id !== "string" || typeof res?.session_id !== "string") {
              return { kind: "unknown", status: 200, code: "http.unreadable_answer", progress: progress() };
            }
            if (eventId !== "" && res.event_id !== eventId) {
              return { kind: "unknown", status: 200, code: "http.unexpected_event", progress: progress() };
            }
            eventId = res.event_id;
            sessionIds.push(res.session_id);
            if (i === 0) created = res.created !== false;
          } catch (err) {
            return withProgress(classifySaveError(err), progress());
          }
        }
        return { kind: "saved", eventId, sessionId: sessionIds[0], sessionIds, created };
      };
      current = run().finally(() => {
        current = null;
      });
      return current;
    },
  };
}

export type SaveBlocker = "venueCreate";

/**
 * What stops a draft that is otherwise correct: a new venue needs
 * `venue.create` (and `city.create`, granted with it). Owners and managers
 * hold both; the screen offers "A new venue" only to them, so this guards a
 * draft restored by another role.
 */
export function saveBlockers(draft: WizardDraft, canCreateVenue: boolean): SaveBlocker[] {
  return draft.venue.mode === "new" && !canCreateVenue ? ["venueCreate"] : [];
}
