/**
 * The "Hall plan" of the event wizard: a date at an existing venue that has a
 * seating plan is sold by the plan (spec 40 §5.S, S3a; the bot's
 * wizard_seated.go is the twin).
 *
 * The person picks one of the venue's plans (its current version) or "without
 * a plan" (free seating, the old path). A plan brings its categories, each
 * with its places; the person prices every one of them. A plan drawn with
 * fewer categories than the person sells (one category: every seat one
 * colour) gets EXTRA price categories — a name, a price and a seat-map
 * colour — that start with no places: the seats are painted into them on the
 * seat map after the save. The bundle sends the plan version
 * (`actionEvent.arenaSeatingPlanVersionId`), one entry per plan category
 * (`planCategoryIndex`) and the extras as `planExtra`.
 *
 * Pure: no React, no fetch. The plans are read by the section component.
 */
import type { SeatingPlan, SeatingPlanVersion } from "@/lib/api/types";
import { majorOf, parsePriceMinor } from "@/lib/wizard/parse";

/** The most categories a seated date may have: the seat layouts cut a hall into at most fifteen. */
export const MAX_SEAT_CATEGORIES = 15;

/** The colours an extra category can take, the bot's colour buttons (eventbot seatColors). */
export const SEAT_COLORS: readonly string[] = [
  "#dc2626",
  "#ea580c",
  "#ca8a04",
  "#16a34a",
  "#2563eb",
  "#7c3aed",
  "#92400e",
  "#374151",
];

/** One category of a plan that owns places, priced by the person. */
export interface PlanCategoryDraft {
  /** The 1-based category index of the plan geometry. */
  index: number;
  name: string;
  places: number;
  /** A standing zone of the plan: its places are the zone's capacity. */
  ga: boolean;
  /** Typed as is; parsed by parsePriceMinor. */
  price: string;
}

/** A price category added over the plan's own: no places until seats are painted into it. */
export interface ExtraCategoryDraft {
  key: string;
  name: string;
  price: string;
  /** #rrggbb. */
  color: string;
}

export interface SeatingSection {
  /** The plan version the date is sold by; "" = free seating (no plan). */
  versionId: string;
  planName: string;
  /** Every place of the plan: its seats and its standing capacity. */
  seats: number;
  categories: PlanCategoryDraft[];
  extras: ExtraCategoryDraft[];
}

export function emptySeating(): SeatingSection {
  return { versionId: "", planName: "", seats: 0, categories: [], extras: [] };
}

export function isSeated(s: SeatingSection): boolean {
  return s.versionId !== "";
}

/** A plan of the venue as the wizard offers it: its current version and its categories with places. */
export interface PlanChoice {
  versionId: string;
  name: string;
  seats: number;
  categories: { index: number; name: string; places: number; ga: boolean }[];
}

interface GeometryLike {
  categories?: { index?: number; name?: string; kind?: string; capacity?: number }[];
  sections?: { rows?: { seats?: { category_index?: number }[] }[] }[];
}

/**
 * The choice a plan offers, read from its current version's geometry: seated
 * categories count their seats, a standing zone its capacity; categories
 * without places are left out. The plan's display names win over the
 * geometry's, as at the import. Null for a plan that is not offered: not the
 * organization's own, not active, without a current version, or without a
 * single category that has places.
 */
export function planChoiceOf(plan: SeatingPlan, versions: readonly SeatingPlanVersion[], orgId: string): PlanChoice | null {
  if (plan.owner_org_id !== orgId || plan.status !== "active" || !plan.current_version_id) return null;
  const v = versions.find((x) => x.id === plan.current_version_id);
  if (v === undefined) return null;
  const g = (v.geometry ?? {}) as GeometryLike;
  const seats = new Map<number, number>();
  for (const sec of g.sections ?? []) {
    for (const row of sec.rows ?? []) {
      for (const s of row.seats ?? []) {
        const i = s.category_index ?? 0;
        seats.set(i, (seats.get(i) ?? 0) + 1);
      }
    }
  }
  const overrides = (plan.category_name_overrides ?? {}) as Record<string, string>;
  const out: PlanChoice = { versionId: v.id, name: plan.name, seats: 0, categories: [] };
  for (const c of g.categories ?? []) {
    const index = c.index ?? 0;
    const ga = c.kind === "general_admission";
    const places = ga ? (c.capacity ?? 0) : (seats.get(index) ?? 0);
    if (places <= 0) continue;
    const override = overrides[String(index)];
    const name = typeof override === "string" && override.trim() !== "" ? override : (c.name ?? "");
    out.categories.push({ index, name, places, ga });
    out.seats += places;
  }
  return out.categories.length > 0 ? out : null;
}

export type SeatingAction =
  | { type: "seatingPlan"; plan: PlanChoice | null }
  | { type: "seatingPrice"; index: number; value: string }
  | { type: "seatingExtraAdd"; key: string }
  | { type: "seatingExtra"; key: string; field: "name" | "price" | "color"; value: string }
  | { type: "seatingExtraRemove"; key: string };

export function isSeatingAction(a: { type: string }): a is SeatingAction {
  return a.type.startsWith("seating");
}

