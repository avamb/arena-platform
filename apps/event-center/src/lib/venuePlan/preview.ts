/**
 * A small preview of a seating plan for the venue card: its seats as dots, read
 * from the geometry of the current version. Pure. The card draws the dots as
 * SVG elements (never as markup from a string).
 */
export interface PreviewDot {
  x: number;
  y: number;
}

export interface Preview {
  /** The box of the dots, with a margin. */
  viewBox: { x: number; y: number; w: number; h: number };
  dots: PreviewDot[];
  /** The dot radius that suits the box. */
  radius: number;
}

interface GeometryLike {
  sections?: { rows?: { seats?: { x?: unknown; y?: unknown }[] }[] }[];
}

/** At most this many dots are drawn: a hall of 3000 seats is a sample. */
export const MAX_DOTS = 600;

export function previewOf(geometry: unknown, maxDots = MAX_DOTS): Preview | null {
  if (geometry === null || typeof geometry !== "object") return null;
  const all: PreviewDot[] = [];
  for (const section of (geometry as GeometryLike).sections ?? []) {
    for (const row of section.rows ?? []) {
      for (const seat of row.seats ?? []) {
        if (typeof seat.x === "number" && typeof seat.y === "number" && Number.isFinite(seat.x) && Number.isFinite(seat.y)) {
          all.push({ x: seat.x, y: seat.y });
        }
      }
    }
  }
  if (all.length === 0) return null;
  const step = Math.max(1, Math.ceil(all.length / Math.max(1, maxDots)));
  const dots = all.filter((_, i) => i % step === 0);
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const d of all) {
    minX = Math.min(minX, d.x);
    minY = Math.min(minY, d.y);
    maxX = Math.max(maxX, d.x);
    maxY = Math.max(maxY, d.y);
  }
  const w = Math.max(1, maxX - minX);
  const h = Math.max(1, maxY - minY);
  const margin = Math.max(w, h) * 0.05;
  const radius = Math.max(w, h) / 150;
  return { viewBox: { x: minX - margin, y: minY - margin, w: w + margin * 2, h: h + margin * 2 }, dots, radius };
}
