/**
 * What the poster said, turned into SUGGESTIONS for the wizard draft.
 *
 * Pure of React and of the network. The vision model's answer is untrusted
 * text: `parseFacts` keeps only values the wizard can use (a real calendar day,
 * a clock time, an age rating the platform knows, a price that is a whole number
 * of minor units), and a missing value stays empty, never guessed. Every text is
 * shown as plain text by the screen and never as HTML.
 *
 * `buildPlan` compares the facts with the CURRENT draft and says, for each
 * field, whether applying it would fill an empty field or REPLACE what the
 * person typed. Replacements are never on by default. `buildActions` turns the
 * choices into draft actions, so applying is the same reducer the typed edits use.
 *
 * Rules the plan keeps (product decisions of the poster-first release):
 *  - a venue is never created or chosen from a name alone: the venues of the
 *    organization that look like it are OFFERED, and one is chosen on purpose;
 *    only when none looks like it, the name and the city are offered as hints
 *    for a NEW venue;
 *  - the reader gives ONE date: it is offered for the FIRST date, the other
 *    dates stay as typed;
 *  - a price is offered only to an untouched single-category ticket setup (or as
 *    an explicit replacement of the one typed price), only with a currency that is
 *    known, and it never opens sales, changes places or other categories.
 */
import type { PosterFactsResponse } from "@/lib/api/types";
import { parseTime, formatPriceMinor, parsePriceMinor } from "@/lib/wizard/parse";
import {
  MAX_DESCRIPTION_RUNES,
  MAX_NAME_RUNES,
  isAgeRating,
  runeLength,
  validCurrency,
  type AgeRating,
  type DraftAction,
  type VenueChoice,
  type WizardDraft,
} from "@/lib/wizard/draft";
import { MAX_CATEGORIES } from "@/lib/wizard/datesDraft";

export interface PosterCategory {
  name: string;
  priceMinor: number;
  /** ISO 4217, "" = the poster shows no currency. */
  currency: string;
}

export interface PosterFacts {
  name: string;
  /** ISO day, "" = not on the poster. */
  date: string;
  /** "HH:MM", "" = not on the poster. */
  time: string;
  venueName: string;
  city: string;
  age: AgeRating | "";
  description: string;
  categories: PosterCategory[];
}

const MAX_CATEGORY_NAME_RUNES = 100;
const MAX_PRICE_MINOR = 99_999_999;
const MAX_VENUE_RUNES = 200;

/* eslint-disable-next-line no-control-regex */
const CONTROL = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g;

function text(v: unknown, maxRunes: number, multiline = false): string {
  if (typeof v !== "string") return "";
  let s = v.replace(CONTROL, "");
  if (!multiline) s = s.replace(/[\r\n\t]+/g, " ");
  s = s.trim();
  return runeLength(s) > maxRunes ? "" : s;
}

function isRealDay(s: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (m === null) return false;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  if (y < 1900 || y > 2200) return false;
  const t = new Date(Date.UTC(y, mo - 1, d));
  return t.getUTCFullYear() === y && t.getUTCMonth() === mo - 1 && t.getUTCDate() === d;
}