export function applySeatingAction(s: SeatingSection, a: SeatingAction): SeatingSection {
  switch (a.type) {
    case "seatingPlan": {
      if (a.plan === null) return isSeated(s) ? emptySeating() : s;
      if (a.plan.versionId === s.versionId) return s;
      return {
        versionId: a.plan.versionId,
        planName: a.plan.name,
        seats: a.plan.seats,
        categories: a.plan.categories.map((c) => ({ ...c, price: "" })),
        // Extra categories belong to the person, not to the plan: another plan keeps them.
        extras: s.extras,
      };
    }
    case "seatingPrice":
      return { ...s, categories: s.categories.map((c) => (c.index === a.index ? { ...c, price: a.value } : c)) };
    case "seatingExtraAdd": {
      if (s.categories.length + s.extras.length >= MAX_SEAT_CATEGORIES) return s;
      const used = new Set(s.extras.map((e) => e.color));
      const color = SEAT_COLORS.find((c) => !used.has(c)) ?? SEAT_COLORS[0];
      return { ...s, extras: [...s.extras, { key: a.key, name: "", price: "", color }] };
    }
    case "seatingExtra":
      return { ...s, extras: s.extras.map((e) => (e.key === a.key ? { ...e, [a.field]: a.value } : e)) };
    case "seatingExtraRemove":
      return { ...s, extras: s.extras.filter((e) => e.key !== a.key) };
  }
}

/** Field keys of the section: `seating.cat.<index>.price`, `seating.extra.<key>.<field>`. */
export function seatCatKey(index: number): string {
  return `seating.cat.${index}.price`;
}
export function seatExtraKey(key: string, field: "name" | "price" | "color"): string {
  return `seating.extra.${key}.${field}`;
}

export type SeatingErrorKey =
  | "wizard.err.priceRequired"
  | "wizard.err.priceShape"
  | "wizard.err.catName"
  | "wizard.err.catDup"
  | "wizard.seating.err.color";

/** The checks of the section the wizard makes itself; empty when the date is not seated. */
export function seatingClientErrors(s: SeatingSection): Map<string, SeatingErrorKey> {
  const out = new Map<string, SeatingErrorKey>();
  if (!isSeated(s)) return out;
  const price = (raw: string, key: string) => {
    if (raw.trim() === "") out.set(key, "wizard.err.priceRequired");
    else if (parsePriceMinor(raw) === null) out.set(key, "wizard.err.priceShape");
  };
  for (const c of s.categories) price(c.price, seatCatKey(c.index));
  const names = new Map<string, number>();
  for (const c of s.categories) names.set(c.name.trim().toLowerCase(), (names.get(c.name.trim().toLowerCase()) ?? 0) + 1);
  for (const e of s.extras) {
    const n = e.name.trim().toLowerCase();
    if (n !== "") names.set(n, (names.get(n) ?? 0) + 1);
  }
  for (const e of s.extras) {
    const n = e.name.trim().toLowerCase();
    if (n === "") out.set(seatExtraKey(e.key, "name"), "wizard.err.catName");
    else if ((names.get(n) ?? 0) > 1) out.set(seatExtraKey(e.key, "name"), "wizard.err.catDup");
    price(e.price, seatExtraKey(e.key, "price"));
    if (!/^#[0-9a-f]{6}$/i.test(e.color)) out.set(seatExtraKey(e.key, "color"), "wizard.seating.err.color");
  }
  return out;
}

/** The categoryList of a seated date: the plan's categories, then the extra ones. */
export interface SeatedBundleCategory {
  categoryPriceName: string;
  price: number;
  availability: number;
  nextCategoryIndex: number;
  priceSchedule: [];
  placement: boolean;
  planCategoryIndex?: number;
  planExtra?: boolean;
  color?: string;
}

export function seatedCategoryList(s: SeatingSection): SeatedBundleCategory[] {
  const major = (raw: string) => majorOf(parsePriceMinor(raw) ?? 0);
  const out: SeatedBundleCategory[] = s.categories.map((c) => ({
    categoryPriceName: c.name.trim(),
    price: major(c.price),
    // A standing zone keeps the plan's capacity; a seated category's places are the plan's seats.
    availability: c.ga ? c.places : 0,
    nextCategoryIndex: -1,
    priceSchedule: [],
    placement: !c.ga,
    planCategoryIndex: c.index,
  }));
  for (const e of s.extras) {
    out.push({
      categoryPriceName: e.name.trim(),
      price: major(e.price),
      availability: 0,
      nextCategoryIndex: -1,
      priceSchedule: [],
      placement: true,
      planExtra: true,
      color: e.color.toLowerCase(),
    });
  }
  return out;
}

/** Reads a stored section; an absent or malformed one is free seating (never half read). */
export function parseSeating(raw: unknown): SeatingSection {
  if (raw === null || typeof raw !== "object") return emptySeating();
  const o = raw as Record<string, unknown>;
  const str = (v: unknown) => (typeof v === "string" ? v : null);
  const versionId = str(o.versionId);
  const planName = str(o.planName);
  if (versionId === null || planName === null || typeof o.seats !== "number" || !Array.isArray(o.categories) || !Array.isArray(o.extras)) {
    return emptySeating();
  }
  const categories: PlanCategoryDraft[] = [];
  for (const c of o.categories as unknown[]) {
    const r = c as Record<string, unknown>;
    if (typeof r?.index !== "number" || typeof r.places !== "number" || typeof r.ga !== "boolean" || str(r.name) === null || str(r.price) === null) {
      return emptySeating();
    }
    categories.push({ index: r.index, name: r.name as string, places: r.places, ga: r.ga, price: r.price as string });
  }
  const extras: ExtraCategoryDraft[] = [];
  for (const e of o.extras as unknown[]) {
    const r = e as Record<string, unknown>;
    const key = str(r?.key);
    const name = str(r?.name);
    const price = str(r?.price);
    const color = str(r?.color);
    if (key === null || name === null || price === null || color === null) return emptySeating();
    extras.push({ key, name, price, color });
  }
  return { versionId, planName, seats: o.seats, categories, extras };
}
