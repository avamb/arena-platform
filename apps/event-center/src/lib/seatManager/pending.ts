/**
 * The tray of the seat manager (spec 26 decision 1): every touch on the map
 * adds a stroke to the tray; nothing reaches the server until the organizer
 * presses "Apply", and then the whole tray goes as ONE atomic batch
 * (POST .../seat-batches). A buyer's widget therefore never sees a half
 * painted hall, and undoing before the apply costs nothing.
 *
 * Pure: no DOM, no API. The rules here mirror seatlayout.ApplyBatch on the
 * server (a sold or held seat is never changed), but the server stays the
 * judge: a seat that sold while the tray was open comes back as "skipped".
 */

export type SeatStatus = "available" | "held" | "sold" | "unavailable";

/** What the server says a seat is right now (GET .../seats/admin). */
export interface SeatState {
  readonly tierId: string | null;
  readonly status: SeatStatus;
}

/** What a stroke wants a seat to become. */
export interface Target {
  readonly tierId?: string;
  readonly status?: "available" | "unavailable";
}

/** The most seats one batch may change (seatlayout.MaxChanges). */
export const MAX_BATCH_CHANGES = 10_000;

export function isFrozen(state: SeatState | undefined): boolean {
  return state !== undefined && (state.status === "sold" || state.status === "held");
}

/** A brush: paint with a category, or close / open seats for sale. */
export type Brush =
  | { readonly kind: "tier"; readonly tierId: string }
  | { readonly kind: "close" }
  | { readonly kind: "open" };

export interface Stroke {
  readonly id: number;
  readonly brush: Brush;
  /** What this stroke changes, per seat. */
  readonly targets: ReadonlyMap<string, Target>;
}

export interface StrokeResult {
  /** Null when the stroke changed nothing. */
  readonly stroke: Stroke | null;
  readonly changed: number;
  /** Sold or in a hold: never changed. */
  readonly skippedFrozen: number;
  /** Already what the brush would make it (or not a seat of this session). */
  readonly unchanged: number;
}

export interface Tray {
  readonly strokes: readonly Stroke[];
  readonly nextId: number;
}

export const EMPTY_TRAY: Tray = { strokes: [], nextId: 1 };

export type Merged = ReadonlyMap<string, Target>;

/** Strokes folded in order: a later stroke wins, field by field. */
export function mergeStrokes(strokes: readonly Stroke[]): Map<string, Target> {
  const out = new Map<string, Target>();
  for (const s of strokes) {
    for (const [key, t] of s.targets) {
      const prev = out.get(key);
      out.set(key, prev === undefined ? t : { ...prev, ...t });
    }
  }
  return out;
}

/** A seat as the organizer sees it: the server's state with the tray on top. */
export function effectiveState(
  base: ReadonlyMap<string, SeatState>,
  merged: Merged,
  key: string,
): SeatState | undefined {
  const b = base.get(key);
  if (b === undefined) return undefined;
  if (isFrozen(b)) return b;
  const t = merged.get(key);
  if (t === undefined) return b;
  return { tierId: t.tierId ?? b.tierId, status: t.status ?? b.status };
}

function targetFor(brush: Brush): Target {
  switch (brush.kind) {
    case "tier":
      return { tierId: brush.tierId };
    case "close":
      return { status: "unavailable" };
    case "open":
      return { status: "available" };
  }
}

/**
 * The stroke a brush makes over some seats. A seat that is sold, held, not
 * known, or already what the brush would make it is left out and counted.
 */
export function makeStroke(
  id: number,
  brush: Brush,
  keys: Iterable<string>,
  base: ReadonlyMap<string, SeatState>,
  merged: Merged,
): StrokeResult {
  const target = targetFor(brush);
  const targets = new Map<string, Target>();
  let skippedFrozen = 0;
  let unchanged = 0;
  const seen = new Set<string>();
  for (const key of keys) {
    if (seen.has(key)) continue;
    seen.add(key);
    const b = base.get(key);
    if (b === undefined) {
      unchanged++;
      continue;
    }
    if (isFrozen(b)) {
      skippedFrozen++;
      continue;
    }
    const now = effectiveState(base, merged, key);
    const same =
      (target.tierId === undefined || target.tierId === now?.tierId) &&
      (target.status === undefined || target.status === now?.status);
    if (same) {
      unchanged++;
      continue;
    }
    targets.set(key, target);
  }
  if (targets.size === 0) return { stroke: null, changed: 0, skippedFrozen, unchanged };
  return { stroke: { id, brush, targets }, changed: targets.size, skippedFrozen, unchanged };
}

export function pushStroke(tray: Tray, stroke: Stroke): Tray {
  return { strokes: [...tray.strokes, stroke], nextId: Math.max(tray.nextId, stroke.id + 1) };
}

