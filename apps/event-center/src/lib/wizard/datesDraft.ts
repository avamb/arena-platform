/**
 * The "Dates" and "Tickets" part of the wizard draft (task 6-1b): the dates of
 * the event and, for each date, how its categories are sold. Pure types and
 * reducer rules, free of React. `draft.ts` holds the draft that owns them.
 *
 * The model is the bot's (`Draft.Sessions`, `DraftSession`, `DraftTickets`,
 * `DraftCategory`, wizard.go / wizard_date_tickets.go) with one difference in
 * shape: every date carries its own tickets, or says "same as the date before"
 * (the bot's U-5 per-date categories). Numbers and times stay as typed;
 * `parse.ts` reads them and `bundle.ts` builds the bundle.
 */
import type { WizardDraft } from "@/lib/wizard/draft";

/**
 * How the categories of a date are sold, the bot's three modes
 * (`ModeSingle`, `ModeParallel`, `ModeSequence`): one category with optional
 * price changes, several at once, or several one after another.
 */
export type TicketMode = "single" | "parallel" | "sequence";
export const TICKET_MODES: readonly TicketMode[] = ["single", "parallel", "sequence"];

export function isTicketMode(v: unknown): v is TicketMode {
  return v === "single" || v === "parallel" || v === "sequence";
}

/** One category. Numbers stay as typed. */
export interface CategoryEntry {
  /** Names the row for React and for error keys; never sent. */
  key: string;
  name: string;
  /** Price in major units as typed ("18,90"); "0" is a free category. */
  price: string;
  /** Parallel mode: this category's own places. */
  places: string;
  /** Sequence mode: the last day this step sells (ISO, inclusive); "" = no date. */
  until: string;
  /** Sequence mode: the step sells this many tickets at most; "" = no limit. */
  limit: string;
}

/** One "from this day the price is ..." row of a single category. */
export interface PriceStepEntry {
  key: string;
  /** ISO day. */
  from: string;
  price: string;
}

export interface TicketsPart {
  mode: TicketMode;
  categories: CategoryEntry[];
  /** Single mode only: the price changes, oldest first. */
  schedule: PriceStepEntry[];
}

/** One date (session) of the event. */
export interface DateEntry {
  key: string;
  /** ISO day, "" = not chosen. */
  day: string;
  /** Start time as typed ("19:00", "1900"). */
  time: string;
  /** Optional end time as typed; "" = the platform's default length. */
  endTime: string;
  /** Sales end as a typed clock time; "" = when the event starts. */
  salesEnd: string;
  /** Doors open as a typed clock time; "" = not given. */
  doors: string;
  /** Places of this date as typed; "" = the venue's seats. Single and sequence modes. */
  capacity: string;
  /** From the second date on: sell what the date before sells (its `tickets` is then unused). */
  sameTickets: boolean;
  tickets: TicketsPart;
}

/** The most dates one event can have in the wizard (a guard for the browser, not a platform rule). */
export const MAX_DATES = 60;
/** The most categories one date can have. */
export const MAX_CATEGORIES = 20;
/** The most price changes of one single category. */
export const MAX_PRICE_STEPS = 20;

export function emptyCategory(key: string, name = ""): CategoryEntry {
  return { key, name, price: "", places: "", until: "", limit: "" };
}

export function emptyTickets(categoryKey: string, defaultName: string): TicketsPart {
  return { mode: "single", categories: [emptyCategory(categoryKey, defaultName)], schedule: [] };
}

export function emptyDate(key: string, defaultName: string): DateEntry {
  return {
    key,
    day: "",
    time: "",
    endTime: "",
    salesEnd: "",
    doors: "",
    capacity: "",
    sameTickets: false,
    tickets: emptyTickets(`${key}-c1`, defaultName),
  };
}

export type DateAction =
  | { type: "dateAdd"; key: string; defaultName: string }
  | { type: "dateRemove"; key: string }
  | { type: "dateDay"; key: string; value: string }
  | { type: "dateField"; key: string; field: "time" | "endTime" | "salesEnd" | "doors" | "capacity"; value: string }
  | { type: "dateSame"; key: string; same: boolean }
  | { type: "ticketMode"; key: string; mode: TicketMode; catKey: string }
  | { type: "catAdd"; key: string; catKey: string }
  | { type: "catRemove"; key: string; catKey: string }
  | { type: "catField"; key: string; catKey: string; field: "name" | "price" | "places" | "until" | "limit"; value: string }
  | { type: "stepAdd"; key: string; stepKey: string }
  | { type: "stepRemove"; key: string; stepKey: string }
  | { type: "stepField"; key: string; stepKey: string; field: "from" | "price"; value: string };

