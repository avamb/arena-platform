/**
 * Turns the wizard draft into event-bundle requests (tasks 6-1a and 6-1b).
 *
 * `buildBundle` is the web twin of the bot's `BuildBundle`
 * (apps/backend/internal/platform/eventbot/wizard_io.go) and the ONLY place a
 * draft becomes a bundle: a rule about what goes on the wire belongs here,
 * never in a screen. The import takes ONE date per request, so a draft with
 * three dates gives three bundles (`buildBundles`), each with its own
 * idempotency key.
 *
 * The tests compare its output with golden bundles written by the bot's own
 * `BuildBundle` for the same answers (apps/backend/internal/platform/eventbot/
 * testdata/wizard_bundles, `bundle.golden.test.ts`): the field names, the
 * money in MAJOR units, the instants in the venue's zone and the chain of
 * categories are the bot's. The venue block is 6-1a's own (a new venue is only
 * described here; creating it is task 6-1c).
 *
 * It is a pure function of the draft and an options object.
 */
import { venueCapacity, venueTimezone, type WizardDraft } from "@/lib/wizard/draft";
import { effectiveTickets, type DateEntry, type TicketsPart } from "@/lib/wizard/datesDraft";
import { doorsOffset, majorOf, parseCount, parsePriceMinor, parseTime, salesEndOffset } from "@/lib/wizard/parse";
import { isSeated, seatedCategoryList } from "@/lib/wizard/seating";
import { endOfInclusiveDayRfc3339, formatRfc3339, startOfDayRfc3339, wallToEpoch } from "@/lib/wizard/zones";

export interface BundleAction {
  actionName: string;
  /** ART, the editor's HTML subset (spec 44, RT-11); the server filters it once more. */
  description: string;
  /** The wizard always writes ART: the server reads `description` with this flag and never guesses it. */
  descriptionFormat: "art";
  age: string;
  /** "" = the organization itself promotes the event (the bot sends it too). */
  promoterId: string;
}

export interface BundleActionEvent {
  /** DD.MM.YYYY, the wire format of the bot's `DisplayDate`. */
  day: string;
  /** HH:MM in the venue's zone. */
  time: string;
  currency: string;
  /** Optional HH:MM end in the venue's zone. */
  endTime?: string;
  /** RFC 3339, in the venue's zone: when ticket sales close. */
  sellEndTime?: string;
  /** RFC 3339, in the venue's zone: when the doors open. */
  doorsOpenTime?: string;
  /** The seating plan version a new date is sold by (seating.ts); absent = free seating. */
  arenaSeatingPlanVersionId?: string;
}

export interface BundleVenue {
  arenaVenueId?: string;
  venueName?: string;
  timezone?: string;
  cityName?: string;
  /** ISO 3166-1 alpha-2; the import reads a two-letter country name as a code. */
  countryName?: string;
}

export interface BundlePriceWindow {
  validFrom: string;
  validTo?: string;
  /** MAJOR units. */
  price: number;
}

export interface BundleCategory {
  categoryPriceName: string;
  /** MAJOR units, like the bot's `money.Major`. */
  price: number;
  availability: number;
  /** The index of the category that takes over; -1 = none (the bot sends it on every category). */
  nextCategoryIndex: number;
  /** A step that sells at most this many tickets; 0 = no limit (sequence mode only). */
  sellLimit?: number;
  sellStartTime?: string;
  sellEndTime?: string;
  /** Always a list: an empty one clears the stored schedule, like the bot's. */
  priceSchedule: BundlePriceWindow[];
  /** A seated date only: whether the category sells seats (false for a standing zone). */
  placement?: boolean;
  /** A seated date only: the 1-based plan category this entry prices. */
  planCategoryIndex?: number;
  /** A seated date only: an extra price category over the plan's own, without places. */
  planExtra?: boolean;
  /** #rrggbb on the seat map. */
  color?: string;
}

export interface EventBundle {
  source: "arena";
  externalRef: string;
  action: BundleAction;
  actionEvent: BundleActionEvent;
  venue: BundleVenue;
  categoryList: BundleCategory[];
  publish: boolean;
}

export interface BuildOptions {
  /** The date's idempotency key; the default names the draft (`web:<org>:<draft>:<n>`). */
  externalRef?: string;
  orgId: string;
}

/** The wire form of a day, the bot's `DisplayDate`: DD.MM.YYYY. */
export function wireDay(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  return m === null ? iso : `${m[3]}.${m[2]}.${Number(m[1])}`;
}

/** The places of a date: the plan's for a seated one, else what was typed, else the venue's seats, else 0. */
export function datePlaces(draft: WizardDraft, date: DateEntry): number {
  if (isSeatedDraft(draft)) return draft.seating.seats;
  return parseCount(date.capacity) ?? venueCapacity(draft.venue) ?? 0;
}

/** True when the dates are sold by a hall plan: one is chosen, at an existing venue. */
export function isSeatedDraft(draft: WizardDraft): boolean {
  return draft.venue.mode === "existing" && isSeated(draft.seating);
}

/** The sales end of a date as minutes after the start; 0 when none is typed or it cannot be read. */
function salesEndMin(start: string, typed: string): number {
  if (typed.trim() === "") return 0;
  return salesEndOffset(start, parseTime(typed) ?? "") ?? 0;
}

