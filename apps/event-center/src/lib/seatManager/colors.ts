/**
 * Category colours of the seat manager.
 *
 * ONE function decides a category's colour: tierColors(). Today it is a fixed
 * palette handed out by the category's order, so a hall looks the same every
 * time it is opened. When ticket_tiers.color arrives (task S1d) the category's
 * own colour wins and this is the only place that changes.
 */

/** Fifteen colours (the most categories a layout may have), readable on white and apart from each other. */
export const TIER_PALETTE: readonly string[] = [
  "#4f46e5",
  "#db2777",
  "#059669",
  "#d97706",
  "#0891b2",
  "#7c3aed",
  "#dc2626",
  "#65a30d",
  "#0d9488",
  "#c026d3",
  "#2563eb",
  "#ea580c",
  "#4d7c0f",
  "#be123c",
  "#0369a1",
];

export interface TierColorInput {
  readonly id: string;
  readonly name: string;
  readonly sort_order: number;
  /** ticket_tiers.color, once the API sends it. */
  readonly color?: string | null;
}

const HEX = /^#[0-9a-f]{6}$/i;

export function isHexColor(value: string | null | undefined): value is string {
  return typeof value === "string" && HEX.test(value);
}

/** The tiers in the order the palette is handed out. */
export function orderTiers<T extends TierColorInput>(tiers: readonly T[]): T[] {
  return [...tiers].sort(
    (a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
  );
}

/** Category id to colour. */
export function tierColors(tiers: readonly TierColorInput[]): Map<string, string> {
  const out = new Map<string, string>();
  orderTiers(tiers).forEach((t, i) => {
    out.set(t.id, isHexColor(t.color) ? t.color.toLowerCase() : TIER_PALETTE[i % TIER_PALETTE.length]!);
  });
  return out;
}

/** The colour of a layout slot's number `i` (a proposal has no category yet). */
export function slotColor(i: number): string {
  return TIER_PALETTE[i % TIER_PALETTE.length]!;
}

/** Black or white text, whichever reads better on the colour. */
export function readableOn(hex: string): "#111827" | "#ffffff" {
  if (!isHexColor(hex)) return "#111827";
  const n = parseInt(hex.slice(1), 16);
  const r = (n >> 16) & 255;
  const g = (n >> 8) & 255;
  const b = n & 255;
  const lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  return lum > 0.6 ? "#111827" : "#ffffff";
}

/** Colours of a seat that is not for sale, whatever its category. */
export const STATUS_COLORS = {
  sold: "#374151",
  held: "#f59e0b",
  closed: "#e5e7eb",
  unknown: "#d1d5db",
} as const;