export const DATE_ACTION_TYPES: ReadonlySet<string> = new Set([
  "dateAdd",
  "dateRemove",
  "dateDay",
  "dateField",
  "dateSame",
  "ticketMode",
  "catAdd",
  "catRemove",
  "catField",
  "stepAdd",
  "stepRemove",
  "stepField",
]);

export function isDateAction(a: { type: string }): a is DateAction {
  return DATE_ACTION_TYPES.has(a.type);
}

function mapDate(d: WizardDraft, key: string, fn: (e: DateEntry, index: number) => DateEntry): WizardDraft {
  const i = d.dates.findIndex((x) => x.key === key);
  if (i < 0) return d;
  const next = fn(d.dates[i], i);
  if (next === d.dates[i]) return d;
  return { ...d, dates: d.dates.map((x, j) => (j === i ? next : x)) };
}

export function cloneTickets(t: TicketsPart): TicketsPart {
  return { mode: t.mode, categories: t.categories.map((c) => ({ ...c })), schedule: t.schedule.map((s) => ({ ...s })) };
}

/**
 * The tickets date `index` really sells: its own, or (the second date on,
 * "same as the date before") those of the date before, followed back.
 */
export function effectiveTickets(dates: readonly DateEntry[], index: number): TicketsPart {
  let i = index;
  while (i > 0 && dates[i].sameTickets) i -= 1;
  return dates[i].tickets;
}

/** A mode change keeps what fits: one category for "single", at least two for the others. */
function switchMode(t: TicketsPart, mode: TicketMode, catKey: string): TicketsPart {
  if (mode === "single") return { mode, categories: t.categories.slice(0, 1).map((c) => ({ ...c })), schedule: t.schedule.map((s) => ({ ...s })) };
  const cats = t.categories.map((c) => ({ ...c }));
  let n = 1;
  while (cats.length < 2) {
    cats.push(emptyCategory(`${catKey}-${n}`));
    n += 1;
  }
  return { mode, categories: cats, schedule: [] };
}

/** Folds one answer about the dates or the tickets into the draft. */
export function applyDateAction(d: WizardDraft, a: DateAction): WizardDraft {
  switch (a.type) {
    case "dateAdd": {
      if (d.dates.length >= MAX_DATES) return d;
      const prev = d.dates[d.dates.length - 1];
      // A new date starts like the one before: its start time, its places and its categories.
      const next: DateEntry = {
        ...emptyDate(a.key, a.defaultName),
        time: prev?.time ?? "",
        capacity: prev?.capacity ?? "",
        sameTickets: d.dates.length > 0,
      };
      return { ...d, dates: [...d.dates, next] };
    }
    case "dateRemove":
      if (d.dates.length <= 1) return d;
      return { ...d, dates: d.dates.filter((x) => x.key !== a.key) };
    case "dateDay":
      return mapDate(d, a.key, (e) => (e.day === a.value ? e : { ...e, day: a.value }));
    case "dateField":
      return mapDate(d, a.key, (e) => (e[a.field] === a.value ? e : { ...e, [a.field]: a.value }));
    case "dateSame":
      return mapDate(d, a.key, (e, i) => {
        if (i === 0 || e.sameTickets === a.same) return e;
        // Leaving "same as before" starts from what the date before sells.
        return a.same ? { ...e, sameTickets: true } : { ...e, sameTickets: false, tickets: cloneTickets(effectiveTickets(d.dates, i - 1)) };
      });
    case "ticketMode":
      return mapDate(d, a.key, (e) => (e.tickets.mode === a.mode ? e : { ...e, tickets: switchMode(e.tickets, a.mode, a.catKey) }));
    case "catAdd":
      return mapDate(d, a.key, (e) => {
        if (e.tickets.mode === "single" || e.tickets.categories.length >= MAX_CATEGORIES) return e;
        return { ...e, tickets: { ...e.tickets, categories: [...e.tickets.categories, emptyCategory(a.catKey)] } };
      });
    case "catRemove":
      return mapDate(d, a.key, (e) => {
        if (e.tickets.mode === "single" || e.tickets.categories.length <= 2) return e;
        return { ...e, tickets: { ...e.tickets, categories: e.tickets.categories.filter((c) => c.key !== a.catKey) } };
      });
    case "catField":
      return mapDate(d, a.key, (e) => {
        const cats = e.tickets.categories.map((c) => (c.key === a.catKey && c[a.field] !== a.value ? { ...c, [a.field]: a.value } : c));
        return cats.every((c, i) => c === e.tickets.categories[i]) ? e : { ...e, tickets: { ...e.tickets, categories: cats } };
      });
    case "stepAdd":
      return mapDate(d, a.key, (e) => {
        if (e.tickets.mode !== "single" || e.tickets.schedule.length >= MAX_PRICE_STEPS) return e;
        return { ...e, tickets: { ...e.tickets, schedule: [...e.tickets.schedule, { key: a.stepKey, from: "", price: "" }] } };
      });
    case "stepRemove":
      return mapDate(d, a.key, (e) => {
        const steps = e.tickets.schedule.filter((s) => s.key !== a.stepKey);
        return steps.length === e.tickets.schedule.length ? e : { ...e, tickets: { ...e.tickets, schedule: steps } };
      });
    case "stepField":
      return mapDate(d, a.key, (e) => {
        const steps = e.tickets.schedule.map((s) => (s.key === a.stepKey && s[a.field] !== a.value ? { ...s, [a.field]: a.value } : s));
        return steps.every((s, i) => s === e.tickets.schedule[i]) ? e : { ...e, tickets: { ...e.tickets, schedule: steps } };
      });
  }
}

