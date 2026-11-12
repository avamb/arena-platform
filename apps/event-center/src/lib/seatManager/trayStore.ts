/**
 * The tray survives a reload (spec 26 section 3.4): on a phone the page is
 * easily reloaded by the system, and an organizer must not lose a half done
 * layout. It lives in the tab's sessionStorage, per session of the event, and
 * holds only seat keys and category ids, never a person's data.
 *
 * Storage can be blocked or full: every access is guarded and the screen works
 * without it.
 */
import type { Brush, PendingTier, Stroke, Target, Tray } from "@/lib/seatManager/pending";
import { EMPTY_TRAY, PENDING_PREFIX, isPendingTierId } from "@/lib/seatManager/pending";

const PREFIX = "arena.seats.tray.";

interface StoredStroke {
  id: number;
  brush: Brush;
  targets: [string, Target][];
}

export function trayKey(sessionId: string): string {
  return `${PREFIX}${sessionId}`;
}

function validBrush(b: unknown): b is Brush {
  if (typeof b !== "object" || b === null) return false;
  const k = (b as { kind?: unknown }).kind;
  if (k === "close" || k === "open") return true;
  return k === "tier" && typeof (b as { tierId?: unknown }).tierId === "string";
}

function validPending(p: unknown): p is PendingTier {
  if (typeof p !== "object" || p === null) return false;
  const o = p as { key?: unknown; name?: unknown; price?: unknown; color?: unknown };
  return (
    typeof o.key === "string" &&
    /^[A-Za-z0-9_-]{1,40}$/.test(o.key) &&
    typeof o.name === "string" &&
    typeof o.price === "number" &&
    Number.isInteger(o.price) &&
    o.price >= 0 &&
    typeof o.color === "string" &&
    /^#[0-9a-f]{6}$/i.test(o.color)
  );
}

function validTarget(t: unknown): t is Target {
  if (typeof t !== "object" || t === null) return false;
  const o = t as { tierId?: unknown; status?: unknown };
  if (o.tierId !== undefined && typeof o.tierId !== "string") return false;
  if (o.status !== undefined && o.status !== "available" && o.status !== "unavailable") return false;
  return o.tierId !== undefined || o.status !== undefined;
}

/** Parses what was stored. Anything that does not look right is an empty tray. */
export function parseTray(raw: string | null): Tray {
  if (raw === null) return EMPTY_TRAY;
  try {
    const data: unknown = JSON.parse(raw);
    if (typeof data !== "object" || data === null) return EMPTY_TRAY;
    const strokes = (data as { strokes?: unknown }).strokes;
    if (!Array.isArray(strokes)) return EMPTY_TRAY;
    // A tray stored before the categories of the tray existed has no such list.
    const rawPending = (data as { pendingTiers?: unknown }).pendingTiers;
    const pendingTiers: PendingTier[] = [];
    if (rawPending !== undefined) {
      if (!Array.isArray(rawPending)) return EMPTY_TRAY;
      for (const p of rawPending) {
        if (!validPending(p) || pendingTiers.some((x) => x.key === p.key)) return EMPTY_TRAY;
        pendingTiers.push({ key: p.key, name: p.name, price: p.price, color: p.color });
      }
    }
    const out: Stroke[] = [];
    let nextId = 1;
    for (const s of strokes as StoredStroke[]) {
      if (typeof s !== "object" || s === null || typeof s.id !== "number" || !validBrush(s.brush) || !Array.isArray(s.targets)) {
        return EMPTY_TRAY;
      }
      const targets = new Map<string, Target>();
      for (const entry of s.targets) {
        if (!Array.isArray(entry) || typeof entry[0] !== "string" || !validTarget(entry[1])) return EMPTY_TRAY;
        targets.set(entry[0], entry[1]);
      }
      out.push({ id: s.id, brush: s.brush, targets });
      nextId = Math.max(nextId, s.id + 1);
    }
    // A stroke may only name a pending category the tray holds.
    const keys = new Set(pendingTiers.map((p) => p.key));
    const names = (id: string | undefined) => id !== undefined && isPendingTierId(id) && !keys.has(id.slice(PENDING_PREFIX.length));
    for (const s of out) {
      if (s.brush.kind === "tier" && names(s.brush.tierId)) return EMPTY_TRAY;
      for (const t of s.targets.values()) if (names(t.tierId)) return EMPTY_TRAY;
    }
    return { strokes: out, nextId, pendingTiers };
  } catch {
    return EMPTY_TRAY;
  }
}

export function serializeTray(tray: Tray): string {
  const strokes: StoredStroke[] = tray.strokes.map((s) => ({ id: s.id, brush: s.brush, targets: [...s.targets] }));
  return JSON.stringify({ strokes, pendingTiers: tray.pendingTiers });
}

export function loadTray(sessionId: string): Tray {
  try {
    return parseTray(sessionStorage.getItem(trayKey(sessionId)));
  } catch {
    return EMPTY_TRAY;
  }
}

export function saveTray(sessionId: string, tray: Tray): void {
  try {
    if (tray.strokes.length === 0 && tray.pendingTiers.length === 0) sessionStorage.removeItem(trayKey(sessionId));
    else sessionStorage.setItem(trayKey(sessionId), serializeTray(tray));
  } catch {
    // Storage is blocked or full: the tray then lives until the page reloads.
  }
}
