/**
 * Turns the wizard draft into an event-bundle request (task 6-1a).
 *
 * `buildBundle` is the web twin of the bot's `BuildBundle`
 * (apps/backend/internal/platform/eventbot/wizard_io.go) and the ONLY place a
 * draft becomes a bundle: a rule about what goes on the wire belongs here,
 * never in a screen. Task 6-1b compares its bytes with the bot's for the same
 * answers, so the field names and the shapes below are the bot's.
 *
 * It is a pure function of the draft and an options object (the clock and the
 * idempotency key are passed in).
 *
 * The sections "Event" and "Venue" are all the wizard has in 6-1a. The
 * server's check needs a date and a category too, so until the later sections
 * exist the bundle carries a STUB of each (`stubbedParts`). A stub is never
 * shown to the person: the answers the check gives about a stub field are
 * filtered out (`validation.ts`) and are not errors of the sections on screen.
 * The stub is also the reason a bundle built here must never be SAVED before
 * 6-1c replaces it with real dates and categories.
 */
import { venueCapacity, type WizardDraft } from "@/lib/wizard/draft";

export interface BundleAction {
  actionName: string;
  description: string;
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
}

export interface BundleVenue {
  arenaVenueId?: string;
  venueName?: string;
  timezone?: string;
  cityName?: string;
  /** ISO 3166-1 alpha-2; the import reads a two-letter country name as a code. */
  countryName?: string;
}

export interface BundleCategory {
  categoryPriceName: string;
  /** MAJOR units, like the bot's `money.Major`. */
  price: number;
  availability: number;
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
  /** The clock: the stub date is a year after it. */
  now: Date;
  /** The date's idempotency key; the default names the draft (`web:<org>:<draft>:1`). */
  externalRef?: string;
  orgId: string;
}

/** What of the bundle stands in for a section that does not exist yet. */
export interface StubbedParts {
  date: boolean;
  categories: boolean;
}

/** In 6-1a the wizard has neither dates nor categories. 6-1b makes this depend on the draft. */
export function stubbedParts(_draft: WizardDraft): StubbedParts {
  return { date: true, categories: true };
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** The stub date: the day a year after `now`, in the bot's DD.MM.YYYY form, at 19:00. */
export function stubDay(now: Date): string {
  const t = new Date(Date.UTC(now.getUTCFullYear() + 1, now.getUTCMonth(), now.getUTCDate()));
  return `${pad(t.getUTCDate())}.${pad(t.getUTCMonth() + 1)}.${t.getUTCFullYear()}`;
}

export const STUB_TIME = "19:00";

export function buildBundle(draft: WizardDraft, opts: BuildOptions): EventBundle {
  const e = draft.event;
  const v = draft.venue;
  const stub = stubbedParts(draft);

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

  return {
    source: "arena",
    externalRef: opts.externalRef ?? `web:${opts.orgId}:${draft.id}:1`,
    action: {
      actionName: e.name.trim(),
      description: e.description.trim(),
      age: e.age,
      promoterId: e.promoterId,
    },
    actionEvent: {
      day: stub.date ? stubDay(opts.now) : "",
      time: stub.date ? STUB_TIME : "",
      currency: draft.currency.trim().toUpperCase(),
    },
    venue,
    categoryList: stub.categories
      ? [{ categoryPriceName: "Category 1", price: 1, availability: venueCapacity(v) ?? 1 }]
      : [],
    publish: false,
  };
}
