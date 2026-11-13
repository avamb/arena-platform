/**
 * The automatic layout (task S2c): the form is DRAWN from the schema the server
 * sends (GET .../seat-layout/algorithms), never from a list in this file, so a
 * new algorithm needs no change here. A proposal is only a preview until the
 * organizer presses "Move as changes": then it becomes strokes of the tray and
 * goes through the same batch as the brush.
 *
 * Pure: no DOM, no API.
 */
import type { SeatLayoutAlgorithm, SeatLayoutParam, SeatLayoutProposal } from "@/lib/api/types";
import { currencyDigits } from "@/lib/promo";
import {
  makeStroke,
  nextPendingKey,
  pendingTierId,
  type Brush,
  type Merged,
  type PendingTier,
  type SeatState,
  type Stroke,
} from "@/lib/seatManager/pending";

export type FieldValue = number | boolean | string | number[] | undefined;
export type FormValues = Record<string, FieldValue>;

/** Parameter units the server names (layoutgen.Unit*). */
export const UNIT_MINOR = "minor_currency";
export const UNIT_PERCENT = "percent";

/** The values a form starts with: the defaults the schema names, everything else empty. */
export function initialValues(alg: SeatLayoutAlgorithm): FormValues {
  const out: FormValues = {};
  for (const p of alg.params) {
    const d = p.default;
    if (p.type === "integer" && typeof d === "number") out[p.name] = d;
    else if (p.type === "boolean" && typeof d === "boolean") out[p.name] = d;
    else if (p.type === "enum" && typeof d === "string") out[p.name] = d;
    else if (p.type === "integer_list" && Array.isArray(d)) out[p.name] = d.filter((x): x is number => typeof x === "number");
  }
  return out;
}

/**
 * The form of a session that already sells starts from its own category prices: a `prices` list in money has no default
 * in the schema, yet the server refuses a calculation without prices. The dearest price comes first, as the list wants.
 * Only an empty `prices` parameter is filled, and only when the categories fit its length. The count of categories
 * follows it, because the algorithm wants one price per category.
 */
export function prefillFromTiers(
  alg: SeatLayoutAlgorithm,
  values: FormValues,
  tierPrices: readonly number[],
  canCreateTiers = false,
): FormValues {
  // One category and the right to create more: the layout is a split of this category's price, so the form does not pin
  // one category. `prices` stays empty, the count is the algorithm's own (3 when that is 1), `price_max` is the price.
  if (canCreateTiers && tierPrices.length === 1) {
    const next: FormValues = { ...values };
    const count = alg.params.find((x) => x.name === "categories" && x.type === "integer");
    if (count !== undefined) {
      const cur = next[count.name];
      if (typeof cur !== "number" || cur <= 1) {
        let n = 3;
        if (typeof count.max === "number") n = Math.min(n, count.max);
        if (typeof count.min === "number") n = Math.max(n, count.min);
        next[count.name] = n;
      }
    }
    const max = alg.params.find((x) => x.name === "price_max" && x.type === "integer" && x.unit === UNIT_MINOR);
    const price = tierPrices[0]!;
    if (max !== undefined && next[max.name] === undefined && price > 0 && (typeof max.max !== "number" || price <= max.max)) {
      next[max.name] = price;
    }
    return next;
  }
  // The price range of the date (dearest and cheapest category) is the default of the two range fields, so a person who
  // clears the list and asks for more categories gets a proposal without typing the range. The server ignores the range
  // while a price list is given.
  const withRange = (v: FormValues): FormValues => {
    const valid = tierPrices.filter((n) => Number.isInteger(n) && n >= 0);
    if (valid.length === 0) return v;
    const out: FormValues = { ...v };
    const fill = (name: string, price: number) => {
      const f = alg.params.find((x) => x.name === name && x.type === "integer" && x.unit === UNIT_MINOR);
      if (f === undefined || out[f.name] !== undefined) return;
      if ((typeof f.min === "number" && price < f.min) || (typeof f.max === "number" && price > f.max)) return;
      out[f.name] = price;
    };
    fill("price_max", Math.max(...valid));
    fill("price_min", Math.min(...valid));
    return out;
  };
  const p = alg.params.find((x) => x.name === "prices" && x.type === "integer_list" && x.unit === UNIT_MINOR);
  if (p === undefined || values[p.name] !== undefined) return withRange(values);
  const list = [...tierPrices].filter((n) => Number.isInteger(n) && n >= 0).sort((a, b) => b - a);
  const min = p.min_items ?? 1;
  const max = p.max_items ?? Number.POSITIVE_INFINITY;
  if (list.length < min || list.length > max) return withRange(values);
  if (list.some((n) => (typeof p.min === "number" && n < p.min) || (typeof p.max === "number" && n > p.max))) return withRange(values);
  const next: FormValues = { ...withRange(values), [p.name]: list };
  // One price per category: the count of categories follows the list, or the server refuses the pair.
  const count = alg.params.find((x) => x.name === "categories" && x.type === "integer");
  if (count !== undefined && (typeof count.min !== "number" || list.length >= count.min) && (typeof count.max !== "number" || list.length <= count.max)) {
    next[count.name] = list.length;
  }
  return next;
}