function categoryList(tk: TicketsPart, places: number, tz: string): BundleCategory[] {
  const out: BundleCategory[] = [];
  const price = (raw: string) => majorOf(parsePriceMinor(raw) ?? 0);
  switch (tk.mode) {
    case "single": {
      const c = tk.categories[0];
      if (c === undefined) return out;
      const steps = tk.schedule.filter((s) => s.from !== "");
      const windows: BundlePriceWindow[] = steps.map((s, i) => {
        const w: BundlePriceWindow = { validFrom: startOfDayRfc3339(s.from, tz), price: price(s.price) };
        if (i + 1 < steps.length) w.validTo = startOfDayRfc3339(steps[i + 1].from, tz);
        return w;
      });
      out.push({
        categoryPriceName: c.name.trim(),
        price: price(c.price),
        availability: places,
        nextCategoryIndex: -1,
        priceSchedule: windows,
      });
      return out;
    }
    case "parallel":
      for (const c of tk.categories) {
        out.push({
          categoryPriceName: c.name.trim(),
          price: price(c.price),
          availability: parseCount(c.places) ?? 0,
          nextCategoryIndex: -1,
          priceSchedule: [],
        });
      }
      return out;
    case "sequence": {
      const n = tk.categories.length;
      tk.categories.forEach((c, i) => {
        const cat: BundleCategory = {
          categoryPriceName: c.name.trim(),
          price: price(c.price),
          availability: i === 0 ? places : 0,
          nextCategoryIndex: -1,
          priceSchedule: [],
        };
        if (i + 1 < n) {
          cat.nextCategoryIndex = i + 1;
          cat.sellLimit = parseCount(c.limit) ?? 0;
          if (c.until !== "") {
            // Inclusive day: the step ends when the next day begins.
            cat.sellEndTime = endOfInclusiveDayRfc3339(c.until, tz);
          }
        } else {
          cat.sellLimit = 0; // the last one sells until the event starts
        }
        out.push(cat);
      });
      // A category that follows a date-only step starts at that step's end.
      for (let i = 1; i < out.length; i += 1) {
        const prev = tk.categories[i - 1];
        if (prev.until !== "" && parseCount(prev.limit) === null) out[i].sellStartTime = out[i - 1].sellEndTime;
      }
      return out;
    }
  }
}

function venueBlock(draft: WizardDraft): BundleVenue {
  const v = draft.venue;
  const venue: BundleVenue = {};
  if (v.mode === "existing") {
    venue.arenaVenueId = v.existingId;
    // The stored zone of an existing venue always wins on the server; sending
    // it keeps the bundle self-describing and costs nothing.
    if (v.existingTimezone !== "") venue.timezone = v.existingTimezone;
  } else if (v.mode === "new") {
    venue.venueName = v.newName.trim();
    if (v.timezone !== "") venue.timezone = v.timezone;
    if (v.cityName.trim() !== "") venue.cityName = v.cityName.trim();
    if (v.countryIso2 !== "") venue.countryName = v.countryIso2;
  }
  return venue;
}

/** The bundle of date number `index` (0-based). */
export function buildBundle(draft: WizardDraft, opts: BuildOptions, index = 0): EventBundle {
  const e = draft.event;
  const date = draft.dates[index];
  const tz = venueTimezone(draft.venue);
  const tickets = effectiveTickets(draft.dates, index);
  const time = parseTime(date.time) ?? date.time.trim();

  const actionEvent: BundleActionEvent = {
    day: wireDay(date.day),
    time,
    currency: draft.currency.trim().toUpperCase(),
  };
  const end = date.endTime.trim();
  if (end !== "") actionEvent.endTime = parseTime(end) ?? end;

  // Sales end and doors open (migration 0128): offsets from the start, sent as
  // instants in the venue's zone, only when the start is a real day and time.
  const start = wallToEpoch(date.day, time, tz);
  if (start !== null) {
    const salesMin = salesEndMin(time, date.salesEnd);
    actionEvent.sellEndTime = formatRfc3339(start + salesMin * 60_000, tz);
    const before = date.doors.trim() === "" ? null : doorsOffset(time, parseTime(date.doors) ?? "");
    if (before !== null) actionEvent.doorsOpenTime = formatRfc3339(start - before * 60_000, tz);
  }
  const seated = isSeatedDraft(draft);
  if (seated) actionEvent.arenaSeatingPlanVersionId = draft.seating.versionId;

  return {
    source: "arena",
    externalRef: opts.externalRef ?? `web:${opts.orgId}:${draft.id}:${index + 1}`,
    action: {
      actionName: e.name.trim(),
      description: e.description.trim(),
      descriptionFormat: "art",
      age: e.age,
      promoterId: e.promoterId,
    },
    actionEvent,
    venue: venueBlock(draft),
    // A date sold by a plan prices the plan's categories (and the extra ones), never the ticket editor's.
    categoryList: seated ? seatedCategoryList(draft.seating) : categoryList(tickets, datePlaces(draft, date), tz),
    publish: false,
  };
}

/** One bundle per date, in the order of the draft. */
export function buildBundles(draft: WizardDraft, opts: Omit<BuildOptions, "externalRef">): EventBundle[] {
  return draft.dates.map((_, i) => buildBundle(draft, opts, i));
}
