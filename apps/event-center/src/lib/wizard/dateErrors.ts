/**
 * Errors of the sections "Dates" and "Tickets" (task 6-1b): the server's check
 * for one date's bundle, turned into the fields of that date, and the few
 * checks the wizard makes itself.
 *
 * A field of a date has a key `<date key>.<suffix>`: `day`, `time`,
 * `endTime`, `salesEnd`, `doors`, `capacity`, `tickets` (the whole set of
 * categories), `c:<category key>.<field>` and `s:<step key>.<field>`. Keys
 * use the rows' own keys, never their position, so a removed date does not
 * move the errors of the next one.
 *
 * Pure: no React, no network.
 */
import type { MessageKey } from "@/lib/i18n/en";
import { effectiveTickets, type DateEntry, type TicketsPart } from "@/lib/wizard/datesDraft";
import type { WizardDraft } from "@/lib/wizard/draft";
import { venueCapacity } from "@/lib/wizard/draft";
import { doorsOffset, parseCount, parsePriceMinor, parseTime, salesEndOffset } from "@/lib/wizard/parse";
import type { CheckError } from "@/lib/wizard/validation";

export function dateFieldKey(dateKey: string, suffix: string): string {
  return `${dateKey}.${suffix}`;
}

export const catSuffix = (catKey: string, field: string): string => `c:${catKey}.${field}`;
export const stepSuffix = (stepKey: string, field: string): string => `s:${stepKey}.${field}`;

export interface DateErrors {
  /** Errors by field key (see above), in the order found. */
  byKey: Map<string, CheckError[]>;
  /** Errors about this date that no field of the form holds. */
  general: CheckError[];
}

const CATEGORY_POINTER = /^\/categoryList\/(\d+)\/([A-Za-z]+)$/;

/**
 * Where the errors of ONE date's bundle belong. `tickets` is what the date
 * really sells (its own or the date before's), because a category pointer
 * counts positions in that list. A date that sells what the date before sells
 * (`sameAsBefore`) shows category problems as one problem of its tickets.
 */
export function mapDateErrors(date: DateEntry, tickets: TicketsPart, errors: readonly CheckError[], sameAsBefore = false): DateErrors {
  const out: DateErrors = { byKey: new Map(), general: [] };
  const put = (suffix: string, err: CheckError) => {
    const key = dateFieldKey(date.key, suffix);
    const list = out.byKey.get(key);
    if (list === undefined) out.byKey.set(key, [err]);
    else list.push(err);
  };
  for (const err of errors) {
    const p = err.field;
    switch (p) {
      case "/actionEvent/day":
        put("day", err);
        continue;
      case "/actionEvent/time":
        put("time", err);
        continue;
      case "/actionEvent/endTime":
        put("endTime", err);
        continue;
      case "/actionEvent/sellEndTime":
        put("salesEnd", err);
        continue;
      case "/actionEvent/doorsOpenTime":
        put("doors", err);
        continue;
      case "/categoryList":
      case "/actionEvent/sellStartTime":
        put("tickets", err);
        continue;
    }
    const m = CATEGORY_POINTER.exec(p);
    if (m !== null) {
      const cat = tickets.categories[Number(m[1])];
      // A date that sells what the date before sells has no category fields of its own.
      if (cat === undefined || sameAsBefore) {
        put("tickets", err);
        continue;
      }
      switch (m[2]) {
        case "categoryPriceName":
          put(catSuffix(cat.key, "name"), err);
          break;
        case "price":
          put(catSuffix(cat.key, "price"), err);
          break;
        case "availability":
          put(tickets.mode === "parallel" ? catSuffix(cat.key, "places") : "capacity", err);
          break;
        case "sellEndTime":
        case "sellStartTime":
          put(tickets.mode === "sequence" ? catSuffix(cat.key, "until") : "tickets", err);
          break;
        case "sellLimit":
          put(tickets.mode === "sequence" ? catSuffix(cat.key, "limit") : "tickets", err);
          break;
        default:
          put("tickets", err);
      }
      continue;
    }
    if (p.startsWith("/categoryList/")) {
      put("tickets", err);
      continue;
    }
    out.general.push(err);
  }
  return out;
}

/** What a date's fields need to show their errors and to mark themselves as reached. */
export interface DateErrorView {
  /** The text under a field of a date (`suffix` as in the module header); undefined = no error to show. */
  errorOf: (dateKey: string, suffix: string) => string | undefined;
  /** Texts about the date that no field holds. */
  generalOf: (dateKey: string) => string[];
  /** The person reached a field (key = dateFieldKey(dateKey, suffix)). */
  touch: (fieldKey: string) => void;
}

