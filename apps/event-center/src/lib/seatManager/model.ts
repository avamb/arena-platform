/**
 * The geometry of a hall, indexed for the seat manager (spec 26 section 3.4).
 *
 * Pure: no DOM, no API. The manager paints with a brush, so every way of
 * picking seats (a seat, a row, a section, a rectangle, a lasso) is a
 * function from the geometry and a point or a shape to a list of seat keys.
 */
import type { Geometry } from "@seatmap/types";

export interface Point {
  readonly x: number;
  readonly y: number;
}

export interface SeatInfo {
  readonly key: string;
  readonly number: string;
  readonly x: number;
  readonly y: number;
  readonly radius: number;
  readonly sectionKey: string;
  readonly rowKey: string;
  /** Category index of the plan, the colour of a seat nobody painted. */
  readonly categoryIndex: number;
}

export interface RowInfo {
  /** Unique across the hall: a row key can repeat in two sections. */
  readonly id: string;
  readonly key: string;
  readonly name: string;
  readonly sectionKey: string;
  readonly sectionName: string;
  readonly seatKeys: readonly string[];
  /** Where the row's handle sits: left of its first seat. */
  readonly handle: Point;
  readonly seatRadius: number;
}

export interface SectionInfo {
  readonly key: string;
  readonly name: string;
  readonly seatKeys: readonly string[];
  readonly rowIds: readonly string[];
}

export interface HallIndex {
  readonly width: number;
  readonly height: number;
  readonly seats: readonly SeatInfo[];
  readonly byKey: ReadonlyMap<string, SeatInfo>;
  readonly rows: readonly RowInfo[];
  readonly rowById: ReadonlyMap<string, RowInfo>;
  readonly sections: readonly SectionInfo[];
  readonly sectionByKey: ReadonlyMap<string, SectionInfo>;
  /** The row id of every seat. */
  readonly rowOfSeat: ReadonlyMap<string, string>;
  /** Smallest distance between two row handles; infinity with fewer than two. */
  readonly minHandleGap: number;
}

const DEFAULT_RADIUS = 8;

export function buildHallIndex(geometry: Geometry): HallIndex {
  const width = geometry.canvas.width > 0 ? geometry.canvas.width : 800;
  const height = geometry.canvas.height > 0 ? geometry.canvas.height : 600;
  const seats: SeatInfo[] = [];
  const byKey = new Map<string, SeatInfo>();
  const rows: RowInfo[] = [];
  const rowById = new Map<string, RowInfo>();
  const rowOfSeat = new Map<string, string>();
  const sections: SectionInfo[] = [];
  const sectionByKey = new Map<string, SectionInfo>();

  for (const section of geometry.sections) {
    const sectionSeatKeys: string[] = [];
    const rowIds: string[] = [];
    for (const row of section.rows) {
      const id = `${section.key}\u0000${row.key}`;
      const keys: string[] = [];
      let minX = Infinity;
      let radius = DEFAULT_RADIUS;
      let firstY = 0;
      for (const seat of row.seats) {
        const r = seat.radius > 0 ? seat.radius : DEFAULT_RADIUS;
        const info: SeatInfo = {
          key: seat.key,
          number: seat.number,
          x: seat.x,
          y: seat.y,
          radius: r,
          sectionKey: section.key,
          rowKey: row.key,
          categoryIndex: seat.category_index,
        };
        seats.push(info);
        byKey.set(seat.key, info);
        rowOfSeat.set(seat.key, id);
        keys.push(seat.key);
        sectionSeatKeys.push(seat.key);
        if (seat.x < minX) {
          minX = seat.x;
          firstY = seat.y;
        }
        radius = r;
      }
      if (keys.length === 0) continue;
      const rowInfo: RowInfo = {
        id,
        key: row.key,
        name: row.name,
        sectionKey: section.key,
        sectionName: section.name,
        seatKeys: keys,
        // Left of the first seat by about two radii, on the row's own height.
        handle: { x: minX - radius * 2.2, y: firstY },
        seatRadius: radius,
      };
      rows.push(rowInfo);
      rowById.set(id, rowInfo);
      rowIds.push(id);
    }
    if (sectionSeatKeys.length > 0) {
      const info: SectionInfo = { key: section.key, name: section.name, seatKeys: sectionSeatKeys, rowIds };
      sections.push(info);
      sectionByKey.set(section.key, info);
    }
  }

  return {
    width,
    height,
    seats,
    byKey,
    rows,
    rowById,
    sections,
    sectionByKey,
    rowOfSeat,
    minHandleGap: nearestHandleGap(rows),
  };
}