/** Reads the answer of the poster route. Null when it is not the expected shape at all. */
export function parseFacts(raw: unknown): PosterFacts | null {
  if (raw === null || typeof raw !== "object") return null;
  const f = (raw as Partial<PosterFactsResponse>).facts;
  if (f === null || f === undefined || typeof f !== "object") return null;
  const o = f as unknown as Record<string, unknown>;
  const date = typeof o.date === "string" && isRealDay(o.date.trim()) ? o.date.trim() : "";
  const time = typeof o.time === "string" && /^([01]\d|2[0-3]):[0-5]\d$/.test(o.time.trim()) ? o.time.trim() : "";
  const age = typeof o.age === "string" && isAgeRating(o.age.trim()) && o.age.trim() !== "NR" ? (o.age.trim() as AgeRating) : "";
  const categories: PosterCategory[] = [];
  if (Array.isArray(o.categories)) {
    for (const c of o.categories.slice(0, MAX_CATEGORIES)) {
      if (c === null || typeof c !== "object") continue;
      const r = c as Record<string, unknown>;
      const minor = r.price_minor;
      // A price must be a whole number of minor units: anything else is not a price we may show.
      if (typeof minor !== "number" || !Number.isSafeInteger(minor) || minor < 0 || minor > MAX_PRICE_MINOR) continue;
      categories.push({
        name: text(r.name, MAX_CATEGORY_NAME_RUNES),
        priceMinor: minor,
        currency: typeof r.currency === "string" ? validCurrency(r.currency) : "",
      });
    }
  }
  return {
    name: text(o.name, MAX_NAME_RUNES),
    date,
    time,
    venueName: text(o.venue_name, MAX_VENUE_RUNES),
    city: text(o.city, MAX_VENUE_RUNES),
    age,
    description: text(o.description, MAX_DESCRIPTION_RUNES, true),
    categories,
  };
}

/** True when the poster gave at least one usable value. */
export function hasUsableFacts(f: PosterFacts): boolean {
  return (
    f.name !== "" ||
    f.date !== "" ||
    f.time !== "" ||
    f.venueName !== "" ||
    f.city !== "" ||
    f.age !== "" ||
    f.description !== "" ||
    f.categories.length > 0
  );
}

// ─── the plan ────────────────────────────────────────────────────────────────

export type HintKey = "name" | "description" | "age" | "date" | "time" | "venueNew" | "city";

export interface PlanRow {
  key: HintKey;
  /** What the poster says. */
  value: string;
  /** What the form holds now ("" = empty). */
  current: string;
  /** Applying would REPLACE typed text. */
  conflict: boolean;
}

export interface PriceOption {
  /** Index in the facts' categories. */
  index: number;
  name: string;
  priceMinor: number;
  priceText: string;
  /** The currency the price will be shown in: the form's, else the poster's. */
  currency: string;
  /** The form has no currency yet: choosing this sets it to `currency`. */
  setsCurrency: boolean;
  /** The one typed price or category name would be replaced. */
  conflict: boolean;
}

export type PlanNote =
  /** The poster names a venue/city that is not applied (shown as a hint). */
  | { code: "venueName"; value: string }
  | { code: "city"; value: string }
  | { code: "priceSetup" }
  | { code: "priceCurrencyDiffers"; poster: string; form: string }
  | { code: "priceNoCurrency" }
  | { code: "moreCategories"; count: number };

export interface Plan {
  rows: PlanRow[];
  /** Venues of the organization that look like the poster's. Chosen on purpose, never preselected. */
  venueCandidates: VenueChoice[];
  /** The poster's venue name, shown beside the candidates. */
  venueName: string;
  prices: PriceOption[];
  notes: PlanNote[];
  /** Nothing to apply and nothing to say: every usable value is already in the form. */
  nothingNew: boolean;
}

export interface PlanContext {
  draft: WizardDraft;
  /** The venues the person may choose from; null when they may not list venues. */
  venues: readonly VenueChoice[] | null;
  /** The name of a fresh first category in the person's language ("Admission"): not typed text. */
  defaultCategoryName: string;
}

function normalize(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim();
}

/** Venues whose name equals the poster's or contains it (or the other way round) as a word run of 4+ letters. */
export function venueCandidatesFor(venueName: string, venues: readonly VenueChoice[]): VenueChoice[] {
  const wanted = normalize(venueName);
  if (wanted === "") return [];
  const hits = venues.filter((v) => {
    const n = normalize(v.name);
    if (n === "") return false;
    if (n === wanted) return true;
    const [short, long] = n.length <= wanted.length ? [n, wanted] : [wanted, n];
    return short.length >= 4 && ` ${long} `.includes(` ${short} `);
  });
  // An exact name first, then the rest by name; a short list: the person chooses, we do not guess.
  return hits.sort((a, b) => Number(normalize(b.name) === wanted) - Number(normalize(a.name) === wanted) || a.name.localeCompare(b.name)).slice(0, 5);
}