export type ClientDateErrors = Map<string, MessageKey>;

function nonEmpty(s: string): boolean {
  return s.trim() !== "";
}

/**
 * The checks about the SHAPE of what a date holds, which the wizard makes
 * itself and which the server would meet as a bad value rather than a field
 * error. Empty fields are not shape errors: the server reports a missing day
 * or time in its own words.
 */
export function dateClientErrors(draft: WizardDraft): ClientDateErrors {
  const out: ClientDateErrors = new Map();
  draft.dates.forEach((date, index) => {
    const set = (suffix: string, key: MessageKey) => out.set(dateFieldKey(date.key, suffix), key);
    const time = parseTime(date.time);
    // The platform reads an empty start time as midnight, which nobody means: the wizard asks for it.
    if (!nonEmpty(date.time)) set("time", "wizard.err.timeRequired");
    else if (time === null) set("time", "wizard.err.timeShape");
    if (nonEmpty(date.endTime) && parseTime(date.endTime) === null) set("endTime", "wizard.err.timeShape");
    if (nonEmpty(date.salesEnd)) {
      const typed = parseTime(date.salesEnd);
      if (typed === null || (time !== null && salesEndOffset(time, typed) === null)) set("salesEnd", "wizard.err.salesEndShape");
    }
    if (nonEmpty(date.doors)) {
      const typed = parseTime(date.doors);
      if (typed === null || (time !== null && doorsOffset(time, typed) === null)) set("doors", "wizard.err.doorsShape");
    }
    const tk = effectiveTickets(draft.dates, index);
    // Places: typed ones must be a count; empty ones need the venue's seats to fall back on.
    if (tk.mode !== "parallel") {
      if (nonEmpty(date.capacity) && parseCount(date.capacity) === null) set("capacity", "wizard.err.placesShape");
      else if (!nonEmpty(date.capacity) && venueCapacity(draft.venue) === null) set("capacity", "wizard.err.placesRequired");
    }
    if (date.sameTickets && index > 0) return; // the categories are shown, and judged, on the date they belong to
    checkTickets(tk, set);
  });
  return out;
}

function checkTickets(tk: TicketsPart, set: (suffix: string, key: MessageKey) => void): void {
  const names = new Map<string, number>();
  tk.categories.forEach((c, j) => {
    if (c.name.trim() === "") set(catSuffix(c.key, "name"), "wizard.err.catName");
    else {
      const lower = c.name.trim().toLowerCase();
      names.set(lower, (names.get(lower) ?? 0) + 1);
    }
    if (nonEmpty(c.price) && parsePriceMinor(c.price) === null) set(catSuffix(c.key, "price"), "wizard.err.priceShape");
    else if (!nonEmpty(c.price)) set(catSuffix(c.key, "price"), "wizard.err.priceRequired");
    if (tk.mode === "parallel") {
      if (parseCount(c.places) === null) set(catSuffix(c.key, "places"), "wizard.err.placesShape");
    }
    if (tk.mode === "sequence" && j + 1 < tk.categories.length) {
      if (nonEmpty(c.limit) && parseCount(c.limit) === null) set(catSuffix(c.key, "limit"), "wizard.err.limitShape");
      // A step that is not the last ends by a day, by a count, or by both.
      if (c.until === "" && !nonEmpty(c.limit)) set(catSuffix(c.key, "until"), "wizard.err.untilRequired");
    }
  });
  tk.categories.forEach((c) => {
    if (c.name.trim() !== "" && (names.get(c.name.trim().toLowerCase()) ?? 0) > 1) set(catSuffix(c.key, "name"), "wizard.err.catDup");
  });
  if (tk.mode === "sequence") {
    // The days of the steps never go back: each step sells at least as long as the one before.
    let prev = "";
    tk.categories.forEach((c, j) => {
      if (j + 1 >= tk.categories.length || c.until === "") return;
      if (prev !== "" && c.until < prev) set(catSuffix(c.key, "until"), "wizard.err.untilOrder");
      prev = c.until;
    });
  }
  if (tk.mode === "single") {
    let prev = "";
    for (const s of tk.schedule) {
      if (s.from === "") set(stepSuffix(s.key, "from"), "wizard.err.stepFrom");
      else if (prev !== "" && s.from <= prev) set(stepSuffix(s.key, "from"), "wizard.err.stepOrder");
      if (s.from !== "") prev = s.from;
      if (!nonEmpty(s.price)) set(stepSuffix(s.key, "price"), "wizard.err.priceRequired");
      else if (parsePriceMinor(s.price) === null) set(stepSuffix(s.key, "price"), "wizard.err.priceShape");
    }
  }
}