function nearestHandleGap(rows: readonly RowInfo[]): number {
  let best = Infinity;
  for (let i = 0; i < rows.length; i++) {
    const a = rows[i]!.handle;
    for (let j = i + 1; j < rows.length; j++) {
      const b = rows[j]!.handle;
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      if (d < best) best = d;
    }
  }
  return best;
}

// ─── picking ─────────────────────────────────────────────────────────────

/** The seat under a point: the nearest one within `slack` canvas units of its edge. */
export function seatAt(index: HallIndex, p: Point, slack = 0): SeatInfo | null {
  let best: SeatInfo | null = null;
  let bestD = Infinity;
  for (const s of index.seats) {
    const d = Math.hypot(s.x - p.x, s.y - p.y);
    if (d <= s.radius + slack && d < bestD) {
      best = s;
      bestD = d;
    }
  }
  return best;
}

export interface Rect {
  readonly x0: number;
  readonly y0: number;
  readonly x1: number;
  readonly y1: number;
}

export function normalizeRect(a: Point, b: Point): Rect {
  return { x0: Math.min(a.x, b.x), y0: Math.min(a.y, b.y), x1: Math.max(a.x, b.x), y1: Math.max(a.y, b.y) };
}

/** Seats whose centre lies inside the rectangle. */
export function seatsInRect(index: HallIndex, r: Rect): string[] {
  const out: string[] = [];
  for (const s of index.seats) {
    if (s.x >= r.x0 && s.x <= r.x1 && s.y >= r.y0 && s.y <= r.y1) out.push(s.key);
  }
  return out;
}

/** Even-odd point-in-polygon test. */
export function pointInPolygon(p: Point, poly: readonly Point[]): boolean {
  let inside = false;
  for (let i = 0, j = poly.length - 1; i < poly.length; j = i++) {
    const a = poly[i]!;
    const b = poly[j]!;
    const crosses = a.y > p.y !== b.y > p.y && p.x < ((b.x - a.x) * (p.y - a.y)) / (b.y - a.y) + a.x;
    if (crosses) inside = !inside;
  }
  return inside;
}

/** Seats whose centre lies inside the lasso. A lasso with fewer than three points picks nothing. */
export function seatsInPolygon(index: HallIndex, poly: readonly Point[]): string[] {
  if (poly.length < 3) return [];
  let x0 = Infinity;
  let y0 = Infinity;
  let x1 = -Infinity;
  let y1 = -Infinity;
  for (const p of poly) {
    x0 = Math.min(x0, p.x);
    y0 = Math.min(y0, p.y);
    x1 = Math.max(x1, p.x);
    y1 = Math.max(y1, p.y);
  }
  const out: string[] = [];
  for (const s of index.seats) {
    if (s.x < x0 || s.x > x1 || s.y < y0 || s.y > y1) continue;
    if (pointInPolygon(s, poly)) out.push(s.key);
  }
  return out;
}

export function seatsOfRow(index: HallIndex, rowId: string): readonly string[] {
  return index.rowById.get(rowId)?.seatKeys ?? [];
}

export function seatsOfSection(index: HallIndex, sectionKey: string): readonly string[] {
  return index.sectionByKey.get(sectionKey)?.seatKeys ?? [];
}

/** The whole row of a seat. */
export function rowSeatsOf(index: HallIndex, seatKey: string): readonly string[] {
  const id = index.rowOfSeat.get(seatKey);
  return id === undefined ? [] : seatsOfRow(index, id);
}

/** The whole section of a seat. */
export function sectionSeatsOf(index: HallIndex, seatKey: string): readonly string[] {
  const s = index.byKey.get(seatKey);
  return s === undefined ? [] : seatsOfSection(index, s.sectionKey);
}

/** Rows `from`..`to` (inclusive, in hall order) of ONE section: the "range" gesture. */
export function rowRange(index: HallIndex, fromRowId: string, toRowId: string): string[] {
  const a = index.rowById.get(fromRowId);
  const b = index.rowById.get(toRowId);
  if (a === undefined || b === undefined || a.sectionKey !== b.sectionKey) return [];
  const section = index.sectionByKey.get(a.sectionKey);
  if (section === undefined) return [];
  const i = section.rowIds.indexOf(fromRowId);
  const j = section.rowIds.indexOf(toRowId);
  if (i < 0 || j < 0) return [];
  const [lo, hi] = i <= j ? [i, j] : [j, i];
  const out: string[] = [];
  for (const id of section.rowIds.slice(lo, hi + 1)) out.push(...seatsOfRow(index, id));
  return out;
}