/** An integer with a short range is a slider; money and wide ranges are typed. */
export function sliderBounds(p: SeatLayoutParam): { min: number; max: number } | null {
  if (p.type !== "integer" || p.unit === UNIT_MINOR) return null;
  if (typeof p.min !== "number" || typeof p.max !== "number") return null;
  if (p.max - p.min > 100 || p.max <= p.min) return null;
  return { min: p.min, max: p.max };
}

export type RequestParams = Record<string, number | boolean | string | number[]>;

export type BuildResult = { ok: true; params: RequestParams } | { ok: false; param: string; reason: string };

/**
 * The `params` of a suggest request: only what the person set or the schema
 * defaults. An empty field is left out (the server fills defaults and checks
 * the rules that need several parameters at once).
 */
export function buildParams(alg: SeatLayoutAlgorithm, values: FormValues): BuildResult {
  const out: RequestParams = {};
  for (const p of alg.params) {
    const v = values[p.name];
    if (v === undefined) {
      if (p.required && p.default === null) return { ok: false, param: p.name, reason: "required" };
      continue;
    }
    if (Array.isArray(v)) {
      if (v.length === 0) continue;
      if (p.min_items !== undefined && p.min_items > 0 && v.length < p.min_items) return { ok: false, param: p.name, reason: "items" };
      if (p.max_items !== undefined && p.max_items > 0 && v.length > p.max_items) return { ok: false, param: p.name, reason: "items" };
      out[p.name] = v;
      continue;
    }
    if (typeof v === "number") {
      if (!Number.isFinite(v) || !Number.isInteger(v)) return { ok: false, param: p.name, reason: "integer" };
      if (typeof p.min === "number" && v < p.min) return { ok: false, param: p.name, reason: "min" };
      if (typeof p.max === "number" && v > p.max) return { ok: false, param: p.name, reason: "max" };
    }
    out[p.name] = v;
  }
  return { ok: true, params: out };
}

/** Minor units of a typed amount in major units ("12", "12.5", "12,50"). Zero is allowed. */
export function parseMajor(raw: string, currency: string): number | null {
  const digits = currencyDigits(currency);
  const s = raw.replace(/[\s  ]/g, "").replace(",", ".");
  if (s === "" || (s.match(/\./g)?.length ?? 0) > 1) return null;
  const [whole = "", frac = ""] = s.split(".");
  if (whole === "" && frac === "") return null;
  if (!/^\d*$/.test(whole) || !/^\d*$/.test(frac) || whole.length > 9 || frac.length > digits) return null;
  const w = whole === "" ? 0 : Number(whole);
  return w * 10 ** digits + (frac === "" ? 0 : Number(frac.padEnd(digits, "0")));
}