export function buildPlan(facts: PosterFacts, ctx: PlanContext): Plan {
  const { draft } = ctx;
  const rows: PlanRow[] = [];
  const notes: PlanNote[] = [];
  const first = draft.dates[0];
  const row = (key: HintKey, value: string, current: string, same: boolean) => {
    if (value === "" || same) return;
    rows.push({ key, value, current, conflict: current.trim() !== "" });
  };

  row("name", facts.name, draft.event.name, draft.event.name.trim() === facts.name);
  row("description", facts.description, draft.event.description, draft.event.description.trim() === facts.description);
  row("age", facts.age, draft.event.age, draft.event.age === facts.age);
  row("date", facts.date, first.day, first.day === facts.date);
  row("time", facts.time, first.time, parseTime(first.time) === facts.time);

  // The venue: candidates to choose from, or hints for a new one. Never a venue made from a name.
  const v = draft.venue;
  let venueCandidates: VenueChoice[] = [];
  if (facts.venueName !== "" && ctx.venues !== null) venueCandidates = venueCandidatesFor(facts.venueName, ctx.venues);
  const alreadyThere = v.mode === "existing" && venueCandidates.some((c) => c.id === v.existingId);
  if (alreadyThere) {
    venueCandidates = [];
  } else if (venueCandidates.length === 0) {
    if (facts.venueName !== "") {
      if (v.mode === "existing" && v.existingId !== "") notes.push({ code: "venueName", value: facts.venueName });
      else row("venueNew", facts.venueName, v.newName, v.newName.trim() === facts.venueName);
    }
    if (facts.city !== "") {
      if (v.mode === "new" && v.countryIso2 !== "") row("city", facts.city, v.cityName, v.cityName.trim() === facts.city);
      else if (!(v.mode === "new" && v.cityName.trim() === facts.city)) notes.push({ code: "city", value: facts.city });
    }
  }

  // Prices: only into an untouched single-category setup, and only with a known currency.
  const prices: PriceOption[] = [];
  if (facts.categories.length > 0) {
    const tk = first.tickets;
    const plainSingle = tk.mode === "single" && tk.categories.length === 1 && tk.schedule.length === 0;
    if (!plainSingle) {
      notes.push({ code: "priceSetup" });
    } else {
      const cat = tk.categories[0];
      const formCurrency = validCurrency(draft.currency);
      const typedPrice = cat.price.trim();
      const typedName = cat.name.trim();
      const differing = new Set<string>();
      let noCurrency = false;
      facts.categories.forEach((c, index) => {
        if (c.currency !== "" && formCurrency !== "" && c.currency !== formCurrency) {
          differing.add(c.currency);
          return;
        }
        const currency = formCurrency !== "" ? formCurrency : c.currency;
        if (currency === "") {
          noCurrency = true;
          return;
        }
        const nameTyped = typedName !== "" && typedName !== ctx.defaultCategoryName && c.name !== "" && typedName !== c.name;
        const priceTyped = typedPrice !== "" && parsePriceMinor(typedPrice) !== c.priceMinor;
        const nameSame = c.name === "" || typedName === c.name;
        if (typedPrice !== "" && !priceTyped && nameSame) return; // already exactly this
        prices.push({
          index,
          name: c.name,
          priceMinor: c.priceMinor,
          priceText: formatPriceMinor(c.priceMinor),
          currency,
          setsCurrency: formCurrency === "",
          conflict: nameTyped || priceTyped,
        });
      });
      for (const poster of differing) notes.push({ code: "priceCurrencyDiffers", poster, form: formCurrency });
      if (noCurrency) notes.push({ code: "priceNoCurrency" });
      if (facts.categories.length > 1 && prices.length > 0) notes.push({ code: "moreCategories", count: facts.categories.length - 1 });
    }
  }

  return {
    rows,
    venueCandidates,
    venueName: facts.venueName,
    prices,
    notes,
    nothingNew: rows.length === 0 && venueCandidates.length === 0 && prices.length === 0 && notes.length === 0,
  };
}

