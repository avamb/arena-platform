/**
 * pan-zoom.ts — pure pan/zoom transform utilities for the SVG seat map.
 *
 * All functions are pure (no DOM, no side-effects) and operate on a simple
 * `PanZoomState` value object.  The caller is responsible for applying the
 * returned state to the SVG `<g>` wrapper via the `toCSSTransform()` or
 * `toSVGTransform()` helper.
 *
 * Transform-based zoom means no layout reflow: only the `transform` CSS
 * property changes, satisfying the WID-B "no layout thrash" constraint.
 */

// ─── State ────────────────────────────────────────────────────────────────────

export interface PanZoomState {
  /** Horizontal translation in pixels (container-space). */
  x: number;
  /** Vertical translation in pixels (container-space). */
  y: number;
  /** Uniform scale factor (1.0 = identity). */
  scale: number;
}

/** Identity transform — no pan, no zoom. */
export const IDENTITY: PanZoomState = { x: 0, y: 0, scale: 1 };

/** Minimum allowed scale (10 %). */
export const MIN_SCALE = 0.1;

/** Maximum allowed scale (20× zoom). */
export const MAX_SCALE = 20;

// ─── Serialisation ────────────────────────────────────────────────────────────

/**
 * Build a CSS transform string for use in `element.style.transform`.
 * Uses `px` units for translate so CSS pixels match SVG user units when
 * the SVG has `overflow: visible` on the root element.
 */
export function toCSSTransform(s: PanZoomState): string {
  return `translate(${s.x}px, ${s.y}px) scale(${s.scale})`;
}

/**
 * Build an SVG `transform` attribute value for use on a `<g>` wrapper.
 */
export function toSVGTransform(s: PanZoomState): string {
  return `translate(${s.x} ${s.y}) scale(${s.scale})`;
}

// ─── Fit ──────────────────────────────────────────────────────────────────────

/**
 * Compute the "fit" transform so the SVG content fills the container with
 * uniform padding, maintaining the canvas aspect ratio.
 *
 * @param canvasW     Geometry canvas width in SVG user units.
 * @param canvasH     Geometry canvas height in SVG user units.
 * @param containerW  Container width in pixels.
 * @param containerH  Container height in pixels.
 * @param padding     Uniform padding in pixels (default 8).
 */
export function fitTransform(
  canvasW: number,
  canvasH: number,
  containerW: number,
  containerH: number,
  padding = 8,
): PanZoomState {
  if (canvasW <= 0 || canvasH <= 0 || containerW <= 0 || containerH <= 0) {
    return IDENTITY;
  }
  const availW = containerW - padding * 2;
  const availH = containerH - padding * 2;
  if (availW <= 0 || availH <= 0) return IDENTITY;

  const scale = Math.min(availW / canvasW, availH / canvasH);
  // Center the scaled content in the container.
  const x = (containerW - canvasW * scale) / 2;
  const y = (containerH - canvasH * scale) / 2;
  return { x, y, scale };
}

// ─── Wheel zoom ───────────────────────────────────────────────────────────────

/**
 * Apply a wheel-zoom step centered on the pointer position.
 *
 * @param state     Current transform.
 * @param deltaY    Wheel delta (negative = zoom in).
 * @param pointerX  Pointer X in container-space pixels.
 * @param pointerY  Pointer Y in container-space pixels.
 */
export function wheelZoom(
  state: PanZoomState,
  deltaY: number,
  pointerX: number,
  pointerY: number,
): PanZoomState {
  const factor = deltaY < 0 ? 1.1 : 1 / 1.1;
  const newScale = clampScale(state.scale * factor);
  const ratio = newScale / state.scale;
  return {
    x: pointerX - (pointerX - state.x) * ratio,
    y: pointerY - (pointerY - state.y) * ratio,
    scale: newScale,
  };
}

// ─── Drag pan ─────────────────────────────────────────────────────────────────

/**
 * Apply a drag-pan delta to the current transform.
 */
export function pan(state: PanZoomState, dx: number, dy: number): PanZoomState {
  return { ...state, x: state.x + dx, y: state.y + dy };
}

// ─── Pinch zoom ───────────────────────────────────────────────────────────────

/**
 * Apply a two-pointer pinch gesture.
 *
 * @param state     Current transform.
 * @param prevDist  Previous pointer distance (pixels).
 * @param currDist  Current pointer distance (pixels).
 * @param midX      Midpoint X of the two pointers (container-space).
 * @param midY      Midpoint Y of the two pointers (container-space).
 */
export function pinchZoom(
  state: PanZoomState,
  prevDist: number,
  currDist: number,
  midX: number,
  midY: number,
): PanZoomState {
  if (prevDist <= 0) return state;
  const factor = currDist / prevDist;
  const newScale = clampScale(state.scale * factor);
  const ratio = newScale / state.scale;
  return {
    x: midX - (midX - state.x) * ratio,
    y: midY - (midY - state.y) * ratio,
    scale: newScale,
  };
}

// ─── Pointer geometry ─────────────────────────────────────────────────────────

/** Euclidean distance between two pointer positions. */
export function pointerDist(
  p1: { x: number; y: number },
  p2: { x: number; y: number },
): number {
  const dx = p2.x - p1.x;
  const dy = p2.y - p1.y;
  return Math.sqrt(dx * dx + dy * dy);
}

/** Midpoint between two pointer positions. */
export function pointerMid(
  p1: { x: number; y: number },
  p2: { x: number; y: number },
): { x: number; y: number } {
  return { x: (p1.x + p2.x) / 2, y: (p1.y + p2.y) / 2 };
}

// ─── Internal ─────────────────────────────────────────────────────────────────

function clampScale(s: number): number {
  return Math.max(MIN_SCALE, Math.min(MAX_SCALE, s));
}