/** Minor units as the number a person types back ("12" or "12.5"). */
export function majorText(minor: number, currency: string): string {
  const digits = currencyDigits(currency);
  const major = minor / 10 ** digits;
  return digits === 0 ? String(major) : major.toFixed(digits).replace(/\.?0+$/, "");
}

/** Whole numbers separated by spaces, commas or semicolons ("10 20 30"). Null when any part is not a whole number. */
export function parseIntList(raw: string): number[] | null {
  const parts = raw.split(/[\s,;]+/).filter((x) => x !== "");
  const out: number[] = [];
  for (const part of parts) {
    if (!/^\d{1,9}$/.test(part)) return null;
    out.push(Number(part));
  }
  return out;
}

/** A money list ("50 40 30") as minor units. */
export function parseMoneyList(raw: string, currency: string): number[] | null {
  const parts = raw.split(/[\s;]+/).filter((x) => x !== "");
  const out: number[] = [];
  for (const part of parts) {
    const v = parseMajor(part, currency);
    if (v === null) return null;
    out.push(v);
  }
  return out;
}

// ─── labels ──────────────────────────────────────────────────────────────

/** "layoutgen.param.price_max" -> "Price max": the fallback for a key the catalog does not know yet. */
export function humanizeKey(key: string): string {
  const last = key.split(".").filter((x) => x !== "hint" && x !== "description").pop() ?? key;
  const words = last.replace(/_/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

// ─── proposal -> tray ────────────────────────────────────────────────────

/** A proposed slot with the category it will land in, picked by the organizer. */
export type SlotMapping = ReadonlyMap<string, string | null>;

export interface TierForMapping {
  readonly id: string;
  /** The price now, in minor units. */
  readonly price: number;
  readonly name: string;
}

/**
 * Suggests which existing category takes each slot: the slot with the same
 * price first, then the dearest slot to the dearest category, and so on. A
 * category is used once. A slot with nothing left stays unmapped (null);
 * mappingWithNew() turns it into "New category".
 */
export function suggestMapping(slots: SeatLayoutProposal["slots"], tiers: readonly TierForMapping[]): Map<string, string | null> {
  const out = new Map<string, string | null>();
  const free = new Set(tiers.map((t) => t.id));
  const byPrice = new Map<number, TierForMapping[]>();
  for (const t of tiers) byPrice.set(t.price, [...(byPrice.get(t.price) ?? []), t]);
  for (const s of slots) {
    const same = (byPrice.get(s.price_amount) ?? []).find((t) => free.has(t.id));
    if (same !== undefined) {
      out.set(s.key, same.id);
      free.delete(same.id);
    } else out.set(s.key, null);
  }
  const rest = tiers
    .filter((t) => free.has(t.id))
    .sort((a, b) => b.price - a.price || a.name.localeCompare(b.name));
  const open = [...slots].filter((s) => out.get(s.key) === null).sort((a, b) => b.price_amount - a.price_amount);
  for (const s of open) {
    const next = rest.shift();
    if (next === undefined) break;
    out.set(s.key, next.id);
  }
  return out;
}

/** The mapping value of a slot that becomes a NEW category (made in the tray, created by "Apply"). */
export const NEW_CATEGORY = "@new";

/**
 * The first mapping of a proposal: what suggestMapping finds, and "New category" for every slot it leaves without
 * one. So a layout with more slots than the date has categories needs no extra step.
 */
export function mappingWithNew(slots: SeatLayoutProposal["slots"], tiers: readonly TierForMapping[]): Map<string, string | null> {
  const out = suggestMapping(slots, tiers);
  for (const [slot, tier] of out) if (tier === null) out.set(slot, NEW_CATEGORY);
  return out;
}

// ─── a new category: the fields, and the rules the server applies ────────

/** The server's limits (seatlayout / SeatBatchNewTier). */
export const MAX_TIERS = 15;
export const MAX_TIER_NAME = 100;
export const MAX_TIER_PRICE = 1_000_000_000;

/** What a person types for a new category. The price is in major units, as in every money field. */
export interface NewTierDraft {
  readonly name: string;
  readonly price: string;
  readonly color: string;
}

export interface NewTierValue {
  readonly name: string;
  /** Minor units. */
  readonly price: number;
  /** Lower case `#rrggbb`. */
  readonly color: string;
}

export interface DraftErrors {
  name?: "empty" | "long" | "taken";
  price?: "bad" | "max";
  color?: "bad";
}

export type DraftCheck = { readonly ok: true; readonly value: NewTierValue } | { readonly ok: false; readonly errors: DraftErrors };

const HEX = /^#[0-9a-fA-F]{6}$/;

/** The key two names are compared by: trimmed, case ignored (the server does the same). */
export function nameKey(name: string): string {
  return name.trim().toLowerCase();
}

/**
 * The draft the way the server will judge it: a name of 1 to 100 characters that no live category and no other new
 * category has (case ignored), a price of 0 to 1 000 000 000 minor units, a colour `#rrggbb`.
 */
export function checkDraft(draft: NewTierDraft, currency: string, taken: ReadonlySet<string>): DraftCheck {
  const errors: DraftErrors = {};
  const name = draft.name.trim();
  if (name === "") errors.name = "empty";
  else if ([...name].length > MAX_TIER_NAME) errors.name = "long";
  else if (taken.has(nameKey(name))) errors.name = "taken";
  const price = parseMajor(draft.price, currency);
  if (price === null) errors.price = "bad";
  else if (price > MAX_TIER_PRICE) errors.price = "max";
  if (!HEX.test(draft.color)) errors.color = "bad";
  if (errors.name !== undefined || errors.price !== undefined || errors.color !== undefined) return { ok: false, errors };
  return { ok: true, value: { name, price: price!, color: draft.color.toLowerCase() } };
}

/**
 * Checks the drafts of all new slots together: a name must also differ from the drafts before it. The first of two
 * equal names is fine, the second is "taken".
 */
export function checkDrafts(
  drafts: ReadonlyMap<string, NewTierDraft>,
  currency: string,
  takenNames: readonly string[],
): Map<string, DraftCheck> {
  const taken = new Set(takenNames.map(nameKey));
  const out = new Map<string, DraftCheck>();
  for (const [slot, d] of drafts) {
    const res = checkDraft(d, currency, taken);
    out.set(slot, res);
    // Even an otherwise bad draft keeps its name: the next one must not repeat it.
    if (d.name.trim() !== "") taken.add(nameKey(d.name));
  }
  return out;
}

/** True when `adding` more categories would break the limit of 15 per date. */
export function overTierLimit(live: number, pending: number, adding: number): boolean {
  return live + pending + adding > MAX_TIERS;
}

/** What a slot proposes, as the form's starting values. */
export function draftOfSlot(slot: SeatLayoutProposal["slots"][number], currency: string, fallbackName: string, fallbackColor: string): NewTierDraft {
  return {
    name: slot.name !== undefined && slot.name.trim() !== "" ? slot.name : fallbackName,
    price: majorText(slot.price_amount, currency),
    color: slot.color !== undefined && HEX.test(slot.color) ? slot.color.toLowerCase() : fallbackColor,
  };
}

/**
 * The mapping with every "new category" slot turned into a category of the tray: its key, and a mapping value that is
 * that category's id. A new slot with no seat to move makes no category (it would be refused as unused).
 */
export function resolveNewSlots(
  proposal: SeatLayoutProposal,
  mapping: SlotMapping,
  values: ReadonlyMap<string, NewTierValue>,
  existing: readonly PendingTier[],
): { mapping: Map<string, string | null>; created: PendingTier[] } {
  const seatsOf = new Map<string, number>();
  for (const slot of Object.values(proposal.assignments)) seatsOf.set(slot, (seatsOf.get(slot) ?? 0) + 1);
  const out = new Map<string, string | null>();
  const created: PendingTier[] = [];
  for (const slot of proposal.slots) {
    const m = mapping.get(slot.key) ?? null;
    if (m !== NEW_CATEGORY) {
      out.set(slot.key, m);
      continue;
    }
    const v = values.get(slot.key);
    if (v === undefined || (seatsOf.get(slot.key) ?? 0) === 0) {
      out.set(slot.key, null);
      continue;
    }
    const key = nextPendingKey([...existing, ...created]);
    created.push({ key, name: v.name, price: v.price, color: v.color });
    out.set(slot.key, pendingTierId(key));
  }
  return { mapping: out, created };
}

// ─── the preview table ───────────────────────────────────────────────────

export interface PreviewRow {
  readonly slotKey: string;
  readonly name: string;
  readonly seats: number;
  readonly price: number;
  /** Open seats at the price, as the server counts them (`revenue_minor`). */
  readonly revenue: number;
}

export interface PreviewTable {
  readonly rows: PreviewRow[];
  readonly totalSeats: number;
  readonly totalRevenue: number;
}

/** "Category, seats, price, at a full house" from the slots and `metrics.by_slot`, with the totals. */
export function previewTable(
  slots: SeatLayoutProposal["slots"],
  bySlot: readonly { slot_key: string; seats: number; price_amount: number; revenue_minor: number }[],
  slotName: (slot: SeatLayoutProposal["slots"][number], index: number) => string,
): PreviewTable {
  const rows: PreviewRow[] = slots.map((s, i) => {
    const m = bySlot.find((x) => x.slot_key === s.key);
    return { slotKey: s.key, name: slotName(s, i), seats: m?.seats ?? 0, price: m?.price_amount ?? s.price_amount, revenue: m?.revenue_minor ?? 0 };
  });
  return {
    rows,
    totalSeats: rows.reduce((n, r) => n + r.seats, 0),
    totalRevenue: rows.reduce((n, r) => n + r.revenue, 0),
  };
}

/** True when every slot has a category: nothing is left out of the transfer. */
export function mappingComplete(slots: SeatLayoutProposal["slots"], mapping: SlotMapping): boolean {
  return slots.every((s) => (mapping.get(s.key) ?? null) !== null);
}

export interface TransferResult {
  /** One stroke per category, or none when the proposal changes nothing. */
  readonly strokes: Stroke[];
  readonly changed: number;
  /** Seats of an unmapped slot: left where they are. */
  readonly leftOut: number;
}

/**
 * The proposal as strokes of the tray: every seat the proposal assigns, whose
 * category differs from the one it has now, is painted with the category of its
 * slot. A sold or held seat is never in the proposal and never touched.
 */
export function proposalToStrokes(
  proposal: SeatLayoutProposal,
  mapping: SlotMapping,
  base: ReadonlyMap<string, SeatState>,
  merged: Merged,
  firstId: number,
): TransferResult {
  const bySlot = new Map<string, string[]>();
  for (const [seat, slot] of Object.entries(proposal.assignments)) {
    const list = bySlot.get(slot) ?? [];
    list.push(seat);
    bySlot.set(slot, list);
  }
  const strokes: Stroke[] = [];
  let changed = 0;
  let leftOut = 0;
  let id = firstId;
  for (const slot of proposal.slots) {
    const seats = bySlot.get(slot.key) ?? [];
    const tierId = mapping.get(slot.key) ?? null;
    if (tierId === null) {
      leftOut += seats.length;
      continue;
    }
    const brush: Brush = { kind: "tier", tierId };
    const res = makeStroke(id, brush, seats, base, merged);
    if (res.stroke !== null) {
      strokes.push(res.stroke);
      changed += res.changed;
      id++;
    }
  }
  return { strokes, changed, leftOut };
}
