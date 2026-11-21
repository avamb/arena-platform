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

/**
 * A category the organizer made in the tray and the server does not know yet. It lives in the tray (and in its
 * storage) until "Apply", when it goes in the same request as the seats painted with it.
 */
export interface PendingTier {
  /** The client's id of the category: `[A-Za-z0-9_-]{1,40}`, the `key` of `new_tiers`. */
  readonly key: string;
  readonly name: string;
  /** Minor units. */
  readonly price: number;
  /** `#rrggbb`. */
  readonly color: string;
}

/** A pending category is used as a category id with this prefix, so strokes, counters and colours need no second path. */
export const PENDING_PREFIX = "new:";
export const pendingTierId = (key: string): string => PENDING_PREFIX + key;
export const isPendingTierId = (id: string): boolean => id.startsWith(PENDING_PREFIX);

export interface Tray {
  readonly strokes: readonly Stroke[];
  readonly nextId: number;
  readonly pendingTiers: readonly PendingTier[];
}

export const EMPTY_TRAY: Tray = { strokes: [], nextId: 1, pendingTiers: [] };

/** The next free key for a pending category ("n1", "n2", ...). */
export function nextPendingKey(existing: readonly PendingTier[]): string {
  let max = 0;
  for (const p of existing) {
    const m = /^n(\d+)$/.exec(p.key);
    if (m !== null) max = Math.max(max, Number(m[1]));
  }
  return `n${max + 1}`;
}

export function addPendingTiers(tray: Tray, tiers: readonly PendingTier[]): Tray {
  if (tiers.length === 0) return tray;
  return { ...tray, pendingTiers: [...tray.pendingTiers, ...tiers] };
}

/** Removes a pending category and every stroke painted with it. */
export function removePendingTier(tray: Tray, key: string): Tray {
  const id = pendingTierId(key);
  return {
    ...tray,
    pendingTiers: tray.pendingTiers.filter((p) => p.key !== key),
    strokes: tray.strokes.filter((s) => !(s.brush.kind === "tier" && s.brush.tierId === id)),
  };
}

/** The category of the answer replaces the pending one: its strokes (made while the request was on its way) follow it. */
function renameTierInStroke(s: Stroke, from: string, to: string): Stroke {
  const brush: Brush = s.brush.kind === "tier" && s.brush.tierId === from ? { kind: "tier", tierId: to } : s.brush;
  const targets = new Map<string, Target>();
  for (const [k, t] of s.targets) targets.set(k, t.tierId === from ? { ...t, tierId: to } : t);
  return { id: s.id, brush, targets };
}

/**
 * The tray after a successful apply: the strokes the request carried are gone, and every category the server
 * created replaces its pending twin in the strokes that stay.
 */
export function settleApplied(
  tray: Tray,
  sentStrokeIds: ReadonlySet<number>,
  created: readonly { readonly key: string; readonly tier_id: string }[],
): Tray {
  let strokes = tray.strokes.filter((x) => !sentStrokeIds.has(x.id));
  for (const c of created) {
    const from = pendingTierId(c.key);
    strokes = strokes.map((s) => renameTierInStroke(s, from, c.tier_id));
  }
  const gone = new Set(created.map((c) => c.key));
  return { strokes, nextId: tray.nextId, pendingTiers: tray.pendingTiers.filter((p) => !gone.has(p.key)) };
}

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
  return { ...tray, strokes: [...tray.strokes, stroke], nextId: Math.max(tray.nextId, stroke.id + 1) };
}

export function undoStroke(tray: Tray): Tray {
  if (tray.strokes.length === 0) return tray;
  return { ...tray, strokes: tray.strokes.slice(0, -1) };
}

/** "Clear": the strokes and the categories that were not created yet. */
export function clearTray(tray: Tray): Tray {
  return { strokes: [], nextId: tray.nextId, pendingTiers: [] };
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

export interface BatchRequestChange {
  readonly seat_key: string;
  readonly tier_id?: string;
  readonly new_tier_key?: string;
  readonly status?: "available" | "unavailable";
}

export interface BatchRequestNewTier {
  readonly key: string;
  readonly name: string;
  readonly price_amount: number;
  readonly color: string;
}

export interface BatchRequestBody {
  readonly kind: BatchKind;
  readonly changes: BatchRequestChange[];
  /** Only when a change names a category made in the tray. */
  readonly new_tiers?: BatchRequestNewTier[];
}

export interface BuiltBatch {
  readonly body: BatchRequestBody;
  /** Categories of the tray that no seat is painted with: they stay in the tray and are not sent. */
  readonly unsent: readonly PendingTier[];
}

/**
 * The ONE request of "Apply" (the tray is never split: it is one atomic batch, at most MAX_BATCH_CHANGES seats).
 * A category of the tray travels in the same request as every change that names it, once: it is in `new_tiers`
 * when at least one change uses it (the server refuses a new category nobody uses), and those changes carry
 * `new_tier_key` instead of `tier_id`.
 */
export function buildBatchRequest(changes: readonly BatchChange[], pending: readonly PendingTier[]): BuiltBatch {
  const byKey = new Map(pending.map((p) => [p.key, p] as const));
  const usedKeys = new Set<string>();
  const out: BatchRequestChange[] = [];
  for (const c of changes) {
    if (c.tier_id !== undefined && isPendingTierId(c.tier_id)) {
      const key = c.tier_id.slice(PENDING_PREFIX.length);
      if (!byKey.has(key)) {
        // The tray names a category it does not hold (a damaged storage): that part of the change is dropped.
        if (c.status !== undefined) out.push({ seat_key: c.seat_key, status: c.status });
        continue;
      }
      usedKeys.add(key);
      const next: { seat_key: string; new_tier_key: string; status?: "available" | "unavailable" } = { seat_key: c.seat_key, new_tier_key: key };
      if (c.status !== undefined) next.status = c.status;
      out.push(next);
    } else {
      const next: { seat_key: string; tier_id?: string; status?: "available" | "unavailable" } = { seat_key: c.seat_key };
      if (c.tier_id !== undefined) next.tier_id = c.tier_id;
      if (c.status !== undefined) next.status = c.status;
      out.push(next);
    }
  }
  const sent = pending.filter((p) => usedKeys.has(p.key));
  const body: BatchRequestBody = {
    kind: batchKind(changes),
    changes: out,
    ...(sent.length > 0 ? { new_tiers: sent.map((p) => ({ key: p.key, name: p.name, price_amount: p.price, color: p.color })) } : {}),
  };
  return { body, unsent: pending.filter((p) => !usedKeys.has(p.key)) };
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