/** A date nobody has touched: no day, time or numbers, one unpriced category. */
export function isBlankDate(e: DateEntry): boolean {
  const t = e.tickets;
  const c = t.categories[0];
  return (
    e.day === "" &&
    e.time === "" &&
    e.endTime === "" &&
    e.salesEnd === "" &&
    e.doors === "" &&
    e.capacity === "" &&
    t.mode === "single" &&
    t.categories.length === 1 &&
    t.schedule.length === 0 &&
    c !== undefined &&
    c.price === "" &&
    c.places === "" &&
    c.until === "" &&
    c.limit === ""
  );
}

// ─── reading a stored draft ──────────────────────────────────────────────────

function str(v: unknown): string | null {
  return typeof v === "string" ? v : null;
}

function obj(v: unknown): Record<string, unknown> | null {
  return v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
}

function parseCategory(raw: unknown): CategoryEntry | null {
  const o = obj(raw);
  if (o === null) return null;
  const [key, name, price, places, until, limit] = [o.key, o.name, o.price, o.places, o.until, o.limit].map(str);
  if (key === null || name === null || price === null || places === null || until === null || limit === null) return null;
  return { key, name, price, places, until, limit };
}

function parseStep(raw: unknown): PriceStepEntry | null {
  const o = obj(raw);
  if (o === null) return null;
  const [key, from, price] = [o.key, o.from, o.price].map(str);
  if (key === null || from === null || price === null) return null;
  return { key, from, price };
}

function parseTickets(raw: unknown): TicketsPart | null {
  const o = obj(raw);
  if (o === null || !isTicketMode(o.mode) || !Array.isArray(o.categories) || !Array.isArray(o.schedule)) return null;
  const categories = o.categories.map(parseCategory);
  const schedule = o.schedule.map(parseStep);
  if (categories.some((c) => c === null) || schedule.some((s) => s === null) || categories.length === 0) return null;
  return { mode: o.mode, categories: categories as CategoryEntry[], schedule: schedule as PriceStepEntry[] };
}

/** Checks the dates of a stored draft field by field; null when anything is off. */
export function parseDates(raw: unknown): DateEntry[] | null {
  if (!Array.isArray(raw) || raw.length === 0 || raw.length > MAX_DATES) return null;
  const out: DateEntry[] = [];
  for (const item of raw) {
    const o = obj(item);
    if (o === null) return null;
    const [key, day, time, endTime, salesEnd, doors, capacity] = [o.key, o.day, o.time, o.endTime, o.salesEnd, o.doors, o.capacity].map(str);
    const tickets = parseTickets(o.tickets);
    if (key === null || day === null || time === null || endTime === null || salesEnd === null || doors === null || capacity === null) return null;
    if (typeof o.sameTickets !== "boolean" || tickets === null) return null;
    out.push({ key, day, time, endTime, salesEnd, doors, capacity, sameTickets: o.sameTickets, tickets });
  }
  if (out[0].sameTickets) return null; // the first date has nothing before it
  return out;
}