// ─── choices ─────────────────────────────────────────────────────────────────

/**
 * A choice remembers what the field held when it was made (`basis`). If the
 * person types in that field afterwards, the choice no longer counts: a
 * replacement the person never saw is never made.
 */
export interface Choice {
  on: boolean;
  basis: string;
}

/** A row is on by default only when it fills an EMPTY field; a replacement needs its own tick. */
export function isRowOn(row: PlanRow, choice: Choice | undefined): boolean {
  if (choice !== undefined && choice.basis === row.current) return choice.on;
  return !row.conflict;
}

export function venueBasis(d: WizardDraft): string {
  return `${d.venue.mode}:${d.venue.existingId}`;
}

export function priceBasis(d: WizardDraft): string {
  const c = d.dates[0].tickets.categories[0];
  return `${d.currency}|${c?.name ?? ""}|${c?.price ?? ""}|${d.dates[0].tickets.mode}`;
}

export interface Selection {
  rows: ReadonlyMap<HintKey, Choice>;
  venue: { id: string; basis: string } | null;
  price: { index: number; basis: string } | null;
}

export const NO_SELECTION: Selection = { rows: new Map(), venue: null, price: null };

export interface Built {
  actions: DraftAction[];
  /** What was applied, for the "applied" summary. */
  applied: Array<HintKey | "venue" | "price">;
}

/** The draft actions of the ticked rows, the chosen venue and the chosen price. Nothing else is touched. */
export function buildActions(plan: Plan, sel: Selection, draft: WizardDraft): Built {
  const actions: DraftAction[] = [];
  const applied: Built["applied"] = [];
  const first = draft.dates[0];
  for (const r of plan.rows) {
    if (!isRowOn(r, sel.rows.get(r.key))) continue;
    switch (r.key) {
      case "name":
      case "description":
        actions.push({ type: "event", field: r.key, value: r.value });
        break;
      case "age":
        if (isAgeRating(r.value)) actions.push({ type: "age", value: r.value });
        else continue;
        break;
      case "date":
        actions.push({ type: "dateDay", key: first.key, value: r.value });
        break;
      case "time":
        actions.push({ type: "dateField", key: first.key, field: "time", value: r.value });
        break;
      case "venueNew":
        actions.push({ type: "venueMode", mode: "new" }, { type: "venueNew", field: "newName", value: r.value });
        break;
      case "city":
        actions.push({ type: "venueNew", field: "cityName", value: r.value });
        break;
    }
    applied.push(r.key);
  }
  if (sel.venue !== null && sel.venue.basis === venueBasis(draft)) {
    const chosen = plan.venueCandidates.find((c) => c.id === sel.venue?.id);
    if (chosen !== undefined) {
      actions.push({ type: "venueExisting", venue: chosen });
      applied.push("venue");
    }
  }
  if (sel.price !== null && sel.price.basis === priceBasis(draft)) {
    const opt = plan.prices.find((p) => p.index === sel.price?.index);
    const cat = first.tickets.categories[0];
    if (opt !== undefined && cat !== undefined) {
      if (opt.setsCurrency) actions.push({ type: "currency", value: opt.currency, source: "typed" });
      if (opt.name !== "") actions.push({ type: "catField", key: first.key, catKey: cat.key, field: "name", value: opt.name });
      actions.push({ type: "catField", key: first.key, catKey: cat.key, field: "price", value: opt.priceText });
      applied.push("price");
    }
  }
  return { actions, applied };
}
