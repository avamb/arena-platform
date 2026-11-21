/**
 * The maths of the pan and zoom box (spec 64 section 2.3: the draft is shown
 * large, the person moves and zooms it to compare it with the files). Pure.
 *
 * The content fits the box at scale 1. A view is the scale and the shift of the
 * content's top-left corner, in pixels of the box: a content point `c` shows at
 * `c * scale + shift`. The scale never goes below 1, so the content always
 * covers the box and a shift never leaves an empty edge.
 */
export interface Box {
  w: number;
  h: number;
}

export interface View {
  scale: number;
  x: number;
  y: number;
}

export const MIN_SCALE = 1;
export const MAX_SCALE = 12;
/** One press of "+" or "-". */
export const BUTTON_STEP = 1.5;
/** How fast the wheel zooms: the factor is exp(-deltaY * this). */
export const WHEEL_SPEED = 0.0015;
/** A key press moves the view by this part of the box. */
export const KEY_PAN = 0.15;

export const RESET: View = { scale: MIN_SCALE, x: 0, y: 0 };

function limit(value: number, low: number, high: number): number {
  return Math.min(high, Math.max(low, value));
}

/** Keeps the scale in range and the shift inside the content. */
export function clampView(v: View, box: Box): View {
  const scale = limit(v.scale, MIN_SCALE, MAX_SCALE);
  return {
    scale,
    x: limit(v.x, box.w - box.w * scale, 0),
    y: limit(v.y, box.h - box.h * scale, 0),
  };
}

/** Zooms by `factor` and keeps the point (px, py) of the box where it is. */
export function zoomAt(v: View, factor: number, px: number, py: number, box: Box): View {
  const scale = limit(v.scale * factor, MIN_SCALE, MAX_SCALE);
  const ratio = scale / v.scale;
  return clampView({ scale, x: px - (px - v.x) * ratio, y: py - (py - v.y) * ratio }, box);
}

/** The "+" (direction 1) and "-" (direction -1) buttons zoom around the middle of the box. */
export function zoomStep(v: View, direction: 1 | -1, box: Box): View {
  return zoomAt(v, direction === 1 ? BUTTON_STEP : 1 / BUTTON_STEP, box.w / 2, box.h / 2, box);
}

/** The factor of a wheel turn. A turn up (negative deltaY) zooms in. */
export function wheelFactor(deltaY: number): number {
  return Math.exp(-deltaY * WHEEL_SPEED);
}

/** Moves the view by a drag of (dx, dy) pixels. */
export function panBy(v: View, dx: number, dy: number, box: Box): View {
  return clampView({ ...v, x: v.x + dx, y: v.y + dy }, box);
}

/** The CSS transform of a view. The content's transform-origin is its top-left corner. */
export function transformOf(v: View): string {
  return `translate(${v.x}px, ${v.y}px) scale(${v.scale})`;
}

/** Is the view the whole content? Then "Reset the view" has nothing to do. */
export function isReset(v: View): boolean {
  return v.scale === MIN_SCALE && v.x === 0 && v.y === 0;
}