export function undoStroke(tray: Tray): Tray {
  if (tray.strokes.length === 0) return tray;
  return { strokes: tray.strokes.slice(0, -1), nextId: tray.nextId };
}

export function clearTray(tray: Tray): Tray {
  return { strokes: [], nextId: tray.nextId };
}

export interface BatchChange {
  readonly seat_key: string;
  readonly tier_id?: string;
  readonly status?: "available" | "unavailable";
}

/**
 * The changes the tray really makes: a field equal to the server's state, or
 * a seat that became frozen meanwhile, is dropped. Sorted by seat key, so the
 * same tray always sends the same body.
 */
export function trayChanges(base: ReadonlyMap<string, SeatState>, merged: Merged): BatchChange[] {
  const out: BatchChange[] = [];
  for (const [key, t] of merged) {
    const b = base.get(key);
    if (b === undefined || isFrozen(b)) continue;
    const tierId = t.tierId !== undefined && t.tierId !== b.tierId ? t.tierId : undefined;
    const status = t.status !== undefined && t.status !== b.status ? t.status : undefined;
    if (tierId === undefined && status === undefined) continue;
    const change: { seat_key: string; tier_id?: string; status?: "available" | "unavailable" } = { seat_key: key };
    if (tierId !== undefined) change.tier_id = tierId;
    if (status !== undefined) change.status = status;
    out.push(change);
  }
  out.sort((a, b) => (a.seat_key < b.seat_key ? -1 : a.seat_key > b.seat_key ? 1 : 0));
  return out;
}

export type BatchKind = "paint" | "close" | "open" | "apply_layout";

/** The `kind` of the batch: the narrowest one that fits (the server checks it). */
export function batchKind(changes: readonly BatchChange[]): BatchKind {
  let paints = false;
  let closes = false;
  let opens = false;
  for (const c of changes) {
    if (c.tier_id !== undefined) paints = true;
    if (c.status === "unavailable") closes = true;
    if (c.status === "available") opens = true;
  }
  if (paints && !closes && !opens) return "paint";
  if (!paints && closes && !opens) return "close";
  if (!paints && !closes && opens) return "open";
  return "apply_layout";
}

export interface TrayTotals {
  readonly seats: number;
  readonly painted: number;
  readonly closed: number;
  readonly opened: number;
}

export function trayTotals(changes: readonly BatchChange[]): TrayTotals {
  let painted = 0;
  let closed = 0;
  let opened = 0;
  for (const c of changes) {
    if (c.tier_id !== undefined) painted++;
    if (c.status === "unavailable") closed++;
    if (c.status === "available") opened++;
  }
  return { seats: changes.length, painted, closed, opened };
}

export interface TierDelta {
  readonly tierId: string;
  /** Seats that arrive in the category / leave it, after the tray. */
  readonly gained: number;
  readonly lost: number;
}

/** The tray as the organizer reads it: which category gains and which loses seats. */
export function tierDeltas(base: ReadonlyMap<string, SeatState>, changes: readonly BatchChange[]): TierDelta[] {
  const gained = new Map<string, number>();
  const lost = new Map<string, number>();
  for (const c of changes) {
    if (c.tier_id === undefined) continue;
    gained.set(c.tier_id, (gained.get(c.tier_id) ?? 0) + 1);
    const from = base.get(c.seat_key)?.tierId;
    if (from !== null && from !== undefined) lost.set(from, (lost.get(from) ?? 0) + 1);
  }
  const ids = new Set<string>([...gained.keys(), ...lost.keys()]);
  return [...ids]
    .map((tierId) => ({ tierId, gained: gained.get(tierId) ?? 0, lost: lost.get(tierId) ?? 0 }))
    .sort((a, b) => (a.tierId < b.tierId ? -1 : 1));
}

export interface TierCounts {
  readonly total: number;
  /** Open for sale. */
  readonly free: number;
  readonly sold: number;
  readonly held: number;
  /** Closed by the organizer. */
  readonly closed: number;
}

/** Seat counts per category, with the tray applied. A seat with no category is not counted. */
export function countByTier(
  base: ReadonlyMap<string, SeatState>,
  merged: Merged,
  seatKeys: Iterable<string>,
): Map<string, TierCounts> {
  const acc = new Map<string, { total: number; free: number; sold: number; held: number; closed: number }>();
  for (const key of seatKeys) {
    const s = effectiveState(base, merged, key);
    if (s === undefined || s.tierId === null) continue;
    let c = acc.get(s.tierId);
    if (c === undefined) {
      c = { total: 0, free: 0, sold: 0, held: 0, closed: 0 };
      acc.set(s.tierId, c);
    }
    c.total++;
    if (s.status === "available") c.free++;
    else if (s.status === "sold") c.sold++;
    else if (s.status === "held") c.held++;
    else c.closed++;
  }
  return acc;
}
